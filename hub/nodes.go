package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// pinValueStore tracks the last commanded value per pin for each device.
var (
	pvMu    sync.Mutex
	pinVals = map[uint32]map[uint8]uint16{} // deviceID → pinNumber → value
)

func setPinValue(deviceID uint32, pin uint8, val uint16) {
	pvMu.Lock()
	defer pvMu.Unlock()
	if pinVals[deviceID] == nil {
		pinVals[deviceID] = map[uint8]uint16{}
	}
	pinVals[deviceID][pin] = val
}

func setPinValues(deviceID uint32, vals map[uint8]uint16) {
	pvMu.Lock()
	defer pvMu.Unlock()
	pinVals[deviceID] = vals
}

func getPinValues(deviceID uint32) map[uint8]uint16 {
	pvMu.Lock()
	defer pvMu.Unlock()
	m := pinVals[deviceID]
	if m == nil {
		return nil
	}
	cp := make(map[uint8]uint16, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

type NodeResponse struct {
	Node
	IsOnline bool `json:"isOnline"`
}

// NodeDetailResponse is returned by GetNode / GetNodeByDeviceID.
// It parses CapabilitiesConfig from its JSON string into a proper array.
type NodeDetailResponse struct {
	NodeID             string             `json:"nodeId"`
	GatewayID          string             `json:"gatewayId"`
	DeviceID           uint32             `json:"deviceId"`
	APIKey             string             `json:"apiKey,omitempty"`
	DeviceType         uint8              `json:"deviceType"`
	Name               string             `json:"name"`
	CapabilitiesConfig []CapabilityConfig `json:"capabilitiesConfig"`
	LastValue          uint16             `json:"value"`
	LastSeen           time.Time          `json:"lastSeen"`
	ConnectedAt        *time.Time         `json:"connectedAt"`
	CreatedAt          time.Time          `json:"createdAt"`
	IsOnline           bool               `json:"isOnline"`
}

type ProvisionRequest struct {
	DeviceID           uint32             `json:"deviceId" binding:"required"`
	Force              bool               `json:"force"`
	Name               string             `json:"name,omitempty"`
	CapabilitiesConfig []CapabilityConfig `json:"capabilitiesConfig,omitempty"`
}

type CrossProvisionRequest struct {
	DeviceID           uint32             `json:"deviceId" binding:"required"`
	GatewayID          string             `json:"gatewayId" binding:"required"`
	Force              bool               `json:"force"`
	Name               string             `json:"name,omitempty"`
	CapabilitiesConfig []CapabilityConfig `json:"capabilitiesConfig,omitempty"`
}

type ControlRequest struct {
	Value    uint16 `json:"value"`
	Pin      *int   `json:"pin,omitempty"`
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
			NodeID:             generateNodeID(),
			GatewayID:          gatewayID,
			DeviceID:           deviceID,
			APIKey:             p.APIKey,
			DeviceType:         p.DeviceType,
			Name:               p.Name,
			CapabilitiesConfig: p.CapabilitiesConfig,
			LastValue:          value,
			LastSeen:           now,
			ConnectedAt:        &now,
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

	// Store telemetry value as pin value for input capabilities
	if caps := parseCapabilitiesConfig(node.CapabilitiesConfig); caps != nil {
		for _, c := range caps {
			if c.Type == "analogInput" || c.Type == "digitalInput" {
				setPinValue(deviceID, uint8(c.Pin), value)
			}
		}
	}

	// Forward telemetry to bound output capabilities
	processBindings(gatewayID, deviceID, value)
}

func isNodeOnline(lastSeen time.Time) bool {
	if lastSeen.IsZero() {
		return false
	}
	return IsNodeOnline(lastSeen)
}

func parseCapabilitiesConfig(s string) []CapabilityConfig {
	if s == "" {
		return nil
	}
	var caps []CapabilityConfig
	if err := json.Unmarshal([]byte(s), &caps); err != nil {
		return nil
	}
	return caps
}

func nodeDetailResponse(node Node) NodeDetailResponse {
	gwOnline := true
	var gw Gateway
	if DB.First(&gw, "id = ?", node.GatewayID).Error == nil {
		gwOnline = gw.IsOnline
	}
	online := isNodeOnline(node.LastSeen) && gwOnline

	caps := parseCapabilitiesConfig(node.CapabilitiesConfig)
	pv := getPinValues(node.DeviceID)
	if pv != nil && caps != nil {
		for i, c := range caps {
			if v, ok := pv[uint8(c.Pin)]; ok {
				v := v
				caps[i].Value = &v
			}
		}
	}

	return NodeDetailResponse{
		NodeID:             node.NodeID,
		GatewayID:          node.GatewayID,
		DeviceID:           node.DeviceID,
		APIKey:             node.APIKey,
		DeviceType:         node.DeviceType,
		Name:               node.Name,
		CapabilitiesConfig: caps,
		LastValue:          node.LastValue,
		LastSeen:           node.LastSeen,
		ConnectedAt:        node.ConnectedAt,
		CreatedAt:          node.CreatedAt,
		IsOnline:           online,
	}
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

func capTypeToInt(typeStr string) int {
	switch typeStr {
	case "analogInput":
		return 0
	case "analogOutput":
		return 1
	case "digitalInput":
		return 2
	case "digitalOutput":
		return 3
	case "relay":
		return 4
	case "irTx":
		return 5
	case "irRx":
		return 6
	case "i2c":
		return 7
	case "uart":
		return 8
	default:
		return 0
	}
}

func compactCapabilities(caps []CapabilityConfig) string {
	if len(caps) == 0 {
		return "0"
	}
	var parts []string
	for _, c := range caps {
		label := strings.ReplaceAll(c.Label, ":", "_")
		parts = append(parts, fmt.Sprintf("%d:%d:%d:%s", capTypeToInt(c.Type), c.Pin, c.Extra, label))
	}
	return fmt.Sprintf("%d:%s", len(caps), strings.Join(parts, ":"))
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

	// Marshal capabilities to JSON for DB storage
	capsDB := ""
	if len(req.CapabilitiesConfig) > 0 {
		b, err := json.Marshal(req.CapabilitiesConfig)
		if err == nil {
			capsDB = string(b)
		}
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
		if req.Name != "" {
			existing.Name = req.Name
		}
		if len(req.CapabilitiesConfig) > 0 {
			existing.CapabilitiesConfig = capsDB
		}
		DB.Save(&existing)
		RemoveDiscoveredNode(gwID, req.DeviceID)

		if err := SendProvision(gwID, req.DeviceID, apiKey, existing.DeviceType, existing.Name, req.CapabilitiesConfig); err != nil {
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

	if err := SendProvision(gwID, req.DeviceID, apiKey, 0, req.Name, req.CapabilitiesConfig); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"status":  "registered_but_offline",
			"message": "Gateway is offline; provision key will be sent when gateway reconnects",
		})
		return
	}

	addPendingProvision(req.DeviceID, gwID, apiKey, 0, req.Name, capsDB)
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

	// Marshal capabilities to JSON for DB storage
	capDB := ""
	if len(req.CapabilitiesConfig) > 0 {
		b, err := json.Marshal(req.CapabilitiesConfig)
		if err == nil {
			capDB = string(b)
		}
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
		if req.Name != "" {
			existing.Name = req.Name
		}
		if len(req.CapabilitiesConfig) > 0 {
			existing.CapabilitiesConfig = capDB
		}
		DB.Save(&existing)
		RemoveDiscoveredNode(req.GatewayID, req.DeviceID)

		if err := SendProvision(req.GatewayID, req.DeviceID, apiKey, existing.DeviceType, existing.Name, req.CapabilitiesConfig); err != nil {
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

	if err := SendProvision(req.GatewayID, req.DeviceID, apiKey, 0, req.Name, req.CapabilitiesConfig); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"status":  "registered_but_offline",
			"message": "Gateway is offline; provision key will be sent when gateway reconnects",
		})
		return
	}

	addPendingProvision(req.DeviceID, req.GatewayID, apiKey, 0, req.Name, capDB)
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

	resp := nodeDetailResponse(node)
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

	resp := nodeDetailResponse(node)
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

	if req.Pin != nil {
		if err := SendPinCommandToGateway(node.GatewayID, node.DeviceID, uint8(*req.Pin), req.Value); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Gateway is offline"})
			return
		}
		setPinValue(node.DeviceID, uint8(*req.Pin), req.Value)
	} else if err := SendCommandToGateway(node.GatewayID, node.DeviceID, 2, req.Value); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Gateway is offline"})
		return
	}

	LogAudit(c, "node_command", "node", &node.NodeID,
		fmt.Sprintf("Command value=%d sent to node %s (%d)", req.Value, node.NodeID, node.DeviceID))

	c.JSON(http.StatusOK, gin.H{"status": "transmitted", "nodeId": node.NodeID, "value": req.Value})
}

// ── Ping ──────────────────────────────────────────────────────────────────────

type UpdateCapabilitiesRequest struct {
	CapabilitiesConfig []CapabilityConfig `json:"capabilitiesConfig"`
}

func UpdateNodeCapabilities(c *gin.Context) {
	userID := c.GetUint("userID")
	nodeID := c.Param("nodeId")

	var node Node
	if err := DB.Joins("JOIN gateways ON gateways.id = nodes.gateway_id").
		Where("nodes.node_id = ? AND gateways.user_id = ?", nodeID, userID).
		First(&node).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}

	var req UpdateCapabilitiesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	b, err := json.Marshal(req.CapabilitiesConfig)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to marshal capabilities"})
		return
	}

	DB.Model(&node).Update("capabilities_config", string(b))

	// Re-send provision to the gateway to apply new capabilities
	if err := SendProvision(node.GatewayID, node.DeviceID, node.APIKey, node.DeviceType, node.Name, req.CapabilitiesConfig); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"status":  "updated_but_offline",
			"message": "Gateway is offline; will be sent when gateway reconnects",
		})
		return
	}

	LogAudit(c, "node_capabilities_updated", "node", &node.NodeID,
		fmt.Sprintf("Capabilities updated for node %s (%d)", node.NodeID, node.DeviceID))

	c.JSON(http.StatusOK, gin.H{"status": "updated"})
}

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
