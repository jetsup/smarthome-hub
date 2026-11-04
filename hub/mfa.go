package hub

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// Optional two-step verification. A user can turn on any mix of:
//
//   phone  — approve in the SmartHomes app: the website shows a number, the phone
//            asks you to type it and tap Approve (number matching, like Microsoft)
//   totp   — a code from an authenticator app (Google Authenticator, Authy, ...)
//   email  — a code sent by email (lifetime: MFA_EMAIL_CODE_TTL_MINUTES)
//
// plus one-time recovery codes for a lost phone.

const (
	mfaApproveTTL       = 2 * time.Minute
	mfaMaxAttempts      = 5
	mfaPhoneMaxAttempts = 3
	mfaStepTTL          = 10 * time.Minute
	mfaRecoveryN        = 10
	maxPhones           = 10
)

// Challenge kinds.
const (
	kindApprove     = "approve"      // sign-in: number on the web, typed + approved on the phone
	kindEmail       = "email"        // sign-in: code emailed to the user
	kindEnable      = "enable"       // turning on phone approval: number on the phone, typed on the web
	kindEnableEmail = "enable-email" // turning on email codes
)

type mfaChallenge struct {
	id            string
	userID        uint
	kind          string
	code          string
	created       time.Time
	expires       time.Time
	attempts      int // typed on the website
	phoneAttempts int // typed on the phone
	approved      bool
	denied        bool
	consumed      bool
	ip, label     string // who asked (shown on the phone)
	deviceKey     string // carried to the session that sign-in creates
	deviceLabel   string
}

var mfa = struct {
	mu     sync.Mutex
	ch     map[string]*mfaChallenge
	starts map[uint][]time.Time // challenges started per user
	fails  map[uint][]time.Time // wrong codes per user
}{ch: map[string]*mfaChallenge{}, starts: map[uint][]time.Time{}, fails: map[uint][]time.Time{}}

func pruneMfaLocked() {
	now := time.Now()
	for id, c := range mfa.ch {
		if now.After(c.expires.Add(time.Minute)) {
			delete(mfa.ch, id)
		}
	}
}

func sixDigits() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
	}
	return fmt.Sprintf("%06d", n.Int64())
}

var deviceKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

func sanitizeDeviceKey(s string) string {
	if deviceKeyRe.MatchString(s) {
		return s
	}
	return ""
}

// ── step token (proves the password was right; useless for the API) ──────────

func generateMFAToken(u User, deviceKey, deviceLabel string) (string, error) {
	claims := Claims{
		UserID: u.ID, Email: u.Email, Role: "mfa", DeviceKey: deviceKey, DeviceLabel: deviceLabel,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(mfaStepTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret())
}

func userFromStepToken(raw string) (User, *Claims, bool) {
	var u User
	cl, ok := parseTokenString(raw)
	if !ok || cl.Role != "mfa" {
		return u, nil, false
	}
	if err := DB.First(&u, cl.UserID).Error; err != nil || !u.MfaEnabled {
		return u, nil, false
	}
	return u, cl, true
}

// ── recovery codes ───────────────────────────────────────────────────────────

func hashRecovery(code string) string {
	norm := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

func recoveryHashes(u User) []string {
	var h []string
	if u.MfaRecovery != "" {
		json.Unmarshal([]byte(u.MfaRecovery), &h)
	}
	return h
}

func newRecoveryCodes() (plain []string, stored string) {
	hashes := make([]string, 0, mfaRecoveryN)
	for i := 0; i < mfaRecoveryN; i++ {
		b := make([]byte, 5)
		rand.Read(b)
		h := hex.EncodeToString(b)
		code := h[:5] + "-" + h[5:]
		plain = append(plain, code)
		hashes = append(hashes, hashRecovery(code))
	}
	j, _ := json.Marshal(hashes)
	return plain, string(j)
}

func consumeRecovery(u User, code string) bool {
	want := hashRecovery(code)
	hs := recoveryHashes(u)
	for i, h := range hs {
		if subtle.ConstantTimeCompare([]byte(h), []byte(want)) == 1 {
			hs = append(hs[:i], hs[i+1:]...)
			j, _ := json.Marshal(hs)
			DB.Model(&u).Update("mfa_recovery", string(j))
			return true
		}
	}
	return false
}

// ── challenges ───────────────────────────────────────────────────────────────

func startChallenge(c *gin.Context, userID uint, kind string, ttl time.Duration, deviceKey, deviceLabel string) (*mfaChallenge, string) {
	mfa.mu.Lock()
	defer mfa.mu.Unlock()
	pruneMfaLocked()
	mfa.starts[userID] = recent(mfa.starts[userID], 10*time.Minute)
	if len(mfa.starts[userID]) >= 8 {
		return nil, "Too many verification requests. Wait a few minutes and try again."
	}
	mfa.starts[userID] = append(mfa.starts[userID], time.Now())
	for id, ch := range mfa.ch { // only the latest request of a kind stays valid
		if ch.userID == userID && ch.kind == kind && !ch.consumed {
			delete(mfa.ch, id)
		}
	}
	label, _ := uaLabel(c.Request.UserAgent())
	ch := &mfaChallenge{id: randHex(8), userID: userID, kind: kind, code: sixDigits(), created: time.Now(),
		expires: time.Now().Add(ttl), ip: c.ClientIP(), label: label, deviceKey: deviceKey, deviceLabel: deviceLabel}
	mfa.ch[ch.id] = ch
	return ch, ""
}

// checkTyped validates a code typed on the website (email / enable flows).
func checkTyped(userID uint, id, code, kind string) (ok bool, errMsg string, status int) {
	mfa.mu.Lock()
	defer mfa.mu.Unlock()
	pruneMfaLocked()
	mfa.fails[userID] = recent(mfa.fails[userID], 10*time.Minute)
	if len(mfa.fails[userID]) >= 10 {
		return false, "Too many wrong attempts. Wait a few minutes.", http.StatusTooManyRequests
	}
	ch := mfa.ch[id]
	if ch == nil || ch.userID != userID || ch.kind != kind || ch.consumed || time.Now().After(ch.expires) {
		return false, "That code expired. Ask for a new one.", http.StatusGone
	}
	ch.attempts++
	if subtle.ConstantTimeCompare([]byte(strings.ReplaceAll(code, " ", "")), []byte(ch.code)) != 1 {
		mfa.fails[userID] = append(mfa.fails[userID], time.Now())
		if ch.attempts >= mfaMaxAttempts {
			delete(mfa.ch, id)
			return false, "Too many wrong codes. Ask for a new one.", http.StatusTooManyRequests
		}
		return false, "That code is not right.", http.StatusUnauthorized
	}
	ch.consumed = true
	return true, "", 0
}

func noteFailure(userID uint) {
	mfa.mu.Lock()
	mfa.fails[userID] = append(recent(mfa.fails[userID], 10*time.Minute), time.Now())
	mfa.mu.Unlock()
}

func tooManyFailures(userID uint) bool {
	mfa.mu.Lock()
	defer mfa.mu.Unlock()
	mfa.fails[userID] = recent(mfa.fails[userID], 10*time.Minute)
	return len(mfa.fails[userID]) >= 10
}

// ── sign-in (public; carries the step token) ─────────────────────────────────

func activePhones(userID uint) int64 {
	var n int64
	DB.Model(&MfaDevice{}).Where("user_id = ?", userID).Count(&n)
	return n
}

// MfaLoginChallenge starts phone approval or sends an email code.
func MfaLoginChallenge(c *gin.Context) {
	var req struct {
		MfaToken string `json:"mfaToken"`
		Method   string `json:"method"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	u, cl, ok := userFromStepToken(req.MfaToken)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Verification expired — sign in again"})
		return
	}
	switch req.Method {
	case "", "phone":
		if !u.MfaPhone || activePhones(u.ID) == 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "Approving in the app is not set up for this account."})
			return
		}
		ch, why := startChallenge(c, u.ID, kindApprove, mfaApproveTTL, cl.DeviceKey, cl.DeviceLabel)
		if ch == nil {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": why})
			return
		}
		// The NUMBER is shown here, on the website. The phone must be told to type it.
		c.JSON(http.StatusOK, gin.H{"method": "phone", "challengeId": ch.id, "number": ch.code, "expiresIn": int(mfaApproveTTL.Seconds())})
	case "email":
		if !u.MfaEmail {
			c.JSON(http.StatusConflict, gin.H{"error": "Email codes are not turned on for this account."})
			return
		}
		ttl := mfaEmailTTL()
		ch, why := startChallenge(c, u.ID, kindEmail, ttl, cl.DeviceKey, cl.DeviceLabel)
		if ch == nil {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": why})
			return
		}
		if err := sendCodeEmail(u, ch, "Your SmartHome sign-in code", "Finish signing in",
			"Use this code to finish signing in to your SmartHome account.", ttl); err != nil {
			mfa.mu.Lock()
			delete(mfa.ch, ch.id)
			mfa.mu.Unlock()
			c.JSON(http.StatusBadGateway, gin.H{"error": "We could not send the email: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"method": "email", "challengeId": ch.id, "expiresIn": int(ttl.Seconds()), "sentTo": maskEmail(u.Email)})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown method"})
	}
}

// sendCodeEmail renders the shared template and sends it.
func sendCodeEmail(u User, ch *mfaChallenge, subject, heading, intro string, ttl time.Duration) error {
	d := CodeEmail{
		Subject:   subject,
		Preheader: "Your code is " + ch.code + " — it expires in " + fmt.Sprintf("%d", int(ttl.Minutes())) + " minutes.",
		Heading:   heading,
		Intro:     intro,
		Code:      ch.code,
		Minutes:   int(ttl.Minutes()),
		Details: [][2]string{
			{"Account", u.Email},
			{"Requested from", ch.label},
			{"IP address", ch.ip},
			{"Time", ch.created.UTC().Format("2 Jan 2006, 15:04 UTC")},
		},
		Notice: "Someone may know your password. Change it right away and sign out unknown devices from Profile & security.",
	}
	h, t := renderCodeEmail(d)
	return SendMail(u.Email, subject, h, t)
}

// MfaLoginPoll — the website polls while the user approves on their phone.
func MfaLoginPoll(c *gin.Context) {
	var req struct {
		MfaToken    string `json:"mfaToken"`
		ChallengeID string `json:"challengeId"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	u, cl, ok := userFromStepToken(req.MfaToken)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Verification expired — sign in again"})
		return
	}
	mfa.mu.Lock()
	ch := mfa.ch[req.ChallengeID]
	status := "expired"
	if ch != nil && ch.userID == u.ID && ch.kind == kindApprove && !ch.consumed {
		switch {
		case ch.denied:
			status = "denied"
		case time.Now().After(ch.expires):
			status = "expired"
		case ch.approved:
			status = "approved"
			ch.consumed = true
		default:
			status = "pending"
		}
	}
	mfa.mu.Unlock()
	if status != "approved" {
		c.JSON(http.StatusOK, gin.H{"status": status})
		return
	}
	payload, ok := loginPayload(c, u, cl.DeviceKey, cl.DeviceLabel)
	if !ok {
		return
	}
	payload["status"] = "approved"
	c.JSON(http.StatusOK, payload)
}

// MfaLoginVerify finishes sign-in with a typed code: authenticator, email or recovery.
func MfaLoginVerify(c *gin.Context) {
	var req struct {
		MfaToken     string `json:"mfaToken"`
		Method       string `json:"method"`
		ChallengeID  string `json:"challengeId"`
		Code         string `json:"code"`
		RecoveryCode string `json:"recoveryCode"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	u, cl, ok := userFromStepToken(req.MfaToken)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Verification expired — sign in again"})
		return
	}
	if req.RecoveryCode != "" || req.Method == "recovery" {
		if tooManyFailures(u.ID) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many wrong attempts. Wait a few minutes."})
			return
		}
		code := req.RecoveryCode
		if code == "" {
			code = req.Code
		}
		if !consumeRecovery(u, code) {
			noteFailure(u.ID)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "That recovery code is not valid"})
			return
		}
		uid := fmt.Sprintf("%d", u.ID)
		LogAudit(c, "mfa_recovery_used", "user", &uid, "Signed in with a recovery code")
		finishLogin(c, u, cl.DeviceKey, cl.DeviceLabel)
		return
	}
	switch req.Method {
	case "totp":
		if !u.MfaTotp {
			c.JSON(http.StatusConflict, gin.H{"error": "Authenticator codes are not turned on for this account."})
			return
		}
		if tooManyFailures(u.ID) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many wrong attempts. Wait a few minutes."})
			return
		}
		if !acceptTotp(&u, req.Code) {
			noteFailure(u.ID)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "That code is not right or was already used."})
			return
		}
	case "email":
		if ok, msg, status := checkTyped(u.ID, req.ChallengeID, req.Code, kindEmail); !ok {
			c.JSON(status, gin.H{"error": msg})
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown method"})
		return
	}
	finishLogin(c, u, cl.DeviceKey, cl.DeviceLabel)
}

// ── phones (the app registers itself) ────────────────────────────────────────

func requireDeviceKey(c *gin.Context) (MfaDevice, bool) {
	var d MfaDevice
	key := c.GetHeader("X-Device-Key")
	if key == "" || DB.Where("device_key = ? AND user_id = ?", key, c.GetUint("userID")).First(&d).Error != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "This device is not registered for sign-in approvals"})
		return d, false
	}
	return d, true
}

func MfaRegisterDevice(c *gin.Context) {
	userID := c.GetUint("userID")
	var req struct {
		DeviceKey    string `json:"deviceKey"`
		Name         string `json:"name"`
		Model        string `json:"model"`
		Manufacturer string `json:"manufacturer"`
		OS           string `json:"os"`
	}
	if c.ShouldBindJSON(&req) != nil || sanitizeDeviceKey(req.DeviceKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device"})
		return
	}
	model := tidyName(req.Model, 80)
	maker := tidyName(req.Manufacturer, 60)
	osName := tidyName(req.OS, 40)
	name := tidyName(req.Name, 80)
	if name == "" {
		name = strings.TrimSpace(maker + " " + model)
	}
	if name == "" {
		name = "Phone"
	}
	var d MfaDevice
	if DB.Where("device_key = ?", req.DeviceKey).First(&d).Error == nil {
		if d.UserID != userID {
			c.JSON(http.StatusConflict, gin.H{"error": "This phone belongs to another account"})
			return
		}
		DB.Model(&d).Updates(map[string]interface{}{"name": name, "model": model, "manufacturer": maker, "os": osName, "last_ip": c.ClientIP(), "last_seen": time.Now()})
	} else {
		var n int64
		DB.Model(&MfaDevice{}).Where("user_id = ?", userID).Count(&n)
		if n >= maxPhones {
			c.JSON(http.StatusConflict, gin.H{"error": "Too many phones — remove one first"})
			return
		}
		d = MfaDevice{UserID: userID, DeviceKey: req.DeviceKey, Name: name, Model: model, Manufacturer: maker, OS: osName, LastIP: c.ClientIP(), LastSeen: time.Now()}
		DB.Create(&d)
	}
	LogAudit(c, "device_registered", "user", nil, "Phone registered: "+d.Name+" ("+strings.TrimSpace(d.Manufacturer+" "+d.Model)+")")
	c.JSON(http.StatusOK, gin.H{"id": d.ID, "name": d.Name})
}

func MfaListDevices(c *gin.Context) {
	var ds []MfaDevice
	DB.Where("user_id = ?", c.GetUint("userID")).Order("created_at ASC").Find(&ds)
	c.JSON(http.StatusOK, ds)
}

// MfaDeleteDevice removes a phone AND signs out every session that phone holds.
func MfaDeleteDevice(c *gin.Context) {
	u, ok := currentUser(c)
	if !ok {
		return
	}
	var d MfaDevice
	if DB.Where("id = ? AND user_id = ?", c.Param("id"), u.ID).First(&d).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Phone not found"})
		return
	}
	if u.MfaPhone && activePhones(u.ID) <= 1 {
		c.JSON(http.StatusConflict, gin.H{"error": "This is your only phone for app approvals. Turn that method off first, or add another phone."})
		return
	}
	var ss []UserSession
	DB.Where("user_id = ? AND device_key = ?", u.ID, d.DeviceKey).Find(&ss)
	for _, s := range ss {
		forgetSession(s.ID)
	}
	DB.Where("user_id = ? AND device_key = ?", u.ID, d.DeviceKey).Delete(&UserSession{})
	DB.Delete(&d)
	LogAudit(c, "device_removed", "user", nil, "Phone removed: "+d.Name+", signed out "+strconv.Itoa(len(ss))+" session(s)")
	c.JSON(http.StatusOK, gin.H{"status": "removed", "signedOut": len(ss)})
}

// MfaPending is polled by the phone: the requests waiting for it.
//
//	sign-in (approve): the phone must type the number shown on the website — the number is NOT sent here
//	enable: the phone shows the number to type on the website
func MfaPending(c *gin.Context) {
	d, ok := requireDeviceKey(c)
	if !ok {
		return
	}
	DB.Model(&d).Updates(map[string]interface{}{"last_seen": time.Now(), "last_ip": c.ClientIP()})
	mfa.mu.Lock()
	defer mfa.mu.Unlock()
	pruneMfaLocked()
	out := []gin.H{}
	now := time.Now()
	for _, ch := range mfa.ch {
		if ch.userID != d.UserID || ch.consumed || ch.denied || ch.approved || now.After(ch.expires) {
			continue
		}
		if ch.kind != kindApprove && ch.kind != kindEnable {
			continue
		}
		item := gin.H{"id": ch.id, "expiresIn": int(time.Until(ch.expires).Seconds()), "device": ch.label, "ip": ch.ip, "createdAt": ch.created}
		if ch.kind == kindApprove {
			item["purpose"] = "login"
			item["needsNumber"] = true
		} else {
			item["purpose"] = "enable"
			item["needsNumber"] = false
			item["code"] = ch.code
		}
		out = append(out, item)
	}
	c.JSON(http.StatusOK, out)
}

// MfaApprove — the user types the number from the website on their phone and taps Approve.
func MfaApprove(c *gin.Context) {
	if _, ok := requireDeviceKey(c); !ok {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	userID := c.GetUint("userID")
	mfa.mu.Lock()
	defer mfa.mu.Unlock()
	ch := mfa.ch[c.Param("id")]
	if ch == nil || ch.userID != userID || ch.kind != kindApprove || ch.consumed || ch.approved || ch.denied || time.Now().After(ch.expires) {
		c.JSON(http.StatusGone, gin.H{"error": "That request expired."})
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.ReplaceAll(req.Code, " ", "")), []byte(ch.code)) != 1 {
		ch.phoneAttempts++
		left := mfaPhoneMaxAttempts - ch.phoneAttempts
		if left <= 0 {
			ch.denied = true // too many wrong numbers: refuse the sign-in
			c.JSON(http.StatusForbidden, gin.H{"error": "Too many wrong numbers — the sign-in was blocked.", "attemptsLeft": 0})
			return
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "That number does not match the one on the website.", "attemptsLeft": left})
		return
	}
	ch.approved = true
	uid := fmt.Sprintf("%d", userID)
	LogAudit(c, "mfa_approved", "user", &uid, "Sign-in approved on a phone")
	c.JSON(http.StatusOK, gin.H{"status": "approved"})
}

// MfaDeny rejects a sign-in the user did not start.
func MfaDeny(c *gin.Context) {
	if _, ok := requireDeviceKey(c); !ok {
		return
	}
	mfa.mu.Lock()
	defer mfa.mu.Unlock()
	if ch := mfa.ch[c.Param("id")]; ch != nil && ch.userID == c.GetUint("userID") {
		ch.denied = true
		uid := fmt.Sprintf("%d", ch.userID)
		LogAudit(c, "mfa_denied", "user", &uid, "A sign-in request was denied on the phone")
	}
	c.JSON(http.StatusOK, gin.H{"status": "denied"})
}

// ── turning methods on / off ─────────────────────────────────────────────────

// enableMethod switches a method on. Recovery codes are created with the first one.
func enableMethod(u User, method string) []string {
	upd := map[string]interface{}{"mfa_enabled": true}
	switch method {
	case "phone":
		upd["mfa_phone"] = true
	case "totp":
		upd["mfa_totp"] = true
	case "email":
		upd["mfa_email"] = true
	}
	var codes []string
	if len(recoveryHashes(u)) == 0 && !u.MfaEnabled {
		plain, stored := newRecoveryCodes()
		upd["mfa_recovery"] = stored
		codes = plain
	}
	DB.Model(&u).Updates(upd)
	uid := fmt.Sprintf("%d", u.ID)
	auditUser(u, "mfa_enabled", "Two-step method turned on: "+method)
	_ = uid
	return codes
}

// disableMethod switches one off; turning off the last one turns MFA off entirely.
func disableMethod(u User, method string) {
	flags := map[string]bool{"phone": u.MfaPhone, "totp": u.MfaTotp, "email": u.MfaEmail}
	flags[method] = false
	upd := map[string]interface{}{}
	switch method {
	case "phone":
		upd["mfa_phone"] = false
	case "totp":
		upd["mfa_totp"] = false
		upd["totp_secret"] = ""
		upd["totp_last_step"] = 0
	case "email":
		upd["mfa_email"] = false
	}
	if !flags["phone"] && !flags["totp"] && !flags["email"] {
		upd["mfa_enabled"] = false
		upd["mfa_recovery"] = ""
	}
	DB.Model(&u).Updates(upd)
	uid := fmt.Sprintf("%d", u.ID)
	auditUser(u, "mfa_disabled", "Two-step method turned off: "+method)
	_ = uid
}

func codesResponse(codes []string) gin.H {
	if codes == nil {
		codes = []string{}
	}
	return gin.H{"recoveryCodes": codes}
}

// Phone approval ---------------------------------------------------------------

func MfaEnableStart(c *gin.Context) {
	u, ok := currentUser(c)
	if !ok {
		return
	}
	if u.MfaPhone {
		c.JSON(http.StatusConflict, gin.H{"error": "Approving in the app is already on"})
		return
	}
	if activePhones(u.ID) == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Sign in to the SmartHomes app on your phone first — it receives the approval request."})
		return
	}
	ch, why := startChallenge(c, u.ID, kindEnable, mfaApproveTTL, "", "")
	if ch == nil {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": why})
		return
	}
	c.JSON(http.StatusOK, gin.H{"challengeId": ch.id, "expiresIn": int(mfaApproveTTL.Seconds())})
}

func MfaEnableConfirm(c *gin.Context) {
	u, ok := currentUser(c)
	if !ok {
		return
	}
	var req struct {
		ChallengeID string `json:"challengeId"`
		Code        string `json:"code"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if ok, msg, status := checkTyped(u.ID, req.ChallengeID, req.Code, kindEnable); !ok {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	c.JSON(http.StatusOK, codesResponse(enableMethod(u, "phone")))
}

// Shared password-confirmed actions ------------------------------------------------

func verifyPasswordBody(c *gin.Context) (User, bool) {
	u, ok := currentUser(c)
	if !ok {
		return u, false
	}
	var req struct {
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&req) != nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Your password is incorrect"})
		return u, false
	}
	return u, true
}

// MfaMethodDisable turns one method off (needs the password).
func MfaMethodDisable(c *gin.Context) {
	u, ok := verifyPasswordBody(c)
	if !ok {
		return
	}
	m := c.Param("method")
	if m != "phone" && m != "totp" && m != "email" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown method"})
		return
	}
	disableMethod(u, m)
	c.JSON(http.StatusOK, gin.H{"status": "off"})
}

// MfaDisable turns everything off.
func MfaDisable(c *gin.Context) {
	u, ok := verifyPasswordBody(c)
	if !ok {
		return
	}
	DB.Model(&u).Updates(map[string]interface{}{"mfa_enabled": false, "mfa_phone": false, "mfa_totp": false, "mfa_email": false, "totp_secret": "", "totp_last_step": 0, "mfa_recovery": ""})
	uid := fmt.Sprintf("%d", u.ID)
	LogAudit(c, "mfa_disabled", "user", &uid, "Two-step verification turned off")
	c.JSON(http.StatusOK, gin.H{"status": "off"})
}

func MfaRegenerateRecovery(c *gin.Context) {
	u, ok := verifyPasswordBody(c)
	if !ok {
		return
	}
	if !u.MfaEnabled {
		c.JSON(http.StatusConflict, gin.H{"error": "Two-step verification is off"})
		return
	}
	plain, stored := newRecoveryCodes()
	DB.Model(&u).Update("mfa_recovery", stored)
	uid := fmt.Sprintf("%d", u.ID)
	LogAudit(c, "mfa_recovery_regenerated", "user", &uid, "Recovery codes regenerated")
	c.JSON(http.StatusOK, gin.H{"recoveryCodes": plain})
}
