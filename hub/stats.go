package hub

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type DashboardStats struct {
	Gateways       int            `json:"gateways"`
	Nodes          int            `json:"nodes"`
	OnlineGW       int            `json:"onlineGateways"`
	OnlineNodes    int            `json:"onlineNodes"`
	GatewayList    []GatewayBrief `json:"gatewayList"`
	OnlineNodeList []NodeBrief    `json:"onlineNodeList"`
}

type GatewayBrief struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Online    bool   `json:"online"`
	NodeCount int    `json:"nodeCount"`
}

type NodeBrief struct {
	NodeID    string `json:"nodeId"`
	DeviceID  uint32 `json:"deviceId"`
	GatewayID string `json:"gatewayId"`
	Value     uint16 `json:"value"`
	Online    bool   `json:"online"`
}

func GetDashboardStats(c *gin.Context) {
	userID := c.GetUint("userID")

	var stats DashboardStats

	var gateways []Gateway
	DB.Where("user_id = ?", userID).Find(&gateways)
	stats.Gateways = len(gateways)

	for _, gw := range gateways {
		if gw.IsOnline {
			stats.OnlineGW++
		}
	}

	gwIDs := make([]string, len(gateways))
	for i, gw := range gateways {
		gwIDs[i] = gw.ID
	}

	if len(gwIDs) == 0 {
		c.JSON(http.StatusOK, stats)
		return
	}

	// Count all activated nodes (connected_at IS NOT NULL)
	var totalNodes int64
	DB.Model(&Node{}).
		Where("gateway_id IN ? AND connected_at IS NOT NULL", gwIDs).
		Count(&totalNodes)
	stats.Nodes = int(totalNodes)

	// Count online nodes
	cutoff := time.Now().Add(-NodeOfflineTimeout)
	var onlineNodes int64
	DB.Model(&Node{}).
		Where("gateway_id IN ? AND connected_at IS NOT NULL AND last_seen > ?", gwIDs, cutoff).
		Count(&onlineNodes)
	stats.OnlineNodes = int(onlineNodes)

	// Gateway brief list
	for _, gw := range gateways {
		var cnt int64
		DB.Model(&Node{}).
			Where("gateway_id = ? AND connected_at IS NOT NULL", gw.ID).
			Count(&cnt)
		stats.GatewayList = append(stats.GatewayList, GatewayBrief{
			ID:        gw.ID,
			Name:      gw.Name,
			Online:    gw.IsOnline,
			NodeCount: int(cnt),
		})
	}

	// Online node list
	var nodes []Node
	DB.Where("gateway_id IN ? AND connected_at IS NOT NULL AND last_seen > ?", gwIDs, cutoff).
		Order("last_seen DESC").
		Limit(50).
		Find(&nodes)

	for _, n := range nodes {
		stats.OnlineNodeList = append(stats.OnlineNodeList, NodeBrief{
			NodeID:    n.NodeID,
			DeviceID:  n.DeviceID,
			GatewayID: n.GatewayID,
			Value:     n.LastValue,
			Online:    true,
		})
	}

	c.JSON(http.StatusOK, stats)
}
