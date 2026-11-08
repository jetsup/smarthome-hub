package hub

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

func userNode(c *gin.Context) (Node, bool) {
	var node Node
	if err := DB.Joins("JOIN gateways ON gateways.id = nodes.gateway_id").
		Where("nodes.node_id = ? AND gateways.user_id = ?", c.Param("nodeId"), c.GetUint("userID")).
		First(&node).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return node, false
	}
	return node, true
}

// PUT /api/nodes/:nodeId  { "name": "Kitchen light" }
// Edits a node's settings in place - no re-pairing. More settings can be added here later.
func UpdateNode(c *gin.Context) {
	node, ok := userNode(c)
	if !ok {
		return
	}
	var req struct {
		Name *string `json:"name"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	changes := map[string]interface{}{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || utf8.RuneCountInString(name) > 40 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "The name must be 1 to 40 characters"})
			return
		}
		if name != node.Name {
			changes["name"] = name
		}
	}
	if len(changes) > 0 {
		DB.Model(&node).Updates(changes)
		LogAudit(c, "node_updated", "node", &node.NodeID, fmt.Sprintf("Node %s renamed from %q to %q", node.NodeID, node.Name, changes["name"]))
		DB.First(&node, node.ID)
	}
	c.JSON(http.StatusOK, nodeDetailResponse(node))
}

// POST /api/nodes/:nodeId/restart - reboots the node (it keeps its pairing and settings).
func RestartNode(c *gin.Context) {
	node, ok := userNode(c)
	if !ok {
		return
	}
	if err := SendCommandToGateway(node.GatewayID, node.DeviceID, 2, RestartCommandValue); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "The gateway is offline, so the node can't be restarted"})
		return
	}
	LogAudit(c, "node_restarted", "node", &node.NodeID, fmt.Sprintf("Restart sent to node %s (%s)", node.NodeID, node.Name))
	c.JSON(http.StatusOK, gin.H{"status": "restarting", "nodeId": node.NodeID})
}

// POST /api/gateways/:id/restart-nodes - reboots every node on a gateway, one after another.
func RestartGatewayNodes(c *gin.Context) {
	var gw Gateway
	if err := DB.First(&gw, "id = ? AND user_id = ?", c.Param("id"), c.GetUint("userID")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}
	if getTCPConn(gw.ID) == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "The gateway is offline"})
		return
	}
	var nodes []Node
	DB.Where("gateway_id = ?", gw.ID).Find(&nodes)
	ids := make([]uint32, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.DeviceID)
	}
	// Spaced out so the radio isn't flooded and the ESP-NOW frames aren't lost.
	go func() {
		for _, id := range ids {
			_ = SendCommandToGateway(gw.ID, id, 2, RestartCommandValue)
			time.Sleep(150 * time.Millisecond)
		}
	}()
	LogAudit(c, "gateway_nodes_restarted", "gateway", &gw.ID, fmt.Sprintf("Restart sent to %d node(s) on %s", len(ids), gw.Name))
	c.JSON(http.StatusOK, gin.H{"status": "restarting", "count": len(ids)})
}
