package hub

import (
	"fmt"
	"strings"
)

// validateCapabilities checks a pin assignment before it is stored or sent to a node.
func validateCapabilities(caps []CapabilityConfig) error {
	if len(caps) == 0 {
		return fmt.Errorf("Add at least one pin")
	}
	if len(caps) > 8 {
		return fmt.Errorf("A node supports at most 8 pins")
	}
	known := map[string]bool{"digitalOutput": true, "analogOutput": true, "analogInput": true, "digitalInput": true, "relay": true, "i2c": true, "uart": true, "irTx": true, "irRx": true}
	used := map[int]bool{}
	for i := range caps {
		c := &caps[i]
		c.Label = strings.TrimSpace(c.Label)
		if len(c.Label) > 10 {
			return fmt.Errorf("Label \"%s\" is longer than 10 characters", c.Label)
		}
		if !known[c.Type] {
			return fmt.Errorf("Unknown pin type \"%s\"", c.Type)
		}
		if c.Type == "i2c" || c.Type == "uart" {
			continue
		}
		if c.Pin < 0 || c.Pin > 39 {
			return fmt.Errorf("GPIO %d does not exist (use 0-39)", c.Pin)
		}
		if c.Pin >= 6 && c.Pin <= 11 {
			return fmt.Errorf("GPIO %d is used by the flash chip and cannot be assigned", c.Pin)
		}
		if used[c.Pin] {
			return fmt.Errorf("GPIO %d is assigned twice", c.Pin)
		}
		used[c.Pin] = true
		isOut := c.Type == "digitalOutput" || c.Type == "analogOutput" || c.Type == "relay" || c.Type == "irTx"
		if isOut && c.Pin >= 34 {
			return fmt.Errorf("GPIO %d is input-only on the ESP32 and cannot be an output", c.Pin)
		}
		if c.Type == "analogOutput" && c.Pin == 0 {
			return fmt.Errorf("GPIO 0 is the boot button and cannot be a PWM output")
		}
	}
	return nil
}

// resendPendingCaps delivers pin assignments that were saved while the gateway was offline.
func resendPendingCaps(gatewayID string) {
	var nodes []Node
	DB.Where("gateway_id = ? AND caps_pending = ?", gatewayID, true).Find(&nodes)
	for _, n := range nodes {
		caps := parseCapabilitiesConfig(n.CapabilitiesConfig)
		if err := SendProvision(n.GatewayID, n.DeviceID, n.APIKey, n.DeviceType, n.Name, caps); err == nil {
			DB.Model(&n).Update("caps_pending", false)
		}
	}
}
