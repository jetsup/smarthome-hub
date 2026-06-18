package hub

import "sync"

// PendingProvision tracks a provision that has been sent to a node
// but not yet acknowledged via telemetry.
type PendingProvision struct {
	DeviceID  uint32
	GatewayID string
	APIKey    string
}

var (
	pendingProvisions = make(map[uint32]PendingProvision)
	pendingMu         sync.RWMutex
)

func addPendingProvision(deviceID uint32, gatewayID, apiKey string) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	pendingProvisions[deviceID] = PendingProvision{
		DeviceID:  deviceID,
		GatewayID: gatewayID,
		APIKey:    apiKey,
	}
}

// consumePendingProvision removes and returns the pending provision for a device.
// Returns nil if no pending provision exists.
func consumePendingProvision(deviceID uint32) *PendingProvision {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	p, ok := pendingProvisions[deviceID]
	if !ok {
		return nil
	}
	delete(pendingProvisions, deviceID)
	return &p
}
