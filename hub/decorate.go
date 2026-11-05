package hub

import (
	"fmt"
	"time"
)

// decorateCaps returns a node's modules with live values and a short state text.
func decorateCaps(node Node) []CapabilityConfig {
	caps := parseCapabilitiesConfig(node.CapabilitiesConfig)
	if caps == nil {
		return nil
	}
	pv := getPinValues(node.DeviceID)
	rs := readingsFor(node)
	effects := effectsFor(node)
	val := func(cap int, ch string) (ReadingValue, bool) {
		for _, r := range rs {
			if r.Cap == cap && r.Key == ch {
				return r, true
			}
		}
		return ReadingValue{}, false
	}
	for i, c := range caps {
		if v, ok := pv[uint8(c.Pin)]; ok && (c.Type == "analogInput" || c.Type == "digitalInput" || c.Type == "digitalOutput" || c.Type == "analogOutput" || c.Type == "relay") {
			v := v
			caps[i].Value = &v
		}
		switch c.Type {
		case "dht":
			t, ok1 := val(i, "temperature")
			h, ok2 := val(i, "humidity")
			if ok1 && ok2 {
				caps[i].Display = fmt.Sprintf("%.1f°C  %.0f%%", t.Value, h.Value)
			} else if ok1 {
				caps[i].Display = fmt.Sprintf("%.1f°C", t.Value)
			}
		case "ld2410":
			if p, ok := val(i, "presence"); ok {
				switch int(p.Value) {
				case 0:
					caps[i].Display = "Clear"
				default:
					d, _ := val(i, "detectionDistance")
					state := map[int]string{1: "Moving", 2: "Still", 3: "Moving"}[int(p.Value)]
					caps[i].Display = fmt.Sprintf("%s %.0f cm", state, d.Value)
				}
			}
		case "ds3231":
			if t, ok := val(i, "time"); ok {
				caps[i].Display = time.Unix(int64(t.Value), 0).UTC().Format("15:04:05") + " UTC"
			}
		case "neopixel":
			caps[i].Display = "Off"
			if e, ok := effects[fmt.Sprint(i)]; ok {
				p := num(e["pattern"], 0)
				for _, ch := range neoPatterns {
					if ch.Value == p {
						caps[i].Display = ch.Label
					}
				}
			}
		case "buzzer":
			caps[i].Display = "Ready"
		}
	}
	return caps
}
