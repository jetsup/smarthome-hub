package hub

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// DetachedNode is a node whose gateway was deleted. The node itself is left
// exactly as it was (still holding its key, modules and pairing code); the hub
// keeps its saved setup here so it can be assigned to another gateway later.
type DetachedNode struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	UserID             uint      `gorm:"index;not null" json:"-"`
	DeviceID           uint32    `gorm:"uniqueIndex;not null" json:"deviceId"`
	NodeID             string    `gorm:"size:16" json:"nodeId"`
	Name               string    `gorm:"size:64" json:"name"`
	DeviceType         uint8     `json:"deviceType"`
	CapabilitiesConfig string    `gorm:"type:text" json:"-"`
	PairCode           string    `gorm:"size:12" json:"pairCode"`
	Mac                string    `gorm:"size:17" json:"mac"`
	Effects            string    `gorm:"type:text" json:"-"`
	Outputs            string    `gorm:"type:text" json:"-"`
	FromGateway        string    `gorm:"size:255" json:"fromGateway"`
	DetachedAt         time.Time `json:"detachedAt"`
}

// detachGateway removes a gateway and everything that belongs to it, but keeps
// its nodes: they are not reset, only remembered as unassigned.
//
// A node that is online right now is unpaired instead (its memory is cleared),
// so it is free to be discovered and paired by any gateway nearby. Nodes that
// cannot be reached are kept so they are not lost.
func detachGateway(gw Gateway) (kept, unpaired int) {
	var nodes []Node
	DB.Where("gateway_id = ?", gw.ID).Find(&nodes)
	live := getTCPConn(gw.ID) != nil
	for _, n := range nodes {
		if live && isNodeOnline(n.LastSeen) {
			if err := SendCommandToGateway(gw.ID, n.DeviceID, 2, ResetCommandValue); err == nil {
				unpaired++
				registryDelete(n.DeviceID)
				clearPinValues(n.DeviceID)
				invalidateTelemetryCache(n.DeviceID)
				DB.Where("device_id = ?", n.DeviceID).Delete(&DetachedNode{})
				time.Sleep(150 * time.Millisecond) // frames are repeated; keep the radio free
				continue
			}
		}
		kept++
		code := ensureNodeCode(&n)
		d := DetachedNode{
			UserID: gw.UserID, DeviceID: n.DeviceID, NodeID: n.NodeID, Name: n.Name, DeviceType: n.DeviceType,
			CapabilitiesConfig: n.CapabilitiesConfig, PairCode: code, Mac: n.Mac,
			Effects: n.Effects, Outputs: n.Outputs, FromGateway: gw.Name, DetachedAt: time.Now(),
		}
		DB.Where("device_id = ?", n.DeviceID).Delete(&DetachedNode{})
		DB.Create(&d)
		registryDelete(n.DeviceID)
		clearPinValues(n.DeviceID)
		invalidateTelemetryCache(n.DeviceID)
	}
	removeTCPConn(gw.ID)
	DB.Where("gateway_id = ?", gw.ID).Delete(&CapabilityBinding{})
	DB.Where("gateway_id = ?", gw.ID).Delete(&Rule{})
	DB.Where("gateway_id = ?", gw.ID).Delete(&WifiCredential{})
	DB.Where("gateway_id = ?", gw.ID).Delete(&PendingAction{})
	deleteHmisOfGateway(gw.ID)
	DB.Where("gateway_id = ?", gw.ID).Delete(&Node{})
	DB.Delete(&Gateway{}, "id = ?", gw.ID)
	return kept, unpaired
}

// GET /api/nodes/unassigned
func ListUnassignedNodes(c *gin.Context) {
	var rows []DetachedNode
	DB.Where("user_id = ?", c.GetUint("userID")).Order("detached_at DESC").Find(&rows)
	out := make([]gin.H, 0, len(rows))
	for _, d := range rows {
		out = append(out, gin.H{
			"id": d.ID, "deviceId": d.DeviceID, "nodeId": d.NodeID, "name": d.Name, "mac": d.Mac,
			"pairCode": d.PairCode, "fromGateway": d.FromGateway, "detachedAt": d.DetachedAt,
			"modules": len(parseCapabilitiesConfig(d.CapabilitiesConfig)),
		})
	}
	c.JSON(http.StatusOK, out)
}

func userDetached(c *gin.Context) (DetachedNode, bool) {
	var d DetachedNode
	id, _ := strconv.Atoi(c.Param("id"))
	if err := DB.Where("id = ? AND user_id = ?", id, c.GetUint("userID")).First(&d).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return d, false
	}
	return d, true
}

// POST /api/nodes/unassigned/:id/assign {gatewayId}
// Sends the saved setup (and the node's own pairing code) to the node through
// the chosen gateway; the node appears there as soon as it reports in.
func AssignUnassignedNode(c *gin.Context) {
	d, ok := userDetached(c)
	if !ok {
		return
	}
	var req struct {
		GatewayID string `json:"gatewayId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Choose a gateway"})
		return
	}
	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", req.GatewayID, c.GetUint("userID")).First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}
	var taken int64
	DB.Model(&Node{}).Where("device_id = ?", d.DeviceID).Count(&taken)
	if taken > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "This node is already paired to a gateway"})
		return
	}
	apiKey := generateNodeAPIKey()
	caps := parseCapabilitiesConfig(d.CapabilitiesConfig)
	RemoveDiscoveredNode(gw.ID, d.DeviceID)
	if err := SendProvisionCode(gw.ID, d.DeviceID, apiKey, d.DeviceType, d.Name, caps, d.PairCode); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "That gateway is offline"})
		return
	}
	// AdoptFrom = its own id: on activation the saved light/sound state is restored
	// and this record is removed.
	addPendingProvision(d.DeviceID, gw.ID, apiKey, d.DeviceType, d.Name, d.CapabilitiesConfig, d.PairCode, d.DeviceID)
	LogAudit(c, "node_assigned", "gateway", &gw.ID, fmt.Sprintf("Node %d (%s) is being assigned to gateway %s", d.DeviceID, d.Name, gw.Name))
	c.JSON(http.StatusOK, gin.H{"status": "provisioning", "message": "Sent. The node appears on the gateway once it reports in."})
}

// DELETE /api/nodes/unassigned/:id - forget the saved setup (the node keeps its own).
func ForgetUnassignedNode(c *gin.Context) {
	d, ok := userDetached(c)
	if !ok {
		return
	}
	DB.Delete(&d)
	LogAudit(c, "unassigned_node_forgotten", "node", &d.NodeID, fmt.Sprintf("Saved setup of node %d (%s) removed", d.DeviceID, d.Name))
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}
