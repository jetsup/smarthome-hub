package hub

import (
	"log"
	"time"
)

// StartOnlineMonitor runs a background goroutine that periodically updates
// gateway and node online status.
func StartOnlineMonitor() {
	ticker := time.NewTicker(15 * time.Second)
	go func() {
		for range ticker.C {
			updateGatewayStatus()
			updateNodeStatus()
		}
	}()
	log.Println("Online status monitor started (15s interval)")
}

func updateGatewayStatus() {
	var gateways []Gateway
	DB.Find(&gateways)

	cutoff := time.Now().Add(-GatewayOfflineTimeout)
	for _, gw := range gateways {
		conn := getTCPConn(gw.ID)
		if conn == nil && gw.IsOnline {
			DB.Model(&gw).Update("is_online", false)
			log.Printf("Gateway %s marked offline (no TCP connection)", gw.ID)
		} else if conn != nil && !gw.IsOnline {
			DB.Model(&gw).Update("is_online", true)
			log.Printf("Gateway %s marked online (TCP reconnected)", gw.ID)
		}

		// A live TCP connection is the source of truth: the gateway is quiet when no
		// node is talking, so a stale last_seen must not mark it offline. Dead peers
		// are detected by TCP keepalive (see StartTCPWorker) which closes the conn.
		if conn == nil && gw.IsOnline && !gw.LastSeen.IsZero() && gw.LastSeen.Before(cutoff) {
			DB.Model(&gw).Update("is_online", false)
		}
	}
}

func updateNodeStatus() {
	var nodes []Node
	DB.Find(&nodes)

	cutoff := time.Now().Add(-NodeOfflineTimeout)
	for _, node := range nodes {
		if node.LastSeen.IsZero() {
			continue
		}

		// Check if the node's gateway is online
		var gw Gateway
		gwOnline := false
		if DB.First(&gw, "id = ?", node.GatewayID).Error == nil {
			gwOnline = gw.IsOnline
		}

		state, _ := registryGet(node.DeviceID)
		if node.LastSeen.Before(cutoff) || !gwOnline {
			// Node is offline — remove from registry if present
			if registryDelete(node.DeviceID) {
				if !gwOnline {
					log.Printf("Node %d (%s) marked offline (gateway %s is offline)", node.DeviceID, node.NodeID, node.GatewayID)
				} else {
					log.Printf("Node %d (%s) marked offline (last seen %s)", node.DeviceID, node.NodeID, node.LastSeen.Format(time.RFC3339))
				}
			}
		} else {
			// Node is online — update registry
			registrySet(DeviceState{DeviceID: node.DeviceID, Value: state.Value})
		}
	}

	// Purge stale discovered nodes
	PurgeStaleDiscoveredNodes()
}

// UserRegistry returns the real-time device states for nodes owned by the user.
func UserRegistry(userID uint) map[uint32]DeviceState {
	var nodes []Node
	DB.Joins("JOIN gateways ON gateways.id = nodes.gateway_id").
		Where("gateways.user_id = ?", userID).Find(&nodes)
	snap := RegistrySnapshot()
	out := make(map[uint32]DeviceState)
	for _, n := range nodes {
		if s, ok := snap[n.DeviceID]; ok {
			out[n.DeviceID] = s
		}
	}
	return out
}

// UserOwnedDeviceGateway returns the gateway id of the user's node with the device id.
func UserOwnedDeviceGateway(userID uint, deviceID uint32) (string, bool) {
	var node Node
	if err := DB.Joins("JOIN gateways ON gateways.id = nodes.gateway_id").
		Where("nodes.device_id = ? AND gateways.user_id = ?", deviceID, userID).
		First(&node).Error; err != nil {
		return "", false
	}
	return node.GatewayID, true
}
