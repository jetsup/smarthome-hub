package hub

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	pairCodeTTL     = 5 * time.Minute
	pairMaxAttempts = 5
)

type pairingEntry struct {
	code      string
	gatewayID string // hex DB id
	expires   time.Time
	attempts  int
}

var (
	// keyed by hex DB id AND by the id the HMI used in its request (decimal announce id)
	pairingCodes   = map[string]*pairingEntry{}
	pairingCodesMu sync.Mutex
)

func newPairCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
	}
	return fmt.Sprintf("%06d", n.Int64())
}

func prunePairingLocked() {
	now := time.Now()
	for k, e := range pairingCodes {
		if now.After(e.expires) {
			delete(pairingCodes, k)
		}
	}
}

// resolveGatewayID maps an HMI-supplied identifier (decimal announce id, hex id or
// name prefix) to the hex database id. Returns "" when no gateway matches.
func resolveGatewayID(id, name string) string {
	if n, err := strconv.ParseUint(id, 10, 32); err == nil {
		if mapped := getGatewayIDByAnnounceID(uint32(n)); mapped != "" {
			return mapped
		}
	}
	var gw Gateway
	if err := DB.Where("id = ?", id).First(&gw).Error; err == nil {
		return gw.ID
	}
	if name != "" {
		if err := DB.Where("name = ?", name).First(&gw).Error; err == nil {
			return gw.ID
		}
	}
	return ""
}

// HandlePairRequest is called by an HMI that wants to pair with a gateway. It is
// unauthenticated by design (the HMI has no credentials yet) but only creates a
// short-lived code that is shown to the gateway owner in the web UI/app.
func HandlePairRequest(c *gin.Context) {
	var req struct {
		GatewayId string `json:"gatewayId"`
		Name      string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.GatewayId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid request"})
		return
	}

	hexID := resolveGatewayID(req.GatewayId, req.Name)
	if hexID == "" {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "unknown gateway"})
		return
	}

	entry := &pairingEntry{code: newPairCode(), gatewayID: hexID, expires: time.Now().Add(pairCodeTTL)}
	pairingCodesMu.Lock()
	prunePairingLocked()
	pairingCodes[hexID] = entry
	pairingCodes[req.GatewayId] = entry
	pairingCodesMu.Unlock()

	if err := SendRawTextToGateway(hexID, "PAIR:CODE:"+entry.code); err != nil {
		log.Printf("Pairing: gateway %s offline for TCP delivery (%v)", hexID, err)
		c.JSON(http.StatusOK, gin.H{"success": true, "note": "gateway offline, code stored"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// HandleGetPairCode returns the active pairing code to the authenticated owner of the gateway.
func HandleGetPairCode(c *gin.Context) {
	userID := c.GetUint("userID")
	gatewayID := c.Param("gatewayId")

	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", gatewayID, userID).First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}

	pairingCodesMu.Lock()
	prunePairingLocked()
	e := pairingCodes[gw.ID]
	pairingCodesMu.Unlock()

	if e == nil {
		c.JSON(http.StatusOK, gin.H{"code": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": e.code, "expiresIn": int(time.Until(e.expires).Seconds())})
}

// HandlePairVerify checks the code typed on the HMI. On success it returns an
// HMI token scoped to the gateway; the HMI must send it on all /api/hmi calls.
func HandlePairVerify(c *gin.Context) {
	var req struct {
		GatewayId string `json:"gatewayId"`
		Code      string `json:"code"`
		DeviceID  string `json:"deviceId"` // display hardware id (optional for old firmware)
		Name      string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid request"})
		return
	}

	pairingCodesMu.Lock()
	prunePairingLocked()
	e, ok := pairingCodes[req.GatewayId]
	if !ok {
		pairingCodesMu.Unlock()
		c.JSON(http.StatusOK, gin.H{"success": false, "error": "no active pairing request"})
		return
	}
	e.attempts++
	if e.attempts > pairMaxAttempts {
		for k, v := range pairingCodes {
			if v == e {
				delete(pairingCodes, k)
			}
		}
		pairingCodesMu.Unlock()
		c.JSON(http.StatusTooManyRequests, gin.H{"success": false, "error": "too many attempts, request a new code"})
		return
	}
	if e.code != req.Code {
		pairingCodesMu.Unlock()
		c.JSON(http.StatusOK, gin.H{"success": false, "error": "incorrect code"})
		return
	}
	for k, v := range pairingCodes {
		if v == e {
			delete(pairingCodes, k)
		}
	}
	gatewayID := e.gatewayID
	pairingCodesMu.Unlock()

	var gw Gateway
	if err := DB.First(&gw, "id = ?", gatewayID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "unknown gateway"})
		return
	}
	hmi, err := createHmi(gw, sanitizeDeviceID(req.DeviceID), req.Name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "could not register display"})
		return
	}
	token, err := GenerateHMIToken(gatewayID, hmi.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "token error"})
		return
	}
	LogAudit(c, "hmi_paired", "gateway", &gatewayID, "HMI paired with gateway")
	c.JSON(http.StatusOK, gin.H{"success": true, "token": token, "gatewayId": gatewayID, "hmiId": hmi.ID, "name": hmi.Name})
}
