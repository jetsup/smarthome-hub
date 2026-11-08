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
		return fmt.Errorf("A node supports at most 8 modules")
	}
	used := map[int]bool{}
	singles := map[string]bool{}
	claim := func(pin int, outputCapable bool, what string) error {
		if pin < 0 || pin > 39 {
			return fmt.Errorf("GPIO %d does not exist (use 0-39)", pin)
		}
		if pin >= 6 && pin <= 11 {
			return fmt.Errorf("GPIO %d is used by the flash chip and cannot be assigned", pin)
		}
		if used[pin] {
			return fmt.Errorf("GPIO %d is assigned twice", pin)
		}
		used[pin] = true
		if outputCapable && pin >= 34 {
			return fmt.Errorf("GPIO %d is input-only on the ESP32 and cannot drive %s", pin, what)
		}
		return nil
	}
	for i := range caps {
		c := &caps[i]
		c.Label = strings.TrimSpace(c.Label)
		if len(c.Label) > 10 {
			return fmt.Errorf("Label \"%s\" is longer than 10 characters", c.Label)
		}
		if c.Symbol != "" && !symbolRe.MatchString(c.Symbol) {
			return fmt.Errorf("Invalid symbol \"%s\"", c.Symbol)
		}
		ct := capTypeByKey(c.Type)
		if ct == nil {
			return fmt.Errorf("Unknown module type \"%s\"", c.Type)
		}
		if ct.Singleton {
			if singles[c.Type] {
				return fmt.Errorf("A node can have only one %s", ct.Label)
			}
			singles[c.Type] = true
		}
		switch c.Type {
		case "i2c", "uart":
			continue
		case "digitalOutput", "relay", "irTx":
			if err := claim(c.Pin, true, "an output"); err != nil {
				return err
			}
		case "analogOutput":
			if c.Pin == 0 {
				return fmt.Errorf("GPIO 0 is the boot button and cannot be a PWM output")
			}
			if err := claim(c.Pin, true, "an output"); err != nil {
				return err
			}
		case "analogInput", "digitalInput", "irRx":
			if err := claim(c.Pin, false, ""); err != nil {
				return err
			}
		case "neopixel":
			if c.Count < 1 || c.Count > 1000 {
				return fmt.Errorf("NeoPixel needs a number of LEDs between 1 and 1000")
			}
			if c.Ring && c.Count < 3 {
				return fmt.Errorf("A ring needs at least 3 LEDs")
			}
			if err := claim(c.Pin, true, "NeoPixels"); err != nil {
				return err
			}
		case "buzzer":
			if err := claim(c.Pin, true, "a buzzer"); err != nil {
				return err
			}
		case "dht":
			if c.Extra == 0 {
				c.Extra = 22
			}
			if c.Extra != 11 && c.Extra != 22 {
				return fmt.Errorf("DHT model must be 11 or 22")
			}
			if err := claim(c.Pin, true, "a DHT sensor"); err != nil {
				return err
			}
		case "ld2410":
			if c.Pin2 == 0 {
				c.Pin2 = 17
			}
			if err := claim(c.Pin, false, ""); err != nil {
				return err
			}
			if err := claim(c.Pin2, true, "the LD2410 link"); err != nil {
				return err
			}
		case "ds3231":
			if c.Pin == 0 {
				c.Pin = 21
			}
			if c.Pin2 == 0 {
				c.Pin2 = 22
			}
			if err := claim(c.Pin, true, "I2C SDA"); err != nil {
				return err
			}
			if err := claim(c.Pin2, true, "I2C SCL"); err != nil {
				return err
			}
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
