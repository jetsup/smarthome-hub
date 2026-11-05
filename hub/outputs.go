package hub

import (
	"encoding/json"
	"log"
	"strconv"
	"sync"
	"time"
)

// The hub remembers the last value it commanded on every output pin. A node that restarts
// (power cut, module edit...) comes back with all outputs off, so when it announces its
// boot the hub puts every output back the way it was.

type outKey struct {
	gw  string
	dev uint32
}

var (
	outMu    sync.Mutex
	outVals  = map[outKey]map[uint8]uint16{}
	outDirty = map[outKey]bool{}
	bootSeen = map[uint32]time.Time{}
)

func rememberOutput(gatewayID string, deviceID uint32, pin uint8, value uint16) {
	k := outKey{gatewayID, deviceID}
	outMu.Lock()
	m := outVals[k]
	if m == nil {
		m = loadOutputsLocked(k)
		outVals[k] = m
	}
	m[pin] = value
	outDirty[k] = true
	outMu.Unlock()
}

// loadOutputsLocked reads the saved map from the database (caller holds outMu).
func loadOutputsLocked(k outKey) map[uint8]uint16 {
	m := map[uint8]uint16{}
	var n Node
	if DB.Select("outputs").Where("gateway_id = ? AND device_id = ?", k.gw, k.dev).First(&n).Error == nil && n.Outputs != "" {
		raw := map[string]uint16{}
		if json.Unmarshal([]byte(n.Outputs), &raw) == nil {
			for ks, v := range raw {
				if p, err := strconv.Atoi(ks); err == nil {
					m[uint8(p)] = v
				}
			}
		}
	}
	return m
}

func savedOutputs(k outKey) map[uint8]uint16 {
	outMu.Lock()
	defer outMu.Unlock()
	m := outVals[k]
	if m == nil {
		m = loadOutputsLocked(k)
		outVals[k] = m
	}
	cp := make(map[uint8]uint16, len(m))
	for p, v := range m {
		cp[p] = v
	}
	return cp
}

// StartOutputFlusher writes changed output maps to the database about once a second.
func StartOutputFlusher() {
	go func() {
		for range time.Tick(time.Second) {
			outMu.Lock()
			todo := map[outKey]map[string]uint16{}
			for k := range outDirty {
				raw := map[string]uint16{}
				for p, v := range outVals[k] {
					raw[strconv.Itoa(int(p))] = v
				}
				todo[k] = raw
			}
			outDirty = map[outKey]bool{}
			outMu.Unlock()
			for k, raw := range todo {
				b, _ := json.Marshal(raw)
				DB.Model(&Node{}).Where("gateway_id = ? AND device_id = ?", k.gw, k.dev).Update("outputs", string(b))
			}
		}
	}()
}

// restoreNodeState re-sends the remembered outputs and effects to a node that just booted.
func restoreNodeState(gatewayID string, deviceID uint32) {
	outMu.Lock()
	if t, ok := bootSeen[deviceID]; ok && time.Since(t) < 5*time.Second {
		outMu.Unlock()
		return // the node repeats its boot frame a few times
	}
	bootSeen[deviceID] = time.Now()
	outMu.Unlock()

	var node Node
	if DB.Where("gateway_id = ? AND device_id = ?", gatewayID, deviceID).First(&node).Error != nil {
		return
	}
	caps := parseCapabilitiesConfig(node.CapabilitiesConfig)
	saved := savedOutputs(outKey{gatewayID, deviceID})
	effects := effectsFor(node)
	restored := 0
	for i, c := range caps {
		switch c.Type {
		case "digitalOutput", "relay", "analogOutput":
			if v, ok := saved[uint8(c.Pin)]; ok && v != 0 {
				setPinValue(deviceID, uint8(c.Pin), v)
				_ = SendPinCommandToGateway(gatewayID, deviceID, uint8(c.Pin), v)
				restored++
			}
		case "neopixel":
			if e, ok := effects[strconv.Itoa(i)]; ok {
				if _, _, err := runAction(node, ActionRequest{Cap: i, Action: "setEffect", Params: e}); err == nil {
					restored++
				}
			}
		}
	}
	if restored > 0 {
		log.Printf("node %d restarted: restored %d output(s)", deviceID, restored)
	}
}
