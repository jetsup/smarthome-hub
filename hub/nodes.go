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
	"gorm.io/gorm"
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

func clearPinValues(deviceID uint32) {
	pvMu.Lock()
	defer pvMu.Unlock()
	delete(pinVals, deviceID)
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
	NodeID             string                            `json:"nodeId"`
	GatewayID          string                            `json:"gatewayId"`
	DeviceID           uint32                            `json:"deviceId"`
	APIKey             string                            `json:"apiKey,omitempty"`
	DeviceType         uint8                             `json:"deviceType"`
	Name               string                            `json:"name"`
	CapabilitiesConfig []CapabilityConfig                `json:"capabilitiesConfig"`
	LastValue          uint16                            `json:"value"`
	LastSeen           time.Time                         `json:"lastSeen"`
	ConnectedAt        *time.Time                        `json:"connectedAt"`
	CreatedAt          time.Time                         `json:"createdAt"`
	IsOnline           bool                              `json:"isOnline"`
	HmiID              *uint                             `json:"hmiId"` // the one display this node is shown on
	Readings           []ReadingValue                    `json:"readings"`
	Effects            map[string]map[string]interface{} `json:"effects"`
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

// ResetCommandValue is the whole-node command value the node firmware treats as
// a factory reset. It is reserved for internal use (disconnect / gateway delete).
const ResetCommandValue = 99

// RestartCommandValue makes a node reboot (nothing is erased).
const RestartCommandValue = 98

type ControlRequest struct {
	Value uint16 `json:"value"`
	Pin   *int   `json:"pin,omitempty"`
}

// ReportNode handles telemetry from a node. If the node has a pending provision,
// the DB record is created on first telemetry (deferred activation).
// telemetryCache avoids a DB round-trip (and write) for every telemetry frame:
// nodes may report many times per second, the DB only needs ~1 write / 2 s.
type telemetryEntry struct {
	node      Node
	fetched   time.Time
	lastWrite time.Time
	lastValue uint16
}

var (
	telemetryCache   = map[uint32]*telemetryEntry{}
	telemetryCacheMu sync.Mutex
)

const (
	telemetryNodeTTL    = 3 * time.Second
	telemetryWriteEvery = 2 * time.Second
)

func invalidateTelemetryCache(deviceID uint32) {
	telemetryCacheMu.Lock()
	delete(telemetryCache, deviceID)
	telemetryCacheMu.Unlock()
}

func ReportNode(gatewayID string, deviceID uint32, value uint16) {
	var node Node
	var result *gorm.DB

	telemetryCacheMu.Lock()
	ent := telemetryCache[deviceID]
	cached := ent != nil && ent.node.GatewayID == gatewayID && time.Since(ent.fetched) < telemetryNodeTTL
	if cached {
		node = ent.node
	}
	telemetryCacheMu.Unlock()

	if !cached {
		result = DB.Where("gateway_id = ? AND device_id = ?", gatewayID, deviceID).First(&node)
	}

	if !cached && result.Error == nil {
		telemetryCacheMu.Lock()
		telemetryCache[deviceID] = &telemetryEntry{node: node, fetched: time.Now(), lastValue: value}
		telemetryCacheMu.Unlock()
	}

	if !cached && result.Error != nil {
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
			HmiID:              defaultHmiFor(gatewayID),
		}
		DB.Create(&node)
		registrySet(DeviceState{DeviceID: deviceID, Value: value})
		RemoveDiscoveredNode(gatewayID, deviceID)
		LogAudit(nil, "node_activated", "gateway", &gatewayID,
			fmt.Sprintf("Node %d (%s) activated on gateway %s via deferred provision", deviceID, node.NodeID, gatewayID))
		return
	}

	// Existing node — persist at most every telemetryWriteEvery (or on a changed value
	// once the window has passed); in-memory state below is always updated.
	telemetryCacheMu.Lock()
	ent = telemetryCache[deviceID]
	if ent == nil {
		ent = &telemetryEntry{node: node, fetched: time.Now()}
		telemetryCache[deviceID] = ent
	}
	doWrite := time.Since(ent.lastWrite) >= telemetryWriteEvery
	if doWrite {
		ent.lastWrite = time.Now()
	}
	ent.lastValue = value
	telemetryCacheMu.Unlock()
	if doWrite {
		DB.Model(&node).Updates(map[string]interface{}{
			"last_value": value,
			"last_seen":  time.Now(),
		})
	}

	registrySet(DeviceState{DeviceID: deviceID, Value: value})

	// Store telemetry value as pin value for input capabilities
	// and forward each input pin's value to any bound output target.
	if caps := parseCapabilitiesConfig(node.CapabilitiesConfig); caps != nil && !readingsFresh(deviceID) {
		for _, c := range caps {
			if c.Type == "analogInput" || c.Type == "digitalInput" {
				pin := uint8(c.Pin)
				setPinValue(deviceID, pin, value)
				processBindings(gatewayID, deviceID, pin, value)
			}
		}
	}
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

	caps := decorateCaps(node)

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
		HmiID:              node.HmiID,
		Readings:           readingsFor(node),
		Effects:            effectsFor(node),
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
			IsOnline: isNodeOnline(n.LastSeen) && getTCPConn(n.GatewayID) != nil,
		})
	}

	c.JSON(http.StatusOK, resp)
}

func capTypeToInt(typeStr string) int {
	if ct := capTypeByKey(typeStr); ct != nil {
		return ct.Code
	}
	return 0
}

func compactCapabilities(caps []CapabilityConfig) string {
	if len(caps) == 0 {
		return "0"
	}
	var parts []string
	for _, c := range caps {
		label := strings.ReplaceAll(c.Label, ":", "_")
		flags := 0
		if c.Ring {
			flags |= 1
		}
		// type:pin:extra:count:flags:pin2:label
		parts = append(parts, fmt.Sprintf("%d:%d:%d:%d:%d:%d:%s", capTypeToInt(c.Type), c.Pin, c.Extra, c.Count, flags, c.Pin2, label))
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

	if len(req.CapabilitiesConfig) > 0 {
		if err := validateCapabilities(req.CapabilitiesConfig); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
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

	if len(req.CapabilitiesConfig) > 0 {
		if err := validateCapabilities(req.CapabilitiesConfig); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
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

	invalidateTelemetryCache(uint32(deviceID))
	if err := DB.Where("gateway_id = ? AND device_id = ?", gwID, deviceID).Delete(&Node{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to disconnect node"})
		return
	}

	SendCommandToGateway(gwID, uint32(deviceID), 2, ResetCommandValue)

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

	state, hasState := registryGet(node.DeviceID)

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

	state, hasState := registryGet(node.DeviceID)

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
		pin := *req.Pin
		if errMsg := ValidateBindingPin(pin, ""); errMsg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "pin: " + errMsg})
			return
		}
		// Update pinValStore first so polling clients see the value immediately
		setPinValue(node.DeviceID, uint8(pin), req.Value)
		if err := SendPinCommandToGateway(node.GatewayID, node.DeviceID, uint8(pin), req.Value); err != nil {
			fmt.Printf("ControlNode: gateway %s offline, pin value stored locally\n", node.GatewayID)
		}
	} else if req.Value == ResetCommandValue || req.Value == RestartCommandValue {
		c.JSON(http.StatusBadRequest, gin.H{"error": "values 98 and 99 are reserved"})
		return
	} else if err := SendCommandToGateway(node.GatewayID, node.DeviceID, 2, req.Value); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Gateway is offline"})
		return
	}

	LogAudit(c, "node_command", "node", &node.NodeID,
		fmt.Sprintf("Command value=%d sent to node %s (%d)", req.Value, node.NodeID, node.DeviceID))

	c.JSON(http.StatusOK, gin.H{"status": "transmitted", "nodeId": node.NodeID, "value": req.Value})
}

// ── HMI Unauthenticated Handlers ──────────────────────────────────────────────

type HMIDeviceInfo struct {
	DeviceID      uint32             `json:"deviceId"`
	Name          string             `json:"name"`
	IsOnline      bool               `json:"isOnline"`
	IsProvisioned bool               `json:"isProvisioned"`
	Value         uint16             `json:"value"`
	Capabilities  []CapabilityConfig `json:"capabilitiesConfig"`
}

func HMIListAllNodes(c *gin.Context) {
	// The HMI token is scoped to one gateway; the path id is ignored.
	gwID := c.GetString("hmiGatewayID")
	hmiID := c.GetUint("hmiID")

	// Get provisioned nodes — only the ones assigned to THIS display.
	var nodes []Node
	DB.Where("gateway_id = ? AND hmi_id = ? AND connected_at IS NOT NULL", gwID, hmiID).Find(&nodes)

	resp := make([]HMIDeviceInfo, 0, len(nodes)+10)

	for _, n := range nodes {
		caps := decorateCaps(n)
		resp = append(resp, HMIDeviceInfo{
			DeviceID:      n.DeviceID,
			Name:          n.Name,
			IsOnline:      isNodeOnline(n.LastSeen) && getTCPConn(n.GatewayID) != nil,
			IsProvisioned: true,
			Value:         n.LastValue,
			Capabilities:  caps,
		})
	}

	// Get discovered (unprovisioned) nodes
	discovered := GetDiscoveredNodeSet(gwID)
	if discovered != nil {
		provisionedIDs := make(map[uint32]bool)
		for _, n := range nodes {
			provisionedIDs[n.DeviceID] = true
		}
		for devID := range discovered {
			if !provisionedIDs[devID] {
				resp = append(resp, HMIDeviceInfo{
					DeviceID:      devID,
					Name:          "Device " + strconv.FormatUint(uint64(devID), 10),
					IsOnline:      false,
					IsProvisioned: false,
					Value:         0,
				})
			}
		}
	}

	c.JSON(http.StatusOK, resp)
}

func HMIListGateways(c *gin.Context) {
	var gateways []Gateway
	DB.Find(&gateways)

	type HmiGateway struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		IsOnline bool   `json:"isOnline"`
	}

	resp := make([]HmiGateway, 0, len(gateways))
	for _, g := range gateways {
		resp = append(resp, HmiGateway{
			ID:       g.ID,
			Name:     g.Name,
			IsOnline: g.IsOnline,
		})
	}
	c.JSON(http.StatusOK, resp)
}

func HMIListNodes(c *gin.Context) {
	gwID := c.GetString("hmiGatewayID")

	var nodes []Node
	DB.Where("gateway_id = ? AND hmi_id = ? AND connected_at IS NOT NULL", gwID, c.GetUint("hmiID")).Find(&nodes)

	resp := make([]NodeResponse, 0, len(nodes))
	for _, n := range nodes {
		resp = append(resp, NodeResponse{
			Node:     n,
			IsOnline: isNodeOnline(n.LastSeen) && getTCPConn(n.GatewayID) != nil,
		})
	}

	c.JSON(http.StatusOK, resp)
}

func HMIGetNodeDetail(c *gin.Context) {
	deviceIDStr := c.Param("deviceId")

	deviceID, err := strconv.ParseUint(deviceIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	var node Node
	if err := DB.Where("device_id = ? AND gateway_id = ? AND hmi_id = ?", uint32(deviceID), c.GetString("hmiGatewayID"), c.GetUint("hmiID")).First(&node).Error; err != nil {
		// Either it does not exist or it belongs to another display: a node is
		// only ever reachable from the display it is assigned to.
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}

	state, hasState := registryGet(node.DeviceID)

	resp := nodeDetailResponse(node)
	if hasState {
		resp.LastValue = state.Value
	}
	c.JSON(http.StatusOK, resp)
}

func HMIControlNode(c *gin.Context) {
	deviceIDStr := c.Param("deviceId")

	deviceID, err := strconv.ParseUint(deviceIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	var node Node
	if err := DB.Where("device_id = ? AND gateway_id = ? AND hmi_id = ?", uint32(deviceID), c.GetString("hmiGatewayID"), c.GetUint("hmiID")).First(&node).Error; err != nil {
		// Either it does not exist or it belongs to another display: a node is
		// only ever reachable from the display it is assigned to.
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}

	// Everything reaches a node through its gateway. If the gateway is not connected
	// the display must not be able to "control" anything.
	if getTCPConn(node.GatewayID) == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Gateway is offline"})
		return
	}

	var req ControlRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Pin != nil {
		pin := *req.Pin
		if errMsg := ValidateBindingPin(pin, ""); errMsg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "pin: " + errMsg})
			return
		}
		// Update pinValStore first so polling clients see the value immediately
		setPinValue(node.DeviceID, uint8(pin), req.Value)
		if err := SendPinCommandToGateway(node.GatewayID, node.DeviceID, uint8(pin), req.Value); err != nil {
			fmt.Printf("HMIControlNode: gateway %s offline, pin value stored locally\n", node.GatewayID)
		}
	} else if req.Value == ResetCommandValue || req.Value == RestartCommandValue {
		c.JSON(http.StatusBadRequest, gin.H{"error": "values 98 and 99 are reserved"})
		return
	} else if err := SendCommandToGateway(node.GatewayID, node.DeviceID, 2, req.Value); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Gateway is offline"})
		return
	}

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

	if err := validateCapabilities(req.CapabilitiesConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	b, err := json.Marshal(req.CapabilitiesConfig)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to marshal capabilities"})
		return
	}

	DB.Model(&node).Updates(map[string]interface{}{"capabilities_config": string(b), "caps_pending": true})
	invalidateTelemetryCache(node.DeviceID)
	dropStaleBindings(node.GatewayID, node.DeviceID, req.CapabilitiesConfig)

	// Re-send provision to the gateway to apply new capabilities
	if err := SendProvision(node.GatewayID, node.DeviceID, node.APIKey, node.DeviceType, node.Name, req.CapabilitiesConfig); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"status":  "updated_but_offline",
			"message": "Saved. The gateway is offline, so the node gets the new pins as soon as it reconnects.",
		})
		return
	}

	DB.Model(&node).Update("caps_pending", false)
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

// dropStaleBindings removes bindings that point at pins the node no longer has.
func dropStaleBindings(gatewayID string, deviceID uint32, caps []CapabilityConfig) {
	has := map[int]bool{}
	for _, c := range caps {
		has[c.Pin] = true
	}
	var bs []CapabilityBinding
	DB.Where("gateway_id = ? AND (source_device_id = ? OR target_device_id = ?)", gatewayID, deviceID, deviceID).Find(&bs)
	for _, b := range bs {
		if (b.SourceDeviceID == deviceID && !has[int(b.SourcePin)]) || (b.TargetDeviceID == deviceID && !has[int(b.TargetPin)]) {
			DB.Delete(&b)
		}
	}
}
