package hub

import (
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const defaultDevSecret = "smarthome-dev-secret-change-in-production"

var (
	jwtSecret     []byte
	jwtSecretOnce sync.Once
)

// secret lazily reads JWT_SECRET so that values loaded from .env (which happens
// in InitDatabase, after package init) are honoured.
func secret() []byte {
	jwtSecretOnce.Do(func() {
		s := os.Getenv("JWT_SECRET")
		if s == "" {
			log.Println("WARNING: JWT_SECRET not set — using insecure development secret")
			s = defaultDevSecret
		} else if s == defaultDevSecret || s == "change-this-to-a-long-random-secret" {
			log.Println("WARNING: JWT_SECRET is a well-known placeholder — set a long random value")
		}
		jwtSecret = []byte(s)
	})
	return jwtSecret
}

type Claims struct {
	UserID uint   `json:"userId"`
	Email  string `json:"email"`
	// Role is empty for normal users and "hmi" for paired HMI displays.
	Role      string `json:"role,omitempty"`
	GatewayID string `json:"gatewayId,omitempty"`
	HmiID     uint   `json:"hmiId,omitempty"`
	TV        int    `json:"tv,omitempty"`  // user token version at issue time
	SID       uint   `json:"sid,omitempty"` // the session this token belongs to
	// Carried through the MFA step so the final session is linked to the phone.
	DeviceKey   string `json:"dk,omitempty"`
	DeviceLabel string `json:"dl,omitempty"`
	jwt.RegisteredClaims
}

// GenerateHMIToken issues a long-lived token scoped to a single gateway,
// handed to an HMI after it has completed pairing.
func GenerateHMIToken(gatewayID string, hmiID uint) (string, error) {
	claims := Claims{
		Role:      "hmi",
		GatewayID: gatewayID,
		HmiID:     hmiID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(365 * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret())
}

// HMIAuthMiddleware requires an HMI token and records the gateway it is scoped to.
func HMIAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, ok := parseBearer(c)
		if !ok || claims.Role != "hmi" || claims.GatewayID == "" || claims.HmiID == 0 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "HMI not paired or token invalid"})
			return
		}
		// The display must still exist (it can be unpaired from the app / web)
		// and still be on the gateway the token was issued for.
		if !touchHmi(claims.HmiID, claims.GatewayID) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "display was unpaired"})
			return
		}
		c.Set("hmiID", claims.HmiID)
		c.Set("hmiGatewayID", claims.GatewayID)
		c.Next()
	}
}

func parseBearer(c *gin.Context) (*Claims, bool) {
	auth := c.GetHeader("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return nil, false
	}
	return parseTokenString(strings.TrimPrefix(auth, "Bearer "))
}

func parseTokenString(raw string) (*Claims, bool) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(raw, claims,
		func(t *jwt.Token) (interface{}, error) { return secret(), nil },
		jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !token.Valid {
		return nil, false
	}
	return claims, true
}

func GenerateToken(userID uint, email string) (string, error) {
	return GenerateSessionToken(userID, email, 0)
}

// GenerateSessionToken issues a user token bound to session `sid` (0 = none).
func GenerateSessionToken(userID uint, email string, sid uint) (string, error) {
	var tv int
	DB.Model(&User{}).Select("token_version").Where("id = ?", userID).Scan(&tv)
	claims := Claims{
		UserID: userID,
		Email:  email,
		TV:     tv,
		SID:    sid,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(30 * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret())
}

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, ok := parseBearer(c)
		if !ok || claims.Role != "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			return
		}
		if !sessionStillValid(claims.UserID, claims.TV) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Session ended — please sign in again"})
			return
		}
		if claims.SID != 0 && !sessionAlive(claims.SID, claims.UserID) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "This device was signed out"})
			return
		}
		c.Set("userID", claims.UserID)
		c.Set("email", claims.Email)
		c.Set("sid", claims.SID)
		c.Next()
	}
}

// ── session validity (token version), cached briefly ─────────────────────────

type userVersion struct {
	tv      int
	fetched time.Time
}

var userVersions = struct {
	mu sync.Mutex
	m  map[uint]userVersion
}{m: map[uint]userVersion{}}

func forgetUserVersion(id uint) {
	userVersions.mu.Lock()
	delete(userVersions.m, id)
	userVersions.mu.Unlock()
}

// sessionStillValid is false when the user no longer exists or changed their
// password after this token was issued.
func sessionStillValid(userID uint, tv int) bool {
	userVersions.mu.Lock()
	e, ok := userVersions.m[userID]
	userVersions.mu.Unlock()
	if !ok || time.Since(e.fetched) > 10*time.Second {
		var u User
		if err := DB.Select("id", "token_version").First(&u, userID).Error; err != nil {
			return false
		}
		e = userVersion{tv: u.TokenVersion, fetched: time.Now()}
		userVersions.mu.Lock()
		userVersions.m[userID] = e
		userVersions.mu.Unlock()
	}
	return tv == e.tv
}

// GatewayAPIKeyAuth validates the X-API-Key header against stored gateway keys.
// Sets the gateway ID in context for downstream handlers.
func GatewayAPIKeyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey := c.GetHeader("X-API-Key")
		if apiKey == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Missing API key"})
			return
		}

		var gw Gateway
		if err := DB.Where("api_key = ?", apiKey).First(&gw).Error; err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
			return
		}

		c.Set("gatewayID", gw.ID)
		c.Set("userID", gw.UserID)
		c.Next()
	}
}
