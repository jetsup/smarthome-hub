package hub

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// Latest value of every sensor channel, per node: key "cap:channel".
type ReadingValue struct {
	Cap     int       `json:"cap"`
	Channel int       `json:"channel"`
	Key     string    `json:"key"`
	Label   string    `json:"label"`
	Unit    string    `json:"unit,omitempty"`
	Value   float64   `json:"value"` // scaled to real units
	At      time.Time `json:"at"`
}

var (
	readingsMu    sync.Mutex
	readingsStore = map[uint32]map[string]ReadingValue{}
	readingsSaved = map[uint32]time.Time{}
)

func handleVariableFrame(gatewayID string, msgType byte, deviceID uint32, payload []byte) {
	switch msgType {
	case msgReadings:
		rs, err := parseReadings(payload)
		if err != nil {
			log.Printf("readings from device %d: %v", deviceID, err)
			return
		}
		ingestReadings(gatewayID, deviceID, rs)
	case msgIdent:
		if len(payload) >= 6 {
			recordMac(deviceID, fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X", payload[0], payload[1], payload[2], payload[3], payload[4], payload[5]))
		}
	case msgBoot:
		restoreNodeState(gatewayID, deviceID)
	default:
		log.Printf("Unknown frame type %d from device %d via gateway %s", msgType, deviceID, gatewayID)
	}
}

func ingestReadings(gatewayID string, deviceID uint32, rs []reading) {
	var node Node
	if DB.Where("gateway_id = ? AND device_id = ?", gatewayID, deviceID).First(&node).Error != nil {
		return // unknown / not yet activated
	}
	caps := parseCapabilitiesConfig(node.CapabilitiesConfig)
	now := time.Now()
	readingsMu.Lock()
	m := readingsStore[deviceID]
	if m == nil {
		m = map[string]ReadingValue{}
		readingsStore[deviceID] = m
	}
	var fresh []ReadingValue
	for _, r := range rs {
		if r.Cap < 0 || r.Cap >= len(caps) {
			continue
		}
		ct := capTypeByKey(caps[r.Cap].Type)
		if ct == nil {
			continue
		}
		ch := ct.channel(r.Channel)
		if ch == nil {
			continue
		}
		scale := ch.Scale
		if scale == 0 {
			scale = 1
		}
		rv := ReadingValue{Cap: r.Cap, Channel: r.Channel, Key: ch.Key, Label: ch.Label, Unit: ch.Unit,
			Value: float64(r.Value) / scale, At: now}
		m[fmt.Sprintf("%d:%d", r.Cap, r.Channel)] = rv
		fresh = append(fresh, rv)
	}
	save := now.Sub(readingsSaved[deviceID]) > 5*time.Second
	var blob []byte
	if save {
		readingsSaved[deviceID] = now
		blob, _ = json.Marshal(m)
	}
	readingsMu.Unlock()

	DB.Model(&Node{}).Where("id = ?", node.ID).Update("last_seen", now)
	if save {
		DB.Model(&Node{}).Where("id = ?", node.ID).Update("readings", string(blob))
	}
	for _, rv := range fresh {
		if t := caps[rv.Cap].Type; t == "analogInput" || t == "digitalInput" {
			// every input pin has its own value; mirror it into the pin store and bindings
			pin := uint8(caps[rv.Cap].Pin)
			v := uint16(rv.Value)
			setPinValue(deviceID, pin, v)
			processBindings(gatewayID, deviceID, pin, v)
		}
		if caps[rv.Cap].Type == "ds3231" && rv.Key == "time" {
			noteRTC(node, int64(rv.Value))
		}
		evaluateRules(gatewayID, deviceID, rv)
	}
}

// readingsFresh is true when the node reports per-module readings (v2 firmware).
func readingsFresh(deviceID uint32) bool {
	readingsMu.Lock()
	defer readingsMu.Unlock()
	for _, v := range readingsStore[deviceID] {
		if time.Since(v.At) < 10*time.Second {
			return true
		}
	}
	return false
}

// readingsFor returns the latest readings of a node (memory first, then the saved copy).
func readingsFor(node Node) []ReadingValue {
	readingsMu.Lock()
	m := readingsStore[node.DeviceID]
	readingsMu.Unlock()
	if m == nil && node.Readings != "" {
		_ = json.Unmarshal([]byte(node.Readings), &m)
	}
	out := make([]ReadingValue, 0, len(m))
	readingsMu.Lock()
	for _, v := range m {
		out = append(out, v)
	}
	readingsMu.Unlock()
	return out
}
