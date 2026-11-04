package hub

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

type RegisterRequest struct {
	FirstName       string `json:"firstName" binding:"required"`
	LastName        string `json:"lastName" binding:"required"`
	Email           string `json:"email" binding:"required,email"`
	Password        string `json:"password" binding:"required,min=6"`
	ConfirmPassword string `json:"confirmPassword" binding:"required,eqfield=Password"`
}

type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
	// Sent by the mobile app so this sign-in is listed under the right phone.
	DeviceKey   string `json:"deviceKey"`
	DeviceLabel string `json:"deviceLabel"`
}

func Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	req.FirstName = strings.TrimSpace(req.FirstName)
	req.LastName = strings.TrimSpace(req.LastName)
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	var exists int64
	DB.Model(&User{}).Where("email = ?", req.Email).Count(&exists)
	if exists > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Email already registered"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process password"})
		return
	}

	user := User{
		FirstName:    req.FirstName,
		LastName:     req.LastName,
		Email:        req.Email,
		PasswordHash: string(hash),
	}

	if err := DB.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create user"})
		return
	}

	sess := newSession(c, user.ID, "", "")
	token, err := GenerateSessionToken(user.ID, user.Email, sess.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	uid := fmt.Sprintf("%d", user.ID)
	LogAuditAs(c, user.ID, user.Email, "user_registered", "Account created", "ok")
	_ = uid

	c.JSON(http.StatusCreated, gin.H{
		"token": token,
		"user": gin.H{
			"id":        user.ID,
			"firstName": user.FirstName,
			"lastName":  user.LastName,
			"email":     user.Email,
		},
	})
}

func Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	var user User
	if err := DB.Where("email = ?", req.Email).First(&user).Error; err != nil {
		LogAuditAs(c, 0, req.Email, "login_failed", "Sign-in attempt for an unknown account", "failed")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid email or password"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		LogAuditAs(c, user.ID, user.Email, "login_failed", "Wrong password", "failed")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid email or password"})
		return
	}

	// Optional two-step verification: the password alone does not sign you in.
	if user.MfaEnabled {
		var phones int64
		DB.Model(&MfaDevice{}).Where("user_id = ?", user.ID).Count(&phones)
		mt, err := generateMFAToken(user, sanitizeDeviceKey(req.DeviceKey), req.DeviceLabel)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to start verification"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"mfaRequired": true,
			"mfaToken":    mt,
			"methods": gin.H{
				"phone":    user.MfaPhone && phones > 0,
				"totp":     user.MfaTotp,
				"email":    user.MfaEmail && mailConfigured(),
				"recovery": len(recoveryHashes(user)) > 0,
			},
			"emailTo": maskEmail(user.Email),
		})
		return
	}
	finishLogin(c, user, sanitizeDeviceKey(req.DeviceKey), req.DeviceLabel)
}

// finishLogin issues the real session token (after the password, and after MFA if enabled).
func finishLogin(c *gin.Context, user User, deviceKey, deviceLabel string) {
	payload, ok := loginPayload(c, user, deviceKey, deviceLabel)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, payload)
}

// loginPayload creates the session and returns {token, user}. It writes the
// error response itself and returns ok=false on failure.
func loginPayload(c *gin.Context, user User, deviceKey, deviceLabel string) (gin.H, bool) {
	sess := newSession(c, user.ID, deviceKey, deviceLabel)
	token, err := GenerateSessionToken(user.ID, user.Email, sess.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return nil, false
	}
	uid := fmt.Sprintf("%d", user.ID)
	LogAuditAs(c, user.ID, user.Email, "user_login", "Signed in on "+sess.Label, "ok")
	_ = uid
	return gin.H{
		"token": token,
		"user": gin.H{
			"id":        user.ID,
			"firstName": user.FirstName,
			"lastName":  user.LastName,
			"email":     user.Email,
		},
	}, true
}

func Me(c *gin.Context) {
	uid, _ := c.Get("userID")
	email, _ := c.Get("email")
	c.JSON(http.StatusOK, gin.H{
		"user": gin.H{
			"id":    uid,
			"email": email,
		},
	})
}

func RefreshToken(c *gin.Context) {
	claims, ok := parseBearer(c)
	if !ok || claims.Role != "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
		return
	}

	newToken, err := GenerateSessionToken(claims.UserID, claims.Email, claims.SID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token": newToken,
		"user": gin.H{
			"id":    claims.UserID,
			"email": claims.Email,
		},
	})
}
