package hub

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/skip2/go-qrcode"
)

// ── authenticator app (TOTP) ─────────────────────────────────────────────────

// The shared secret is stored encrypted (AES-GCM, key derived from JWT_SECRET) so a
// copy of the database alone does not hand out working authenticator codes.
func totpKey() []byte {
	k := sha256.Sum256(append([]byte("smarthome-totp-v1:"), secret()...))
	return k[:]
}

func encryptSecret(plain string) (string, error) {
	block, err := aes.NewCipher(totpKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func decryptSecret(enc string) (string, error) {
	raw, err := base64.RawStdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(totpKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("bad secret")
	}
	out, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	return string(out), err
}

var totpOpts = totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}

// matchTotp returns the 30-second step a code belongs to (±1 step of clock drift).
func matchTotp(secretPlain, code string) (int64, bool) {
	code = strings.ReplaceAll(code, " ", "")
	now := time.Now().UTC()
	for _, off := range []int{0, -1, 1} {
		t := now.Add(time.Duration(off*30) * time.Second)
		want, err := totp.GenerateCodeCustom(secretPlain, t, totpOpts)
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return t.Unix() / 30, true
		}
	}
	return 0, false
}

// acceptTotp validates a sign-in code and burns its step so it cannot be replayed.
func acceptTotp(u *User, code string) bool {
	if u.TotpSecret == "" {
		return false
	}
	plain, err := decryptSecret(u.TotpSecret)
	if err != nil {
		return false
	}
	step, ok := matchTotp(plain, code)
	if !ok || step <= u.TotpLastStep {
		return false
	}
	DB.Model(u).Update("totp_last_step", step)
	u.TotpLastStep = step
	return true
}

// MfaTotpSetup creates a new secret and returns what an authenticator needs:
// the otpauth URI, the same thing as a QR image, and the key to type by hand.
func MfaTotpSetup(c *gin.Context) {
	u, ok := currentUser(c)
	if !ok {
		return
	}
	if u.MfaTotp {
		c.JSON(http.StatusConflict, gin.H{"error": "An authenticator app is already set up"})
		return
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "SmartHome", AccountName: u.Email, Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not create a secret"})
		return
	}
	enc, err := encryptSecret(key.Secret())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not store the secret"})
		return
	}
	DB.Model(&u).Updates(map[string]interface{}{"totp_secret": enc, "totp_last_step": 0})
	png, err := qrcode.Encode(key.URL(), qrcode.Medium, 280)
	qr := ""
	if err == nil {
		qr = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	}
	c.JSON(http.StatusOK, gin.H{"secret": key.Secret(), "uri": key.URL(), "qr": qr})
}

// MfaTotpConfirm checks the first code from the app, then turns the method on.
func MfaTotpConfirm(c *gin.Context) {
	u, ok := currentUser(c)
	if !ok {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if c.ShouldBindJSON(&req) != nil || u.TotpSecret == "" || u.MfaTotp {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Start the setup first"})
		return
	}
	if tooManyFailures(u.ID) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many wrong attempts. Wait a few minutes."})
		return
	}
	if !acceptTotp(&u, req.Code) {
		noteFailure(u.ID)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "That code is not right. Check the time on your phone and try the next code."})
		return
	}
	c.JSON(http.StatusOK, codesResponse(enableMethod(u, "totp")))
}

// ── email codes ──────────────────────────────────────────────────────────────

func MfaEmailStart(c *gin.Context) {
	u, ok := currentUser(c)
	if !ok {
		return
	}
	if u.MfaEmail {
		c.JSON(http.StatusConflict, gin.H{"error": "Email codes are already on"})
		return
	}
	if !mailConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Email is not set up on this hub yet. Set SMTP_HOST and SMTP_FROM in the hub's .env."})
		return
	}
	ttl := mfaEmailTTL()
	ch, why := startChallenge(c, u.ID, kindEnableEmail, ttl, "", "")
	if ch == nil {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": why})
		return
	}
	if err := sendCodeEmail(u, ch, "Verify your email for two-step verification", "Confirm your email",
		"Enter this code in SmartHome to turn on sign-in codes by email.", ttl); err != nil {
		mfa.mu.Lock()
		delete(mfa.ch, ch.id)
		mfa.mu.Unlock()
		c.JSON(http.StatusBadGateway, gin.H{"error": "We could not send the email: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"challengeId": ch.id, "expiresIn": int(ttl.Seconds()), "sentTo": maskEmail(u.Email)})
}

func MfaEmailConfirm(c *gin.Context) {
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
	if ok, msg, status := checkTyped(u.ID, req.ChallengeID, req.Code, kindEnableEmail); !ok {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	c.JSON(http.StatusOK, codesResponse(enableMethod(u, "email")))
}
