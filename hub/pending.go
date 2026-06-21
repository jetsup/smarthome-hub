package hub

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ── In-memory pending provision (deferred DB creation until first telemetry) ──

type pendingProvision struct {
	DeviceID           uint32
	GatewayID          string
	APIKey             string
	DeviceType         uint8
	Name               string
	CapabilitiesConfig string
}

var (
	pendingProvisionMu    sync.RWMutex
	pendingProvisions     = make(map[uint32]*pendingProvision)
	pendingDeviceTypes    = make(map[uint32]uint8) // deviceID -> deviceType (from discovery)
)

func addPendingProvision(deviceID uint32, gatewayID, apiKey string, deviceType uint8, name, caps string) {
	pendingProvisionMu.Lock()
	defer pendingProvisionMu.Unlock()
	pendingProvisions[deviceID] = &pendingProvision{
		DeviceID:           deviceID,
		GatewayID:          gatewayID,
		APIKey:             apiKey,
		DeviceType:         deviceType,
		Name:               name,
		CapabilitiesConfig: caps,
	}
}

func consumePendingProvision(deviceID uint32) *pendingProvision {
	pendingProvisionMu.Lock()
	defer pendingProvisionMu.Unlock()
	p := pendingProvisions[deviceID]
	if p != nil {
		delete(pendingProvisions, deviceID)
		delete(pendingDeviceTypes, deviceID)
	}
	return p
}

func addPendingDeviceType(deviceID uint32, deviceType uint8) {
	pendingProvisionMu.Lock()
	defer pendingProvisionMu.Unlock()
	pendingDeviceTypes[deviceID] = deviceType
	// Also update the provision entry if it exists
	if p, ok := pendingProvisions[deviceID]; ok {
		p.DeviceType = deviceType
	}
}

func ListPendingActions(c *gin.Context) {
	userID := c.GetUint("userID")

	var actions []PendingAction
	DB.Joins("JOIN gateways ON gateways.id = pending_actions.gateway_id").
		Where("gateways.user_id = ? AND pending_actions.status = ?", userID, "pending").
		Order("pending_actions.created_at DESC").
		Find(&actions)

	// Enrich with gateway name
	type responseItem struct {
		PendingAction
		GatewayName string `json:"gatewayName"`
	}
	items := make([]responseItem, len(actions))
	for i, a := range actions {
		var gw Gateway
		items[i] = responseItem{PendingAction: a}
		if DB.Select("name").First(&gw, "id = ?", a.GatewayID).Error == nil {
			items[i].GatewayName = gw.Name
		}
	}

	c.JSON(http.StatusOK, items)
}

func DeletePendingAction(c *gin.Context) {
	userID := c.GetUint("userID")
	actionID := c.Param("actionId")

	var action PendingAction
	if err := DB.First(&action, actionID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Pending action not found"})
		return
	}

	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", action.GatewayID, userID).First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}

	DB.Delete(&action)
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// executePendingGatewayDeletion performs the full gateway deletion sequence
// (reset nodes, wait for ACK, clean up DB) for a pending action.
// Returns true if successful.
func executePendingGatewayDeletion(action PendingAction) bool {
	var nodes []Node
	DB.Where("gateway_id = ?", action.TargetID).Find(&nodes)

	for _, n := range nodes {
		ch := expectACK(n.DeviceID)
		if err := SendCommandToGateway(action.GatewayID, n.DeviceID, 2, 99); err != nil {
			signalACK(n.DeviceID)
			continue
		}
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
		}
		delete(NetworkRegistry, n.DeviceID)
		clearPinValues(n.DeviceID)
	}
	removeTCPConn(action.TargetID)

	DB.Where("gateway_id = ?", action.TargetID).Delete(&CapabilityBinding{})
	DB.Where("gateway_id = ?", action.TargetID).Delete(&WifiCredential{})
	DB.Where("gateway_id = ?", action.TargetID).Delete(&Node{})
	DB.Where("id = ?", action.TargetID).Delete(&Gateway{})

	DB.Model(&action).Updates(map[string]interface{}{
		"status": "completed",
	})
	return true
}

func executePendingActions(gatewayID string) {
	var actions []PendingAction
	DB.Where("gateway_id = ? AND status = ?", gatewayID, "pending").Find(&actions)
	for _, a := range actions {
		switch a.Action {
		case "delete_gateway":
			if executePendingGatewayDeletion(a) {
				LogAudit(nil, "pending_action_executed", "gateway", &gatewayID, "Pending deletion executed for gateway "+a.TargetID)
			} else {
				DB.Model(&a).Update("status", "failed")
			}
		}
	}
}
