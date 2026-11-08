package hub

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// categoryOf groups audit actions so the log can be filtered.
func categoryOf(action string) string {
	switch {
	case strings.HasPrefix(action, "mfa_"), strings.HasPrefix(action, "session_"), strings.HasPrefix(action, "login_"),
		action == "password_changed", action == "api_key_retrieved", action == "device_registered", action == "device_removed":
		return "security"
	case strings.HasPrefix(action, "user_"), strings.HasPrefix(action, "profile_"):
		return "account"
	case strings.HasPrefix(action, "gateway_"), strings.HasPrefix(action, "scan_"), strings.HasPrefix(action, "pending_"):
		return "gateway"
	case strings.HasPrefix(action, "node_"):
		return "node"
	case strings.HasPrefix(action, "hmi_"):
		return "display"
	case strings.HasPrefix(action, "wifi_"):
		return "wifi"
	}
	return "system"
}

// LogAudit records who did what, from which device, for the audit trail.
func LogAudit(c *gin.Context, action, entityType string, entityID *string, details string) {
	writeAudit(c, action, entityType, entityID, details, "ok")
}

// LogAuditFailed records an attempt that was refused (bad password, wrong code...).
func LogAuditFailed(c *gin.Context, action, entityType string, entityID *string, details string) {
	writeAudit(c, action, entityType, entityID, details, "failed")
}

func writeAudit(c *gin.Context, action, entityType string, entityID *string, details, outcome string) {
	a := AuditLog{Action: action, EntityType: entityType, EntityID: entityID, Details: details,
		Category: categoryOf(action), Outcome: outcome, ActorKind: "system", Actor: "System"}
	if c != nil {
		a.IPAddress = c.ClientIP()
		if hid := c.GetUint("hmiID"); hid != 0 {
			var h Hmi
			if DB.Select("id", "name", "user_id").First(&h, hid).Error == nil {
				a.UserID = &h.UserID
				a.Actor, a.ActorKind, a.Device = h.Name, "display", "Display “"+h.Name+"”"
			}
		} else if uid := c.GetUint("userID"); uid != 0 {
			a.UserID, a.ActorKind = &uid, "user"
			a.Actor = c.GetString("email")
			if sid := c.GetUint("sid"); sid != 0 {
				var s UserSession
				if DB.Select("label").First(&s, sid).Error == nil {
					a.Device = s.Label
				}
			}
			if a.Device == "" {
				a.Device, _ = uaLabel(c.Request.UserAgent())
			}
		}
	}
	DB.Create(&a)
}

// LogAuditAs records an event for a known user when the request isn't authenticated yet (sign-in).
func LogAuditAs(c *gin.Context, userID uint, email, action, details, outcome string) {
	dev, _ := uaLabel(c.Request.UserAgent())
	a := AuditLog{Action: action, EntityType: "user", Details: details, Category: categoryOf(action),
		Outcome: outcome, ActorKind: "user", Actor: email, IPAddress: c.ClientIP(), Device: dev}
	if dl := c.GetString("deviceLabelHint"); dl != "" {
		a.Device = dl
	}
	if userID != 0 {
		a.UserID = &userID
	}
	DB.Create(&a)
}

// ListAudit returns the activity of the signed-in user, their devices and displays,
// plus events on gateways they own. Filters: category, outcome, q, before (id), limit.
func ListAudit(c *gin.Context) {
	rows, total := queryAudit(c)
	if c.Query("format") == "csv" {
		c.Header("Content-Type", "text/csv")
		c.Header("Content-Disposition", `attachment; filename="smarthome-audit-`+time.Now().Format("2006-01-02")+`.csv"`)
		w := csv.NewWriter(c.Writer)
		w.Write([]string{"time", "actor", "kind", "device", "ip", "category", "action", "outcome", "details"})
		for _, r := range rows {
			w.Write([]string{r.CreatedAt.Format(time.RFC3339), csvSafe(r.Actor), r.ActorKind, csvSafe(r.Device), r.IPAddress, r.Category, r.Action, r.Outcome, csvSafe(r.Details)})
		}
		w.Flush()
		return
	}
	next := uint(0)
	if len(rows) > 0 {
		next = rows[len(rows)-1].ID
	}
	limit := auditLimit(c)
	page := auditPage(c)
	pages := (int(total) + limit - 1) / limit
	if pages < 1 {
		pages = 1
	}
	c.JSON(http.StatusOK, gin.H{"items": rows, "next": next, "total": total, "page": page, "pageSize": limit, "pages": pages})
}

// csvSafe stops spreadsheet formula injection in exported text.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func auditLimit(c *gin.Context) int {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit < 1 || limit > 500 {
		limit = 50
	}
	return limit
}

func auditPage(c *gin.Context) int {
	p, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if p < 1 {
		p = 1
	}
	return p
}

// queryAudit returns one page of the log (newest first) and the total number of matches.
// Pages: ?page=1&limit=50. (?before=<id> still works for cursor-style paging.)
func queryAudit(c *gin.Context) ([]AuditLog, int64) {
	uid := c.GetUint("userID")
	limit := auditLimit(c)
	if c.Query("format") == "csv" {
		limit = 5000
	}
	var gwIDs []string
	DB.Model(&Gateway{}).Where("user_id = ?", uid).Pluck("id", &gwIDs)
	q := DB.Model(&AuditLog{})
	if len(gwIDs) > 0 {
		q = q.Where("user_id = ? OR (entity_type = 'gateway' AND entity_id IN ?)", uid, gwIDs)
	} else {
		q = q.Where("user_id = ?", uid)
	}
	if v := c.Query("category"); v != "" {
		q = q.Where("category = ?", v)
	}
	if v := c.Query("outcome"); v == "ok" || v == "failed" {
		q = q.Where("outcome = ?", v)
	}
	if v := strings.TrimSpace(c.Query("q")); v != "" {
		like := "%" + strings.NewReplacer("%", "", "_", "").Replace(v) + "%"
		q = q.Where("details LIKE ? OR device LIKE ? OR actor LIKE ? OR action LIKE ? OR ip_address LIKE ?", like, like, like, like, like)
	}
	if b, _ := strconv.Atoi(c.Query("before")); b > 0 {
		q = q.Where("id < ?", b)
	}
	var total int64
	q.Count(&total)
	if c.Query("before") == "" && c.Query("format") != "csv" {
		q = q.Offset((auditPage(c) - 1) * limit)
	}
	var rows []AuditLog
	q.Order("id DESC").Limit(limit).Find(&rows)
	return rows, total
}

// auditUser logs an event for a user from code that has no request context.
func auditUser(u User, action, details string) {
	id := u.ID
	DB.Create(&AuditLog{UserID: &id, Action: action, EntityType: "user", Details: details, Category: categoryOf(action),
		Outcome: "ok", ActorKind: "user", Actor: u.Email})
}
