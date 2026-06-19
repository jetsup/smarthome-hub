package hub

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type WifiCredentialRequest struct {
	SSID     string `json:"ssid" binding:"required"`
	Password string `json:"password"`
}

func ListWifiCredentials(c *gin.Context) {
	userID := c.GetUint("userID")
	gwID := c.Param("id")

	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", gwID, userID).First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}

	var creds []WifiCredential
	DB.Where("gateway_id = ?", gwID).Order("created_at DESC").Find(&creds)

	c.JSON(http.StatusOK, creds)
}

func SaveWifiCredential(c *gin.Context) {
	userID := c.GetUint("userID")
	gwID := c.Param("id")

	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", gwID, userID).First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}

	var req WifiCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	cred := WifiCredential{
		GatewayID: gwID,
		SSID:      req.SSID,
		Password:  req.Password,
	}
	if err := DB.Create(&cred).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save credential"})
		return
	}

	// Forward to gateway via TCP
	cmd := "WIFI:" + req.SSID + ":" + req.Password
	if err := SendRawTextToGateway(gwID, cmd); err != nil {
		log.Printf("Failed to forward WiFi config to gateway %s: %v", gwID, err)
	}
	LogAudit(c, "wifi_credential_saved", "gateway", &gwID, "WiFi credential saved for SSID: "+req.SSID)

	// Update gateway name to SSID if not already set
	if gw.Name == "" || gw.Name == "New Gateway" {
		now := time.Now()
		DB.Model(&gw).Updates(map[string]interface{}{
			"name":       req.SSID,
			"updated_at": now,
		})
	}

	c.JSON(http.StatusCreated, cred)
}
