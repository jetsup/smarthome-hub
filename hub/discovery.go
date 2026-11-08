package hub

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// ── Per-gateway scan ──────────────────────────────────────────────────────────

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

	if err := SendCommandToGateway(gwID, 0, 4, 0); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Gateway is offline"})
		return
	}

	ClearDiscoveredNodes(gwID)

	LogAudit(c, "scan_started", "gateway", &gw.ID, "Node scan started for gateway: "+gw.Name)
	c.JSON(http.StatusOK, gin.H{"status": "scanning"})
}

// ── Per-gateway discovered nodes ──────────────────────────────────────────────

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
		c.JSON(http.StatusOK, []DiscoveredNodeInfo{})
		return
	}

	// Paired nodes answer scans too; the ones already on this gateway are not "new".
	mine := ownNodeNames(gwID)
	result := make([]DiscoveredNodeInfo, 0, len(nodes))
	for devID := range nodes {
		_, paired := discoveryState(devID)
		name, isMine := mine[devID]
		if isMine && paired {
			continue // alive and working: not new
		}
		// A node this gateway has a record of, heard saying "I am not paired",
		// was reset (or re-flashed): offer it again, with its saved setup.
		result = append(result, DiscoveredNodeInfo{
			DeviceID:  devID,
			GatewayID: gwID,
			Paired:    paired,
			Known:     isMine,
			KnownName: name,
			Mac:       macOf(devID),
		})
	}
	c.JSON(http.StatusOK, result)
}

// ── Cross-gateway scan (all gateways) ─────────────────────────────────────────

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

// ── Cross-gateway discovered nodes ────────────────────────────────────────────

func GetAllDiscoveredNodesHandler(c *gin.Context) {
	userID := c.GetUint("userID")

	var gateways []Gateway
	DB.Where("user_id = ?", userID).Find(&gateways)

	userGatewayIDs := make(map[string]bool)
	for _, gw := range gateways {
		userGatewayIDs[gw.ID] = true
	}

	allDiscovered := GetAllDiscoveredNodes()
	filtered := make([]DiscoveredNodeInfo, 0, len(allDiscovered))
	own := map[string]map[uint32]string{}
	for id := range userGatewayIDs {
		own[id] = ownNodeNames(id)
	}
	for _, d := range allDiscovered {
		name, isMine := own[d.GatewayID][d.DeviceID]
		if isMine && d.Paired {
			continue
		}
		d.Known, d.KnownName, d.Mac = isMine, name, macOf(d.DeviceID)
		if userGatewayIDs[d.GatewayID] {
			filtered = append(filtered, d)
		}
	}

	c.JSON(http.StatusOK, filtered)
}

// ownNodeNames: the nodes already paired to a gateway (device id -> saved name).
func ownNodeNames(gwID string) map[uint32]string {
	var rows []Node
	DB.Select("device_id", "name").Where("gateway_id = ?", gwID).Find(&rows)
	m := make(map[uint32]string, len(rows))
	for _, n := range rows {
		m[n.DeviceID] = n.Name
	}
	return m
}
