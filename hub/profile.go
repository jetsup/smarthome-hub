package hub

import (
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func currentUser(c *gin.Context) (User, bool) {
	var u User
	if err := DB.First(&u, c.GetUint("userID")).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Account not found"})
		return u, false
	}
	return u, true
}

func userView(u User) gin.H {
	return gin.H{
		"id": u.ID, "firstName": u.FirstName, "lastName": u.LastName, "email": u.Email,
		"createdAt": u.CreatedAt, "mfaEnabled": u.MfaEnabled,
	}
}

// GetProfile returns the signed-in user's details and security state.
func GetProfile(c *gin.Context) {
	u, ok := currentUser(c)
	if !ok {
		return
	}
	var devices []MfaDevice
	DB.Where("user_id = ?", u.ID).Order("created_at ASC").Find(&devices)
	out := userView(u)
	out["phones"] = devices
	out["recoveryCodesLeft"] = len(recoveryHashes(u))
	out["methods"] = gin.H{"phone": u.MfaPhone, "totp": u.MfaTotp, "email": u.MfaEmail}
	out["emailConfigured"] = mailConfigured()
	out["emailCodeMinutes"] = int(mfaEmailTTL().Minutes())
	c.JSON(http.StatusOK, out)
}

func validName(s string) bool {
	n := utf8.RuneCountInString(s)
	return n >= 1 && n <= 100
}

// UpdateProfile changes name and/or email. Changing the email needs the
// current password.
func UpdateProfile(c *gin.Context) {
	u, ok := currentUser(c)
	if !ok {
		return
	}
	var req struct {
		FirstName       string `json:"firstName"`
		LastName        string `json:"lastName"`
		Email           string `json:"email"`
		CurrentPassword string `json:"currentPassword"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	first := strings.Join(strings.Fields(req.FirstName), " ")
	last := strings.Join(strings.Fields(req.LastName), " ")
	if !validName(first) || !validName(last) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "First and last name are required (max 100 characters)"})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		email = u.Email
	}
	if _, err := mail.ParseAddress(email); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Enter a valid email address"})
		return
	}
	if email != u.Email {
		if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.CurrentPassword)) != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "Enter your current password to change your email"})
			return
		}
		var n int64
		DB.Model(&User{}).Where("email = ? AND id <> ?", email, u.ID).Count(&n)
		if n > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "That email is already in use"})
			return
		}
	}
	old := u.Email
	DB.Model(&u).Updates(map[string]interface{}{"first_name": first, "last_name": last, "email": email, "updated_at": time.Now()})
	u.FirstName, u.LastName, u.Email = first, last, email
	uid := fmt.Sprintf("%d", u.ID)
	LogAudit(c, "profile_updated", "user", &uid, "Profile updated"+map[bool]string{true: " (email " + old + " → " + email + ")", false: ""}[old != email])
	resp := gin.H{"user": userView(u)}
	if old != email {
		// The token carries the email; hand back a fresh one for this session.
		cur, _ := c.Get("sid")
		sid, _ := cur.(uint)
		if t, err := GenerateSessionToken(u.ID, u.Email, sid); err == nil {
			resp["token"] = t
		}
	}
	c.JSON(http.StatusOK, resp)
}

// ChangePassword verifies the current password, sets the new one and ends every
// other session (they carry the old token version). The caller gets a new token.
func ChangePassword(c *gin.Context) {
	u, ok := currentUser(c)
	if !ok {
		return
	}
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.CurrentPassword)) != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Your current password is incorrect"})
		return
	}
	if len(req.NewPassword) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "The new password must be at least 8 characters"})
		return
	}
	if req.NewPassword == req.CurrentPassword {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Choose a password you have not used just now"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process password"})
		return
	}
	DB.Model(&u).Updates(map[string]interface{}{"password_hash": string(hash), "token_version": u.TokenVersion + 1, "updated_at": time.Now()})
	forgetUserVersion(u.ID)
	// Every other device is signed out; this one gets a fresh session.
	var old []UserSession
	DB.Where("user_id = ?", u.ID).Find(&old)
	for _, s := range old {
		forgetSession(s.ID)
	}
	DB.Where("user_id = ?", u.ID).Delete(&UserSession{})
	sess := newSession(c, u.ID, "", "")
	uid := fmt.Sprintf("%d", u.ID)
	LogAudit(c, "password_changed", "user", &uid, "Password changed; other sessions ended")
	t, _ := GenerateSessionToken(u.ID, u.Email, sess.ID)
	c.JSON(http.StatusOK, gin.H{"status": "changed", "token": t})
}
