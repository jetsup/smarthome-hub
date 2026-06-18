package hub

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

type NodeResponse struct {
	Node
	IsOnline bool `json:"isOnline"`
}

type ProvisionRequest struct {
	DeviceID uint32 `json:"deviceId" binding:"required"`
	Force    bool   `json:"force"`
}

type CrossProvisionRequest struct {
	DeviceID  uint32 `json:"deviceId" binding:"required"`
	GatewayID string `json:"gatewayId" binding:"required"`
	Force     bool   `json:"force"`
}

type ControlRequest struct {
	Value uint16 `json:"value"`
}

// ReportNode handles telemetry from a node. If the node has a pending provision,
// the DB record is created on first telemetry (deferred activation).
func ReportNode(gatewayID string, deviceID uint32, value uint16) {
	var node Node
	result := DB.Where("gateway_id = ? AND device_id = ?", gatewayID, deviceID).First(&node)

	if result.Error != nil {
		// No DB record yet — check for a pending provision
		p := consumePendingProvision(deviceID)
		if p == nil {
			return // ignore unknown nodes
		}
		// First telemetry after provision — create the DB record
		now := time.Now()
		node = Node{
			NodeID:      generateNodeID(),
			GatewayID:   gatewayID,
			DeviceID:    deviceID,
			APIKey:      p.APIKey,
			DeviceType:  p.DeviceType,
			LastValue:   value,
			LastSeen:    now,
			ConnectedAt: &now,
		}
		DB.Create(&node)
		NetworkRegistry[deviceID] = DeviceState{
			DeviceID: deviceID,
			Value:    value,
		}
		RemoveDiscoveredNode(gatewayID, deviceID)
		LogAudit(nil, "node_activated", "gateway", &gatewayID,
			fmt.Sprintf("Node %d (%s) activated on gateway %s via deferred provision", deviceID, node.NodeID, gatewayID))
		return
	}

	// Existing node — update telemetry
	DB.Model(&node).Updates(map[string]interface{}{
		"last_value": value,
		"last_seen":  time.Now(),
	})

	NetworkRegistry[deviceID] = DeviceState{
		DeviceID: deviceID,
		Value:    value,
	}
}

func isNodeOnline(lastSeen time.Time) bool {
	if lastSeen.IsZero() {
		return false
	}
	return IsNodeOnline(lastSeen)
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
	DB.Where("gateway_id = ? AND connected_at IS NOT NULL", gwID).Find(&nodes)

	resp := make([]NodeResponse, 0, len(nodes))
	for _, n := range nodes {
		resp = append(resp, NodeResponse{
			Node:     n,
			IsOnline: isNodeOnline(n.LastSeen),
		})
	}

	c.JSON(http.StatusOK, resp)
}

// ── Per-gateway provision ─────────────────────────────────────────────────────

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

	// Check conflict with another gateway of the same user
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

	// Same gateway — re-provision existing node
	var existing Node
	if DB.Where("gateway_id = ? AND device_id = ?", gwID, req.DeviceID).First(&existing).Error == nil {
		existing.APIKey = apiKey
		existing.ConnectedAt = &now
		DB.Save(&existing)
		RemoveDiscoveredNode(gwID, req.DeviceID)

		if err := SendProvision(gwID, req.DeviceID, apiKey, existing.DeviceType); err != nil {
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

	// Force-move from another gateway
	if conflictResult.Error == nil {
		DB.Delete(&conflicting) // remove old record, will recreate on telemetry
	}

	// Brand-new node — deferred provision: no DB record until telemetry arrives
	RemoveDiscoveredNode(gwID, req.DeviceID)

	if err := SendProvision(gwID, req.DeviceID, apiKey, 0); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"status":  "registered_but_offline",
			"message": "Gateway is offline; provision key will be sent when gateway reconnects",
		})
		return
	}

	addPendingProvision(req.DeviceID, gwID, apiKey, 0)
	LogAudit(c, "node_provisioning", "gateway", &gw.ID, fmt.Sprintf("Node %d provisioning sent (awaiting activation) on gateway: %s", req.DeviceID, gw.Name))

	c.JSON(http.StatusOK, gin.H{
		"status":  "provisioning",
		"message": "Provision command sent; node will appear once it reports in",
	})
}

// ── Cross-gateway provision ───────────────────────────────────────────────────

func ProvisionNodeToGateway(c *gin.Context) {
	userID := c.GetUint("userID")

	var req CrossProvisionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", req.GatewayID, userID).First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}

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

		if err := SendProvision(req.GatewayID, req.DeviceID, apiKey, existing.DeviceType); err != nil {
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

	// Force-move from another gateway
	if conflictResult.Error == nil {
		DB.Delete(&conflicting)
	}

	// Brand-new node — deferred provision
	RemoveDiscoveredNode(req.GatewayID, req.DeviceID)

	if err := SendProvision(req.GatewayID, req.DeviceID, apiKey, 0); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"status":  "registered_but_offline",
			"message": "Gateway is offline; provision key will be sent when gateway reconnects",
		})
		return
	}

	addPendingProvision(req.DeviceID, req.GatewayID, apiKey, 0)
	LogAudit(c, "node_provisioning", "gateway", &gw.ID, fmt.Sprintf("Node %d provisioning sent (awaiting activation) on gateway: %s", req.DeviceID, gw.Name))

	c.JSON(http.StatusOK, gin.H{
		"status":  "provisioning",
		"message": "Provision command sent; node will appear once it reports in",
	})
}

// ── Disconnect ────────────────────────────────────────────────────────────────

func DisconnectNode(c *gin.Context) {
	userID := c.GetUint("userID")
	gwID := c.Param("id")
	deviceIDStr := c.Param("deviceId")

	deviceID, err := strconv.ParseUint(deviceIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", gwID, userID).First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}

	if err := DB.Where("gateway_id = ? AND device_id = ?", gwID, deviceID).Delete(&Node{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to disconnect node"})
		return
	}

	SendCommandToGateway(gwID, uint32(deviceID), 2, 99)

	LogAudit(c, "node_disconnected", "gateway", &gw.ID, fmt.Sprintf("Node %d disconnected from gateway: %s", deviceID, gw.Name))
	c.JSON(http.StatusOK, gin.H{"status": "disconnected", "deviceId": deviceID})
}

// ── Node Detail ───────────────────────────────────────────────────────────────

func GetNode(c *gin.Context) {
	userID := c.GetUint("userID")
	nodeID := c.Param("nodeId")

	var node Node
	if err := DB.Joins("JOIN gateways ON gateways.id = nodes.gateway_id").
		Where("nodes.node_id = ? AND gateways.user_id = ?", nodeID, userID).
		First(&node).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}

	state, hasState := NetworkRegistry[node.DeviceID]

	resp := NodeResponse{
		Node:     node,
		IsOnline: isNodeOnline(node.LastSeen),
	}
	if hasState {
		resp.LastValue = state.Value
	}
	c.JSON(http.StatusOK, resp)
}

func GetNodeByDeviceID(c *gin.Context) {
	userID := c.GetUint("userID")
	deviceIDStr := c.Param("deviceId")

	deviceID, err := strconv.ParseUint(deviceIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	var node Node
	if err := DB.Joins("JOIN gateways ON gateways.id = nodes.gateway_id").
		Where("nodes.device_id = ? AND gateways.user_id = ?", uint32(deviceID), userID).
		First(&node).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}

	state, hasState := NetworkRegistry[node.DeviceID]

	resp := NodeResponse{
		Node:     node,
		IsOnline: isNodeOnline(node.LastSeen),
	}
	if hasState {
		resp.LastValue = state.Value
	}
	c.JSON(http.StatusOK, resp)
}

// ── Send Command to Node ──────────────────────────────────────────────────────

func ControlNode(c *gin.Context) {
	userID := c.GetUint("userID")
	nodeID := c.Param("nodeId")

	var node Node
	if err := DB.Joins("JOIN gateways ON gateways.id = nodes.gateway_id").
		Where("nodes.node_id = ? AND gateways.user_id = ?", nodeID, userID).
		First(&node).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}

	var req ControlRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := SendCommandToGateway(node.GatewayID, node.DeviceID, 2, req.Value); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Gateway is offline"})
		return
	}

	LogAudit(c, "node_command", "node", &node.NodeID,
		fmt.Sprintf("Command value=%d sent to node %s (%d)", req.Value, node.NodeID, node.DeviceID))

	c.JSON(http.StatusOK, gin.H{"status": "transmitted", "nodeId": node.NodeID, "value": req.Value})
}

// ── Ping ──────────────────────────────────────────────────────────────────────

func PingDevice(c *gin.Context) {
	userID := c.GetUint("userID")
	idStr := c.Param("id")

	deviceID, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	var node Node
	if err := DB.Joins("JOIN gateways ON gateways.id = nodes.gateway_id").
		Where("nodes.device_id = ? AND gateways.user_id = ?", uint32(deviceID), userID).
		First(&node).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}

	DB.Model(&node).Update("last_seen", time.Now())

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
