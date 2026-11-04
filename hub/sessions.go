package hub

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ── what to call a device ─────────────────────────────────────────────────────

// uaLabel turns a User-Agent into "Chrome on Linux" and says whether it is the app.
func uaLabel(ua string) (label, kind string) {
	if strings.Contains(ua, "Dart/") || strings.Contains(ua, "okhttp") {
		return "SmartHomes app", "app"
	}
	browser := "Browser"
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/") || strings.Contains(ua, "Opera"):
		browser = "Opera"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Brave"):
		browser = "Brave"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	osName := ""
	switch {
	case strings.Contains(ua, "Android"):
		osName = "Android"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		osName = "iOS"
	case strings.Contains(ua, "Windows"):
		osName = "Windows"
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		osName = "macOS"
	case strings.Contains(ua, "CrOS"):
		osName = "ChromeOS"
	case strings.Contains(ua, "Linux"):
		osName = "Linux"
	}
	if osName != "" {
		return browser + " on " + osName, "browser"
	}
	return browser, "browser"
}

// ── creating & checking sessions ──────────────────────────────────────────────

// newSession records a sign-in. deviceLabel / deviceKey come from the mobile app.
func newSession(c *gin.Context, userID uint, deviceKey, deviceLabel string) UserSession {
	ua := c.Request.UserAgent()
	label, kind := uaLabel(ua)
	if deviceLabel = tidyName(deviceLabel, 150); deviceLabel != "" {
		label, kind = deviceLabel, "app"
	}
	now := time.Now()
	if len(ua) > 250 {
		ua = ua[:250]
	}
	s := UserSession{UserID: userID, DeviceKey: deviceKey, Label: label, Kind: kind, IP: c.ClientIP(), UserAgent: ua, CreatedAt: now, LastSeen: now}
	DB.Create(&s)
	return s
}

var sessionCache = struct {
	mu sync.Mutex
	m  map[uint]sessionEntry
}{m: map[uint]sessionEntry{}}

type sessionEntry struct {
	userID  uint
	checked time.Time
	wrote   time.Time
}

func forgetSession(id uint) {
	sessionCache.mu.Lock()
	delete(sessionCache.m, id)
	sessionCache.mu.Unlock()
}

// sessionAlive is false once the session was revoked (cached for a few seconds).
func sessionAlive(sid, userID uint) bool {
	now := time.Now()
	sessionCache.mu.Lock()
	e, ok := sessionCache.m[sid]
	if ok && e.userID == userID && now.Sub(e.checked) < 5*time.Second {
		write := now.Sub(e.wrote) > time.Minute
		if write {
			e.wrote = now
			sessionCache.m[sid] = e
		}
		sessionCache.mu.Unlock()
		if write {
			DB.Model(&UserSession{}).Where("id = ?", sid).Update("last_seen", now)
		}
		return true
	}
	sessionCache.mu.Unlock()

	var s UserSession
	if err := DB.Select("id", "user_id").First(&s, sid).Error; err != nil || s.UserID != userID {
		forgetSession(sid)
		return false
	}
	DB.Model(&UserSession{}).Where("id = ?", sid).Update("last_seen", now)
	sessionCache.mu.Lock()
	sessionCache.m[sid] = sessionEntry{userID: userID, checked: now, wrote: now}
	sessionCache.mu.Unlock()
	return true
}

// ── API: signed-in devices ────────────────────────────────────────────────────

type sessionView struct {
	UserSession
	Current bool `json:"current"`
}

func ListSessions(c *gin.Context) {
	var ss []UserSession
	DB.Where("user_id = ?", c.GetUint("userID")).Order("last_seen DESC").Find(&ss)
	cur, _ := c.Get("sid")
	curID, _ := cur.(uint)
	out := make([]sessionView, 0, len(ss))
	for _, s := range ss {
		out = append(out, sessionView{UserSession: s, Current: curID != 0 && s.ID == curID})
	}
	c.JSON(http.StatusOK, out)
}

func RevokeSession(c *gin.Context) {
	var s UserSession
	if DB.Where("slug = ? AND user_id = ?", c.Param("id"), c.GetUint("userID")).First(&s).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Device not found"})
		return
	}
	DB.Delete(&s)
	forgetSession(s.ID)
	LogAudit(c, "session_revoked", "user", nil, "Signed out device: "+s.Label)
	c.JSON(http.StatusOK, gin.H{"status": "signed out"})
}

// RevokeOtherSessions signs out everything except the caller.
func RevokeOtherSessions(c *gin.Context) {
	cur, _ := c.Get("sid")
	curID, _ := cur.(uint)
	var ss []UserSession
	q := DB.Where("user_id = ?", c.GetUint("userID"))
	if curID != 0 {
		q = q.Where("id <> ?", curID)
	}
	q.Find(&ss)
	for _, s := range ss {
		forgetSession(s.ID)
	}
	if len(ss) > 0 {
		ids := make([]uint, 0, len(ss))
		for _, s := range ss {
			ids = append(ids, s.ID)
		}
		DB.Where("id IN ?", ids).Delete(&UserSession{})
	}
	LogAudit(c, "session_revoked_others", "user", nil, "Signed out all other devices ("+strconv.Itoa(len(ss))+")")
	c.JSON(http.StatusOK, gin.H{"signedOut": len(ss)})
}
