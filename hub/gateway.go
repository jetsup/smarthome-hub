package hub

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
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

	// Only count/show nodes that have actually reported in (non-zero LastSeen)
	filtered := make([]Node, 0)
	for _, n := range gw.Nodes {
		if !n.LastSeen.IsZero() {
			filtered = append(filtered, n)
		}
	}
	gw.Nodes = filtered
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

	resp := make([]NodeResponse, 0)
	for _, n := range nodes {
		if n.LastSeen.IsZero() {
			continue
		}
		resp = append(resp, NodeResponse{
			Node:     n,
			IsOnline: isNodeOnline(n.LastSeen),
		})
	}

	c.JSON(http.StatusOK, resp)
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
		return // Only update existing (provisioned) nodes; new nodes go through provisioning flow
	}

	DB.Model(&node).Updates(map[string]interface{}{
		"last_value": value,
		"last_seen":  "NOW()",
	})

	NetworkRegistry[deviceID] = DeviceState{
		DeviceID: deviceID,
		Value:    value,
	}
}

func generateNodeAPIKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return "node_" + hex.EncodeToString(b)
}

func StartScan(c *gin.Context) {
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

	if err := SendCommand(0, 4, 0); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Gateway is offline"})
		return
	}

	// Clear previous discoveries for this gateway
	ClearDiscoveredNodes(gwID)

	LogAudit(c, "scan_started", "gateway", &gw.ID, "Node scan started for gateway: "+gw.Name)
	c.JSON(http.StatusOK, gin.H{"status": "scanning"})
}

func GetDiscoveredNodes(c *gin.Context) {
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

	nodes := GetDiscoveredNodeSet(gwID)
	if len(nodes) == 0 {
		c.JSON(http.StatusOK, []uint32{})
		return
	}

	result := make([]uint32, 0, len(nodes))
	for devID := range nodes {
		result = append(result, devID)
	}
	c.JSON(http.StatusOK, result)
}

type ProvisionRequest struct {
	DeviceID uint32 `json:"deviceId" binding:"required"`
	Force    bool   `json:"force"`
}

type NodeResponse struct {
	Node
	IsOnline bool `json:"isOnline"`
}

func isNodeOnline(lastSeen time.Time) bool {
	if lastSeen.IsZero() {
		return false
	}
	return IsNodeOnline(lastSeen)
}

func ProvisionNode(c *gin.Context) {
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

	var req ProvisionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Check if this deviceId is already linked to another gateway owned by the same user
	var conflicting Node
	conflictResult := DB.Joins("JOIN gateways ON gateways.id = nodes.gateway_id").
		Where("nodes.device_id = ? AND gateways.user_id = ? AND nodes.gateway_id != ?",
			req.DeviceID, userID, gwID).
		First(&conflicting)
	if conflictResult.Error == nil && !req.Force {
		var conflictingGW Gateway
		DB.First(&conflictingGW, "id = ?", conflicting.GatewayID)
		c.JSON(http.StatusConflict, gin.H{
			"status": "gateway_conflict",
			"conflicting_gateway": gin.H{
				"id":   conflictingGW.ID,
				"name": conflictingGW.Name,
			},
			"message": fmt.Sprintf("This node is already linked to gateway \"%s\". Click Add again to switch it to this gateway.", conflictingGW.Name),
		})
		return
	}

	apiKey := generateNodeAPIKey()
	now := time.Now()

	// Same gateway — re-provision (retry from offline or re-add after reset)
	var existing Node
	if DB.Where("gateway_id = ? AND device_id = ?", gwID, req.DeviceID).First(&existing).Error == nil {
		existing.APIKey = apiKey
		existing.ConnectedAt = &now
		DB.Save(&existing)

		// Remove from discovered set
		if discovered := DiscoveredNodes[gwID]; discovered != nil {
			delete(discovered, req.DeviceID)
		}

		if err := SendProvision(req.DeviceID, apiKey); err != nil {
			c.JSON(http.StatusOK, gin.H{
				"status":  "registered_but_offline",
				"node":    existing,
				"message": "Gateway is offline; key will be sent when gateway reconnects",
			})
			return
		}

		LogAudit(c, "node_reprovisioned", "gateway", &gw.ID, fmt.Sprintf("Node %d re-provisioned on gateway: %s", req.DeviceID, gw.Name))
		c.JSON(http.StatusOK, gin.H{"status": "provisioned", "node": existing})
		return
	}

	// Conflict with force flag or different user — move/update the existing node
	if conflictResult.Error == nil {
		// Move the node to the new gateway (same user, force mode)
		conflicting.GatewayID = gwID
		conflicting.APIKey = apiKey
		conflicting.ConnectedAt = &now
		DB.Save(&conflicting)

		if err := SendProvision(req.DeviceID, apiKey); err != nil {
			c.JSON(http.StatusOK, gin.H{
				"status":  "registered_but_offline",
				"node":    conflicting,
				"message": "Gateway is offline; key will be sent when gateway reconnects",
			})
			return
		}

		LogAudit(c, "node_moved", "gateway", &gw.ID, fmt.Sprintf("Node %d moved from gateway %s to %s", req.DeviceID, conflicting.GatewayID, gwID))
		c.JSON(http.StatusOK, gin.H{"status": "provisioned", "node": conflicting})
		return
	}

	// Brand-new node
	node := Node{
		GatewayID:   gwID,
		DeviceID:    req.DeviceID,
		APIKey:      apiKey,
		ConnectedAt: &now,
	}

	if err := DB.Create(&node).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create node"})
		return
	}

	// Remove from discovered set
	if discovered := DiscoveredNodes[gwID]; discovered != nil {
		delete(discovered, req.DeviceID)
	}

	if err := SendProvision(req.DeviceID, apiKey); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"status":  "registered_but_offline",
			"node":    node,
			"message": "Node registered but gateway is offline; key will be sent when gateway reconnects",
		})
		return
	}

	LogAudit(c, "node_provisioned", "gateway", &gw.ID, fmt.Sprintf("Node %d provisioned on gateway: %s", req.DeviceID, gw.Name))

	c.JSON(http.StatusCreated, gin.H{"status": "provisioned", "node": node})
}

func PingDevice(c *gin.Context) {
	userID := c.GetUint("userID")
	idStr := c.Param("id")

	deviceID, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	// Find node belonging to any gateway owned by this user
	var node Node
	if err := DB.Joins("JOIN gateways ON gateways.id = nodes.gateway_id").
		Where("nodes.device_id = ? AND gateways.user_id = ?", uint32(deviceID), userID).
		First(&node).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}

	DB.Model(&node).Update("last_seen", "NOW()")

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ── Aggregate node discovery (across all gateways) ────────────────────────────

func ScanAllGateways(c *gin.Context) {
	userID := c.GetUint("userID")

	var gateways []Gateway
	DB.Where("user_id = ? AND is_online = ?", userID, true).Find(&gateways)

	for _, gw := range gateways {
		ClearDiscoveredNodes(gw.ID)
	}

	if err := SendCommand(0, 4, 0); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "No gateways online"})
		return
	}

	LogAudit(c, "scan_all_started", "gateway", nil, fmt.Sprintf("Scanned %d gateways", len(gateways)))
	c.JSON(http.StatusOK, gin.H{"status": "scanning", "gatewayCount": len(gateways)})
}

func DisconnectNode(c *gin.Context) {
	userID := c.GetUint("userID")
	gwID := c.Param("id")
	deviceIDStr := c.Param("deviceId")

	deviceID, err := strconv.ParseUint(deviceIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	// Verify gateway ownership
	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", gwID, userID).First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}

	// Delete the node record
	if err := DB.Where("gateway_id = ? AND device_id = ?", gwID, deviceID).Delete(&Node{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to disconnect node"})
		return
	}

	// Send factory reset command to node (value=99)
	SendCommand(uint32(deviceID), 2, 99)

	LogAudit(c, "node_disconnected", "gateway", &gw.ID, fmt.Sprintf("Node %d disconnected from gateway: %s", deviceID, gw.Name))
	c.JSON(http.StatusOK, gin.H{"status": "disconnected", "deviceId": deviceID})
}

func GetAllDiscoveredNodesHandler(c *gin.Context) {
	userID := c.GetUint("userID")

	// Get all gateways owned by this user
	var gateways []Gateway
	DB.Where("user_id = ?", userID).Find(&gateways)

	// Build set of gateway IDs for this user
	userGatewayIDs := make(map[string]bool)
	for _, gw := range gateways {
		userGatewayIDs[gw.ID] = true
	}

	// Filter discovered nodes to only those from the user's gateways
	allDiscovered := GetAllDiscoveredNodes()
	filtered := make([]DiscoveredNodeInfo, 0, len(allDiscovered))
	for _, d := range allDiscovered {
		if userGatewayIDs[d.GatewayID] {
			filtered = append(filtered, d)
		}
	}

	c.JSON(http.StatusOK, filtered)
}

type CrossProvisionRequest struct {
	DeviceID  uint32 `json:"deviceId" binding:"required"`
	GatewayID string `json:"gatewayId" binding:"required"`
	Force     bool   `json:"force"`
}

func ProvisionNodeToGateway(c *gin.Context) {
	userID := c.GetUint("userID")

	var req CrossProvisionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Verify gateway belongs to user
	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", req.GatewayID, userID).First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}

	// Reuse existing ProvisionNode logic by re-routing through context
	// Build a minimal context-like flow
	apiKey := generateNodeAPIKey()
	now := time.Now()

	// Check conflict with another gateway of same user
	var conflicting Node
	conflictResult := DB.Joins("JOIN gateways ON gateways.id = nodes.gateway_id").
		Where("nodes.device_id = ? AND gateways.user_id = ? AND nodes.gateway_id != ?",
			req.DeviceID, userID, req.GatewayID).
		First(&conflicting)
	if conflictResult.Error == nil && !req.Force {
		var conflictingGW Gateway
		DB.First(&conflictingGW, "id = ?", conflicting.GatewayID)
		c.JSON(http.StatusConflict, gin.H{
			"status": "gateway_conflict",
			"conflicting_gateway": gin.H{
				"id":   conflictingGW.ID,
				"name": conflictingGW.Name,
			},
			"message": fmt.Sprintf("This node is already linked to gateway \"%s\". Click Add again to switch it to this gateway.", conflictingGW.Name),
		})
		return
	}

	// Same gateway re-provision
	var existing Node
	if DB.Where("gateway_id = ? AND device_id = ?", req.GatewayID, req.DeviceID).First(&existing).Error == nil {
		existing.APIKey = apiKey
		existing.ConnectedAt = &now
		DB.Save(&existing)
		RemoveDiscoveredNode(req.GatewayID, req.DeviceID)

		if err := SendProvision(req.DeviceID, apiKey); err != nil {
			c.JSON(http.StatusOK, gin.H{
				"status":  "registered_but_offline",
				"node":    existing,
				"message": "Gateway is offline; key will be sent when gateway reconnects",
			})
			return
		}
		LogAudit(c, "node_reprovisioned", "gateway", &gw.ID, fmt.Sprintf("Node %d re-provisioned on gateway: %s", req.DeviceID, gw.Name))
		c.JSON(http.StatusOK, gin.H{"status": "provisioned", "node": existing})
		return
	}

	// Move from another gateway (force)
	if conflictResult.Error == nil {
		conflicting.GatewayID = req.GatewayID
		conflicting.APIKey = apiKey
		conflicting.ConnectedAt = &now
		DB.Save(&conflicting)
		RemoveDiscoveredNode(req.GatewayID, req.DeviceID)

		if err := SendProvision(req.DeviceID, apiKey); err != nil {
			c.JSON(http.StatusOK, gin.H{
				"status":  "registered_but_offline",
				"node":    conflicting,
				"message": "Gateway is offline; key will be sent when gateway reconnects",
			})
			return
		}
		LogAudit(c, "node_moved", "gateway", &gw.ID, fmt.Sprintf("Node %d moved to gateway %s", req.DeviceID, req.GatewayID))
		c.JSON(http.StatusOK, gin.H{"status": "provisioned", "node": conflicting})
		return
	}

	// Brand new
	node := Node{
		GatewayID:   req.GatewayID,
		DeviceID:    req.DeviceID,
		APIKey:      apiKey,
		ConnectedAt: &now,
	}
	if err := DB.Create(&node).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create node"})
		return
	}
	RemoveDiscoveredNode(req.GatewayID, req.DeviceID)

	if err := SendProvision(req.DeviceID, apiKey); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"status":  "registered_but_offline",
			"node":    node,
			"message": "Gateway is offline; key will be sent when gateway reconnects",
		})
		return
	}
	LogAudit(c, "node_provisioned", "gateway", &gw.ID, fmt.Sprintf("Node %d provisioned on gateway: %s", req.DeviceID, gw.Name))
	c.JSON(http.StatusCreated, gin.H{"status": "provisioned", "node": node})
}
