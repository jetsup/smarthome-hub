package hub

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

func generateGatewayID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b) // 16 hex characters
}

func generateAPIKey() string {
	b := make([]byte, 24)
	rand.Read(b)
	return "gwy_" + hex.EncodeToString(b)
}

type CreateGatewayRequest struct {
	Name string `json:"name" binding:"required"`
}

func ListGateways(c *gin.Context) {
	userID := c.GetUint("userID")

	var gateways []Gateway
	DB.Where("user_id = ?", userID).Find(&gateways)

	for i := range gateways {
		gateways[i].APIKey = maskKey(gateways[i].APIKey)
	}

	c.JSON(http.StatusOK, gateways)
}

func CreateGateway(c *gin.Context) {
	userID := c.GetUint("userID")

	var req CreateGatewayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	gw := Gateway{
		ID:               generateGatewayID(),
		UserID:           userID,
		Name:             req.Name,
		APIKey:           generateAPIKey(),
		IsOnline:         false,
		APIKeyAssignedAt: nil,
	}

	if err := DB.Create(&gw).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create gateway"})
		return
	}

	LogAudit(c, "gateway_created", "gateway", &gw.ID, "Gateway created: "+gw.Name)

	c.JSON(http.StatusCreated, gin.H{
		"id":     gw.ID,
		"name":   gw.Name,
		"apiKey": gw.APIKey,
		"online": gw.IsOnline,
	})
}

func GetGateway(c *gin.Context) {
	userID := c.GetUint("userID")
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid gateway ID"})
		return
	}

	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", id, userID).Preload("Nodes").First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}

	gw.APIKey = maskKey(gw.APIKey)
	nodeCount := len(gw.Nodes)

	c.JSON(http.StatusOK, gin.H{
		"id":        gw.ID,
		"name":      gw.Name,
		"apiKey":    gw.APIKey,
		"online":    gw.IsOnline,
		"nodeCount": nodeCount,
		"nodes":     gw.Nodes,
	})
}

func DeleteGateway(c *gin.Context) {
	userID := c.GetUint("userID")
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid gateway ID"})
		return
	}

	result := DB.Where("id = ? AND user_id = ?", id, userID).Delete(&Gateway{})
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}

	LogAudit(c, "gateway_deleted", "gateway", &id, "Gateway deleted")

	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func GetGatewayAPIKey(c *gin.Context) {
	userID := c.GetUint("userID")
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid gateway ID"})
		return
	}

	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", id, userID).First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}

	now := time.Now()
	DB.Model(&gw).Update("api_key_assigned_at", &now)

	LogAudit(c, "api_key_retrieved", "gateway", &gw.ID, "API key retrieved for gateway: "+gw.Name)

	c.JSON(http.StatusOK, gin.H{"apiKey": gw.APIKey})
}

func ListNodes(c *gin.Context) {
	userID := c.GetUint("userID")
	gwID := c.Param("id")
	if gwID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid gateway ID"})
		return
	}

	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", gwID, userID).First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}

	var nodes []Node
	DB.Where("gateway_id = ?", gwID).Find(&nodes)

	c.JSON(http.StatusOK, nodes)
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return key
	}
	return key[:4] + "…" + key[len(key)-4:]
}

func ReportNode(gatewayID string, deviceID uint32, value uint16) {
	var node Node
	result := DB.Where("gateway_id = ? AND device_id = ?", gatewayID, deviceID).First(&node)

	if result.Error != nil {
		node = Node{
			GatewayID: gatewayID,
			DeviceID:  deviceID,
			LastValue: value,
		}
		DB.Create(&node)
	} else {
		DB.Model(&node).Updates(map[string]interface{}{
			"last_value": value,
			"last_seen":  "NOW()",
		})
	}

	NetworkRegistry[deviceID] = DeviceState{
		DeviceID: deviceID,
		Value:    value,
	}
}
