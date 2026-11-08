package hub

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ── Displays (HMIs) ───────────────────────────────────────────────────────────
//
// A display is paired to ONE gateway and shows only the nodes assigned to it.
// A node is assigned to at most one display (Node.HmiID is a single column), so
// it can never be controlled from two displays at once.

const (
	hmiOnlineWindow  = 90 * time.Second // a display polls every few seconds
	claimTTL         = 10 * time.Minute
	claimDeliveryTTL = 3 * time.Minute // after a claim, how long the display has to collect its token
	claimMaxFails    = 5               // wrong codes per user per window
	claimFailWindow  = 10 * time.Minute
)

var deviceIDRe = regexp.MustCompile(`^[A-Za-z0-9:_-]{6,40}$`)

func sanitizeDeviceID(s string) string {
	s = strings.TrimSpace(s)
	if deviceIDRe.MatchString(s) {
		return s
	}
	return ""
}

func tidyName(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		r = r[:max]
	}
	return string(r)
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ── token validity cache (also records "last seen") ───────────────────────────

type hmiCacheEntry struct {
	gatewayID string
	checked   time.Time
	wrote     time.Time
}

var hmiCache = struct {
	mu sync.Mutex
	m  map[uint]*hmiCacheEntry
}{m: map[uint]*hmiCacheEntry{}}

func forgetHmi(id uint) {
	hmiCache.mu.Lock()
	delete(hmiCache.m, id)
	hmiCache.mu.Unlock()
}

// touchHmi confirms the display still exists on the token's gateway and
// refreshes its last-seen time (throttled). Unpairing takes effect at once.
func touchHmi(id uint, gatewayID string) bool {
	now := time.Now()
	hmiCache.mu.Lock()
	if e := hmiCache.m[id]; e != nil && e.gatewayID == gatewayID && now.Sub(e.checked) < 5*time.Second {
		write := now.Sub(e.wrote) > 10*time.Second
		if write {
			e.wrote = now
		}
		hmiCache.mu.Unlock()
		if write {
			DB.Model(&Hmi{}).Where("id = ?", id).Update("last_seen", now)
		}
		return true
	}
	hmiCache.mu.Unlock()

	var h Hmi
	if err := DB.Select("id", "gateway_id").First(&h, id).Error; err != nil || h.GatewayID != gatewayID {
		forgetHmi(id)
		return false
	}
	DB.Model(&Hmi{}).Where("id = ?", id).Update("last_seen", now)
	hmiCache.mu.Lock()
	hmiCache.m[id] = &hmiCacheEntry{gatewayID: gatewayID, checked: now, wrote: now}
	hmiCache.mu.Unlock()
	return true
}

// ── creation / defaults ───────────────────────────────────────────────────────

// createHmi registers (or re-pairs) a display on a gateway. The first display on
// a gateway takes over its unassigned nodes; later displays start empty and the
// user assigns nodes to them, which keeps every node on exactly one display.
func createHmi(gw Gateway, deviceID, name string) (*Hmi, error) {
	if deviceID == "" {
		deviceID = "legacy-" + randHex(6)
	}
	name = tidyName(name, 40)
	now := time.Now()

	var h Hmi
	if DB.Omit("lock_bg").Where("device_id = ?", deviceID).First(&h).Error == nil {
		// Same hardware paired again (maybe to another gateway): drop old assignments.
		DB.Model(&Node{}).Where("hmi_id = ?", h.ID).Update("hmi_id", nil)
		h.GatewayID, h.UserID, h.LastSeen = gw.ID, gw.UserID, now
		if name != "" {
			h.Name = name
		}
		if err := DB.Save(&h).Error; err != nil {
			return nil, err
		}
		forgetHmi(h.ID)
	} else {
		if name == "" {
			var n int64
			DB.Model(&Hmi{}).Where("user_id = ?", gw.UserID).Count(&n)
			name = "Display " + itoa(int(n)+1)
		}
		h = Hmi{DeviceID: deviceID, Mac: normMac(deviceID), GatewayID: gw.ID, UserID: gw.UserID, Name: name, LastSeen: now}
		if err := DB.Create(&h).Error; err != nil {
			return nil, err
		}
	}

	var others int64
	DB.Model(&Hmi{}).Where("gateway_id = ? AND id <> ?", gw.ID, h.ID).Count(&others)
	if others == 0 {
		DB.Model(&Node{}).Where("gateway_id = ? AND hmi_id IS NULL", gw.ID).Update("hmi_id", h.ID)
	}
	return &h, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

// defaultHmiFor picks the display a brand-new node should appear on: only when
// the gateway has exactly one display; otherwise the node waits to be assigned.
func defaultHmiFor(gatewayID string) *uint {
	var hs []Hmi
	DB.Select("id").Where("gateway_id = ?", gatewayID).Limit(2).Find(&hs)
	if len(hs) == 1 {
		id := hs[0].ID
		return &id
	}
	return nil
}

func deleteHmisOfGateway(gatewayID string) {
	var hs []Hmi
	DB.Select("id").Where("gateway_id = ?", gatewayID).Find(&hs)
	for _, h := range hs {
		forgetHmi(h.ID)
	}
	DB.Where("gateway_id = ?", gatewayID).Delete(&Hmi{})
}

// ── claim flow: the display shows a code, the owner confirms it ───────────────

type hmiClaim struct {
	code, secret, deviceID, name string
	expires                      time.Time
	done                         bool
	token, gatewayID, gwName     string
	hmiID                        uint
}

var claims = struct {
	mu       sync.Mutex
	byCode   map[string]*hmiClaim
	byDevice map[string]*hmiClaim
	fails    map[uint][]time.Time // wrong-code attempts per user
	regs     map[string][]time.Time
}{byCode: map[string]*hmiClaim{}, byDevice: map[string]*hmiClaim{}, fails: map[uint][]time.Time{}, regs: map[string][]time.Time{}}

func pruneClaimsLocked() {
	now := time.Now()
	for code, cl := range claims.byCode {
		if now.After(cl.expires) {
			delete(claims.byCode, code)
			if claims.byDevice[cl.deviceID] == cl {
				delete(claims.byDevice, cl.deviceID)
			}
		}
	}
}

func recent(ts []time.Time, window time.Duration) []time.Time {
	cut := time.Now().Add(-window)
	out := ts[:0]
	for _, t := range ts {
		if t.After(cut) {
			out = append(out, t)
		}
	}
	return out
}

// HandleHMIRegister — a display with no credentials asks for a pairing code.
// Unauthenticated by design; it only mints a short-lived code the owner must
// confirm. Rate-limited per client address.
func HandleHMIRegister(c *gin.Context) {
	var req struct {
		DeviceID string `json:"deviceId"`
		Name     string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	dev := sanitizeDeviceID(req.DeviceID)
	if dev == "" || strings.HasPrefix(dev, "legacy-") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device id"})
		return
	}

	claims.mu.Lock()
	defer claims.mu.Unlock()
	ip := c.ClientIP()
	claims.regs[ip] = recent(claims.regs[ip], time.Minute)
	if len(claims.regs[ip]) >= 20 {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many requests"})
		return
	}
	claims.regs[ip] = append(claims.regs[ip], time.Now())

	pruneClaimsLocked()
	if old := claims.byDevice[dev]; old != nil {
		delete(claims.byCode, old.code) // a new request replaces the old code
	}
	code := newPairCode()
	for claims.byCode[code] != nil {
		code = newPairCode()
	}
	cl := &hmiClaim{code: code, secret: randHex(16), deviceID: dev, name: tidyName(req.Name, 40), expires: time.Now().Add(claimTTL)}
	claims.byCode[code] = cl
	claims.byDevice[dev] = cl
	c.JSON(http.StatusOK, gin.H{"code": code, "secret": cl.secret, "expiresIn": int(claimTTL.Seconds())})
}

// HandleHMIRegisterStatus — the display polls until its code has been claimed,
// then receives its token exactly once.
func HandleHMIRegisterStatus(c *gin.Context) {
	dev := sanitizeDeviceID(c.Query("deviceId"))
	secret := c.Query("secret")
	claims.mu.Lock()
	defer claims.mu.Unlock()
	pruneClaimsLocked()
	cl := claims.byDevice[dev]
	if cl == nil {
		c.JSON(http.StatusOK, gin.H{"status": "expired"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(secret), []byte(cl.secret)) != 1 {
		c.JSON(http.StatusForbidden, gin.H{"error": "bad secret"})
		return
	}
	if !cl.done {
		c.JSON(http.StatusOK, gin.H{"status": "pending", "expiresIn": int(time.Until(cl.expires).Seconds())})
		return
	}
	delete(claims.byCode, cl.code)
	delete(claims.byDevice, cl.deviceID)
	c.JSON(http.StatusOK, gin.H{"status": "claimed", "token": cl.token, "gatewayId": cl.gatewayID,
		"gatewayName": cl.gwName, "hmiId": cl.hmiID, "name": cl.name})
}

type hmiView struct {
	Hmi
	GatewayName   string `json:"gatewayName"`
	GatewayOnline bool   `json:"gatewayOnline"`
	Online        bool   `json:"online"`
	NodeCount     int    `json:"nodeCount"`
}

func buildHmiViews(hs []Hmi) []hmiView {
	gwIDs := make([]string, 0, len(hs))
	for _, h := range hs {
		gwIDs = append(gwIDs, h.GatewayID)
	}
	gws := map[string]Gateway{}
	if len(gwIDs) > 0 {
		var rows []Gateway
		DB.Select("id", "name", "is_online").Where("id IN ?", gwIDs).Find(&rows)
		for _, g := range rows {
			gws[g.ID] = g
		}
	}
	counts := map[uint]int{}
	if len(hs) > 0 {
		ids := make([]uint, 0, len(hs))
		for _, h := range hs {
			ids = append(ids, h.ID)
		}
		var cr []struct {
			HmiID uint
			Count int
		}
		DB.Model(&Node{}).Select("hmi_id, COUNT(*) as count").Where("hmi_id IN ? AND connected_at IS NOT NULL", ids).Group("hmi_id").Scan(&cr)
		for _, r := range cr {
			counts[r.HmiID] = r.Count
		}
	}
	out := make([]hmiView, 0, len(hs))
	for _, h := range hs {
		g := gws[h.GatewayID]
		out = append(out, hmiView{Hmi: h, GatewayName: g.Name, GatewayOnline: g.IsOnline,
			Online: time.Since(h.LastSeen) < hmiOnlineWindow, NodeCount: counts[h.ID]})
	}
	return out
}

// HandleClaimHMI — the owner confirms a display's code (typed on the website or
// scanned from its QR in the app) and chooses the gateway it belongs to.
func HandleClaimHMI(c *gin.Context) {
	userID := c.GetUint("userID")
	var req struct {
		Code      string `json:"code"`
		GatewayID string `json:"gatewayId"`
		Name      string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Code == "" || req.GatewayID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code and gatewayId are required"})
		return
	}
	req.Code = strings.ReplaceAll(strings.TrimSpace(req.Code), " ", "")

	claims.mu.Lock()
	defer claims.mu.Unlock()
	claims.fails[userID] = recent(claims.fails[userID], claimFailWindow)
	if len(claims.fails[userID]) >= claimMaxFails {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many wrong codes. Wait a few minutes, or generate a new code on the display."})
		return
	}
	pruneClaimsLocked()
	cl := claims.byCode[req.Code]
	if cl == nil || cl.done {
		claims.fails[userID] = append(claims.fails[userID], time.Now())
		c.JSON(http.StatusNotFound, gin.H{"error": "That code is not valid or has expired. Check the display for the current code."})
		return
	}
	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", req.GatewayID, userID).First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}
	hmi, err := createHmi(gw, cl.deviceID, firstNonEmpty(req.Name, cl.name))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not register the display"})
		return
	}
	tok, err := GenerateHMIToken(gw.ID, hmi.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token error"})
		return
	}
	cl.done, cl.token, cl.gatewayID, cl.gwName, cl.hmiID, cl.name = true, tok, gw.ID, gw.Name, hmi.ID, hmi.Name
	cl.expires = time.Now().Add(claimDeliveryTTL)
	LogAudit(c, "hmi_paired", "gateway", &gw.ID, "Display \""+hmi.Name+"\" paired with gateway "+gw.Name)
	c.JSON(http.StatusOK, gin.H{"hmi": buildHmiViews([]Hmi{*hmi})[0]})
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// ── owner API ─────────────────────────────────────────────────────────────────

func ownedHmi(c *gin.Context) (Hmi, bool) {
	var h Hmi
	if err := DB.Omit("lock_bg").Where("slug = ? AND user_id = ?", c.Param("id"), c.GetUint("userID")).First(&h).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Display not found"})
		return h, false
	}
	return h, true
}

func ListHmis(c *gin.Context) {
	var hs []Hmi
	DB.Omit("lock_bg").Where("user_id = ?", c.GetUint("userID")).Order("name ASC, id ASC").Find(&hs)
	c.JSON(http.StatusOK, buildHmiViews(hs))
}

type hmiNodeView struct {
	DeviceID uint32 `json:"deviceId"`
	Name     string `json:"name"`
	IsOnline bool   `json:"isOnline"`
	HmiID    *uint  `json:"hmiId"`
	HmiName  string `json:"hmiName"`
}

// GetHmi returns a display and every provisioned node on its gateway together
// with which display (if any) each node is assigned to.
func GetHmi(c *gin.Context) {
	h, ok := ownedHmi(c)
	if !ok {
		return
	}
	var nodes []Node
	DB.Where("gateway_id = ? AND connected_at IS NOT NULL", h.GatewayID).Order("name ASC, device_id ASC").Find(&nodes)
	var hs []Hmi
	DB.Omit("lock_bg").Where("gateway_id = ?", h.GatewayID).Find(&hs)
	names := map[uint]string{}
	for _, x := range hs {
		names[x.ID] = x.Name
	}
	nv := make([]hmiNodeView, 0, len(nodes))
	for _, n := range nodes {
		v := hmiNodeView{DeviceID: n.DeviceID, Name: n.Name, IsOnline: isNodeOnline(n.LastSeen) && getTCPConn(n.GatewayID) != nil, HmiID: n.HmiID}
		if n.HmiID != nil {
			v.HmiName = names[*n.HmiID]
		}
		nv = append(nv, v)
	}
	c.JSON(http.StatusOK, gin.H{"hmi": buildHmiViews([]Hmi{h})[0], "nodes": nv, "siblings": len(hs) - 1})
}

func RenameHmi(c *gin.Context) {
	h, ok := ownedHmi(c)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || tidyName(req.Name, 40) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Name must be 1-40 characters"})
		return
	}
	h.Name = tidyName(req.Name, 40)
	DB.Model(&h).Update("name", h.Name)
	LogAudit(c, "hmi_renamed", "gateway", &h.GatewayID, "Display renamed to "+h.Name)
	c.JSON(http.StatusOK, buildHmiViews([]Hmi{h})[0])
}

// DeleteHmi unpairs a display: its token stops working immediately and its
// nodes become unassigned.
func DeleteHmi(c *gin.Context) {
	h, ok := ownedHmi(c)
	if !ok {
		return
	}
	releaseAndDeleteHmi(h)
	LogAudit(c, "hmi_unpaired", "gateway", &h.GatewayID, "Display \""+h.Name+"\" unpaired")
	c.JSON(http.StatusOK, gin.H{"status": "unpaired"})
}

func releaseAndDeleteHmi(h Hmi) {
	DB.Model(&Node{}).Where("hmi_id = ?", h.ID).Update("hmi_id", nil)
	DB.Delete(&Hmi{}, h.ID)
	forgetHmi(h.ID)
}

// SetHmiNodes makes `deviceIds` exactly the set of nodes shown on this display.
// Nodes taken from another display are moved (never shared); nodes dropped from
// this display become unassigned.
func SetHmiNodes(c *gin.Context) {
	h, ok := ownedHmi(c)
	if !ok {
		return
	}
	var req struct {
		DeviceIDs []uint32 `json:"deviceIds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "deviceIds required"})
		return
	}
	ids := req.DeviceIDs
	if len(ids) > 0 {
		var n int64
		DB.Model(&Node{}).Where("gateway_id = ? AND device_id IN ?", h.GatewayID, ids).Count(&n)
		if int(n) != len(uniqueU32(ids)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Some nodes do not belong to this display's gateway"})
			return
		}
	}
	if len(ids) == 0 {
		DB.Model(&Node{}).Where("hmi_id = ?", h.ID).Update("hmi_id", nil)
	} else {
		DB.Model(&Node{}).Where("hmi_id = ? AND device_id NOT IN ?", h.ID, ids).Update("hmi_id", nil)
		DB.Model(&Node{}).Where("gateway_id = ? AND device_id IN ?", h.GatewayID, ids).Update("hmi_id", h.ID)
	}
	LogAudit(c, "hmi_nodes_assigned", "gateway", &h.GatewayID, "Nodes assigned to display "+h.Name)
	GetHmi(c)
}

func uniqueU32(in []uint32) []uint32 {
	seen := map[uint32]bool{}
	out := []uint32{}
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// AssignNodeHmi assigns one node to a display (or to none).
func AssignNodeHmi(c *gin.Context) {
	userID := c.GetUint("userID")
	var req struct {
		HmiID *uint `json:"hmiId"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "hmiId required (or null)"})
		return
	}
	var node Node
	if err := DB.Joins("JOIN gateways ON gateways.id = nodes.gateway_id").
		Where("nodes.node_id = ? AND gateways.user_id = ?", c.Param("nodeId"), userID).First(&node).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}
	if req.HmiID != nil {
		var h Hmi
		if err := DB.Where("id = ? AND user_id = ? AND gateway_id = ?", *req.HmiID, userID, node.GatewayID).First(&h).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "That display is not paired to this node's gateway"})
			return
		}
	}
	DB.Model(&Node{}).Where("id = ?", node.ID).Update("hmi_id", req.HmiID)
	node.HmiID = req.HmiID
	c.JSON(http.StatusOK, gin.H{"nodeId": node.NodeID, "deviceId": node.DeviceID, "hmiId": req.HmiID})
}

// ── display-side API (HMI token) ──────────────────────────────────────────────

// HMIGetGateway returns the gateway (and display) an HMI token is scoped to, so
// the screen can follow renames.
func HMIGetGateway(c *gin.Context) {
	var gw Gateway
	if err := DB.Select("id", "name", "is_online").First(&gw, "id = ?", c.GetString("hmiGatewayID")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}
	var h Hmi
	DB.Select("id", "name", "theme", "accent", "brightness", "status_led", "background", "led_mode", "led_color", "led_brightness", "led_speed", "sleep_after", "led_sleep_off", "lock_bg_version").First(&h, c.GetUint("hmiID"))
	c.JSON(http.StatusOK, gin.H{"id": gw.ID, "name": gw.Name, "isOnline": gw.IsOnline, "hmiId": h.ID, "hmiName": h.Name,
		"appearance": appearanceOf(h)})
}

func appearanceOf(h Hmi) gin.H {
	theme, accent, br := h.Theme, strings.ToUpper(h.Accent), h.Brightness
	if theme != "light" {
		theme = "dark"
	}
	if !hexColor.MatchString(accent) {
		accent = "00A8E8"
	}
	if br < 10 || br > 100 {
		br = 100
	}
	bg, ledColor := strings.ToUpper(h.Background), strings.ToUpper(h.LedColor)
	if !hexColor.MatchString(bg) {
		bg = ""
	}
	if !hexColor.MatchString(ledColor) {
		ledColor = ""
	}
	mode := h.LedMode
	if !ledModes[mode] {
		mode = "solid"
	}
	lb, ls := h.LedBrightness, h.LedSpeed
	if lb < 1 || lb > 100 {
		lb = 40
	}
	if ls < 1 || ls > 100 {
		ls = 50
	}
	return gin.H{"theme": theme, "accent": accent, "brightness": br, "statusLed": h.StatusLed,
		"background": bg, "ledMode": mode, "ledColor": ledColor, "ledBrightness": lb, "ledSpeed": ls,
		"sleepAfter": h.SleepAfter, "ledSleepOff": h.LedSleepOff, "lockBg": h.LockBgVersion}
}

// Patterns the onboard RGB LED can play (the display firmware maps these to effects).
var ledModes = map[string]bool{"solid": true, "blink": true, "breathe": true, "fade": true, "rainbow": true,
	"random": true, "strobe": true, "twinkle": true, "flicker": true, "sparkle": true}

var hexColor = regexp.MustCompile(`^[0-9A-F]{6}$`)

// SetHmiAppearance changes how the display looks (theme, accent, brightness, LED).
// The display picks it up on its next refresh (within ~30 s).
func SetHmiAppearance(c *gin.Context) {
	h, ok := ownedHmi(c)
	if !ok {
		return
	}
	var req struct {
		Theme      *string `json:"theme"`
		Accent     *string `json:"accent"`
		Brightness *int    `json:"brightness"`
		StatusLed  *bool   `json:"statusLed"`

		Background    *string `json:"background"`
		LedMode       *string `json:"ledMode"`
		LedColor      *string `json:"ledColor"`
		LedBrightness *int    `json:"ledBrightness"`
		LedSpeed      *int    `json:"ledSpeed"`
		SleepAfter    *int    `json:"sleepAfter"`
		LedSleepOff   *bool   `json:"ledSleepOff"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	up := map[string]interface{}{}
	if req.Theme != nil {
		if *req.Theme != "dark" && *req.Theme != "light" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Theme must be dark or light"})
			return
		}
		up["theme"] = *req.Theme
	}
	if req.Accent != nil {
		a := strings.ToUpper(strings.TrimPrefix(*req.Accent, "#"))
		if !hexColor.MatchString(a) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Accent must be a colour like 00A8E8"})
			return
		}
		up["accent"] = a
	}
	if req.Brightness != nil {
		if *req.Brightness < 10 || *req.Brightness > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Brightness must be 10-100"})
			return
		}
		up["brightness"] = *req.Brightness
	}
	if req.StatusLed != nil {
		up["status_led"] = *req.StatusLed
	}
	for field, col := range map[string]*string{"background": req.Background, "ledColor": req.LedColor} {
		if col == nil {
			continue
		}
		v := strings.ToUpper(strings.TrimPrefix(*col, "#"))
		if v != "" && !hexColor.MatchString(v) {
			c.JSON(http.StatusBadRequest, gin.H{"error": field + " must be a colour like 00A8E8 (or empty for the default)"})
			return
		}
		if field == "background" {
			up["background"] = v
		} else {
			up["led_color"] = v
		}
	}
	if req.LedMode != nil {
		if !ledModes[*req.LedMode] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Unknown LED pattern"})
			return
		}
		up["led_mode"] = *req.LedMode
	}
	if req.LedBrightness != nil {
		if *req.LedBrightness < 1 || *req.LedBrightness > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "LED brightness must be 1-100"})
			return
		}
		up["led_brightness"] = *req.LedBrightness
	}
	if req.LedSpeed != nil {
		if *req.LedSpeed < 1 || *req.LedSpeed > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "LED speed must be 1-100"})
			return
		}
		up["led_speed"] = *req.LedSpeed
	}
	if req.SleepAfter != nil {
		if !validSleepAfter(*req.SleepAfter) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Screen-off time must be 0 (never) or 10 to 3600 seconds"})
			return
		}
		up["sleep_after"] = *req.SleepAfter
	}
	if req.LedSleepOff != nil {
		up["led_sleep_off"] = *req.LedSleepOff
	}
	if len(up) > 0 {
		DB.Model(&h).Updates(up)
		DB.First(&h, h.ID)
		LogAudit(c, "hmi_appearance", "gateway", &h.GatewayID, "Appearance of \""+h.Name+"\" changed")
	}
	c.JSON(http.StatusOK, buildHmiViews([]Hmi{h})[0])
}

// HMIUnpair lets a display remove itself (e.g. after a factory reset).
func HMIUnpair(c *gin.Context) {
	var h Hmi
	if err := DB.First(&h, c.GetUint("hmiID")).Error; err == nil {
		releaseAndDeleteHmi(h)
	}
	c.JSON(http.StatusOK, gin.H{"status": "unpaired"})
}

func validSleepAfter(v int) bool { return v == 0 || (v >= 10 && v <= 3600) }

// HMISetDisplay lets the display itself change its power settings from its own
// Settings screen. Same values as the app/website, stored on the hub so all agree.
func HMISetDisplay(c *gin.Context) {
	var req struct {
		Brightness    *int  `json:"brightness"`
		LedBrightness *int  `json:"ledBrightness"`
		SleepAfter    *int  `json:"sleepAfter"`
		LedSleepOff   *bool `json:"ledSleepOff"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	up := map[string]interface{}{}
	if req.Brightness != nil {
		if *req.Brightness < 10 || *req.Brightness > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Brightness must be 10-100"})
			return
		}
		up["brightness"] = *req.Brightness
	}
	if req.LedBrightness != nil {
		if *req.LedBrightness < 1 || *req.LedBrightness > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "LED brightness must be 1-100"})
			return
		}
		up["led_brightness"] = *req.LedBrightness
	}
	if req.SleepAfter != nil {
		if !validSleepAfter(*req.SleepAfter) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Screen-off time must be 0 (never) or 10 to 3600 seconds"})
			return
		}
		up["sleep_after"] = *req.SleepAfter
	}
	if req.LedSleepOff != nil {
		up["led_sleep_off"] = *req.LedSleepOff
	}
	if len(up) > 0 {
		DB.Model(&Hmi{}).Where("id = ?", c.GetUint("hmiID")).Updates(up)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
