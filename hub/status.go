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

		// Set gateway offline if last_seen is past timeout
		if gw.IsOnline && !gw.LastSeen.IsZero() && gw.LastSeen.Before(cutoff) {
			log.Printf("Gateway %s last_seen timeout — marking offline", gw.ID)
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
		state := NetworkRegistry[node.DeviceID]
		if node.LastSeen.Before(cutoff) {
			// Node is offline — remove from registry if present
			if _, ok := NetworkRegistry[node.DeviceID]; ok {
				delete(NetworkRegistry, node.DeviceID)
				log.Printf("Node %d (%s) marked offline (last seen %s)", node.DeviceID, node.NodeID, node.LastSeen.Format(time.RFC3339))
			}
		} else {
			// Node is online — update registry
			NetworkRegistry[node.DeviceID] = DeviceState{
				DeviceID: node.DeviceID,
				Value:    state.Value,
			}
		}
	}

	// Purge stale discovered nodes
	PurgeStaleDiscoveredNodes()
}
