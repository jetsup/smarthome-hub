package hub

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"sync"

	"github.com/gin-gonic/gin"
)

var (
	pairingCodes   = map[string]string{}
	pairingCodesMu sync.RWMutex
)

func HandlePairRequest(c *gin.Context) {
	var req struct {
		GatewayId string `json:"gatewayId"`
		Name      string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.GatewayId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid request"})
		return
	}

	// Resolve decimal announce ID to hex DB ID via TCP-registered mapping
	hexId := req.GatewayId
	if id, err := strconv.ParseUint(req.GatewayId, 10, 32); err == nil {
		if mappedId := getGatewayIDByAnnounceID(uint32(id)); mappedId != "" {
			hexId = mappedId
			log.Printf("Pairing: resolved announce ID %d → hex %s", id, mappedId)
		}
	}
	// Fallback: try name-based lookup (HMI announces truncated name)
	if hexId == req.GatewayId && req.Name != "" {
		var gw Gateway
		if err := DB.Where("name LIKE ?", req.Name+"%").First(&gw).Error; err == nil && gw.ID != "" {
			hexId = gw.ID
			log.Printf("Pairing: resolved name '%s' → hex %s", req.Name, hexId)
		}
	}

	code := fmt.Sprintf("%06d", rand.Intn(1000000))

	pairingCodesMu.Lock()
	pairingCodes[hexId] = code           // Flutter polls by hex DB ID
	pairingCodes[req.GatewayId] = code    // HMI verifies by decimal announce ID
	pairingCodesMu.Unlock()

	err := SendRawTextToGateway(hexId, "PAIR:CODE:"+code)
	if err != nil {
		log.Printf("Pairing: gateway %s offline for TCP delivery (%v)", hexId, err)
		c.JSON(http.StatusOK, gin.H{"success": true, "note": "gateway offline, code stored"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

func HandleGetPairCode(c *gin.Context) {
	gatewayId := c.Param("gatewayId")

	pairingCodesMu.RLock()
	code, exists := pairingCodes[gatewayId]
	if !exists {
		// Fallback: return first stored code (only one active pairing at a time)
		// This covers mismatch between decimal announce ID and hex DB ID
		for _, v := range pairingCodes {
			code = v
			exists = true
			break
		}
	}
	pairingCodesMu.RUnlock()

	if !exists {
		c.JSON(http.StatusOK, gin.H{"code": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": code})
}

func HandlePairVerify(c *gin.Context) {
	var req struct {
		GatewayId string `json:"gatewayId"`
		Code      string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid request"})
		return
	}

	pairingCodesMu.Lock()
	stored, exists := pairingCodes[req.GatewayId]
	if exists && stored == req.Code {
		delete(pairingCodes, req.GatewayId)
		pairingCodesMu.Unlock()
		c.JSON(http.StatusOK, gin.H{"success": true})
	} else {
		pairingCodesMu.Unlock()
		c.JSON(http.StatusOK, gin.H{"success": false, "error": "incorrect code"})
	}
}
