package hub

// ── ESP32 GPIO Reference ─────────────────────────────────────────────────────
// See https://lastminuteengineers.com/esp32-pinout-reference

// ValidGPIOs lists all ESP32 GPIOs that are physically broken out and usable
// as general-purpose I/O (excluding flash pins 6-11, power-only pins).
var ValidGPIOs = []int{
	0, 1, 2, 3, 4, 5,
	12, 13, 14, 15, 16, 17, 18, 19,
	21, 22, 23,
	25, 26, 27,
	32, 33, 34, 35, 36, 39,
}

// ADC2Pins conflict with built-in Wi-Fi. When Wi-Fi is enabled, analogRead on
// these pins returns ESP_ERR_TIMEOUT (see technical reference).
var ADC2Pins = []int{0, 2, 4, 12, 13, 14, 15, 25, 26, 27}

// InputOnlyPins are usable as digital/analog inputs only (no pullup/pulldown).
var InputOnlyPins = []int{34, 35, 36, 39}

var validPinSet = func() map[int]bool {
	s := make(map[int]bool, len(ValidGPIOs))
	for _, p := range ValidGPIOs {
		s[p] = true
	}
	return s
}()

var adc2Set = func() map[int]bool {
	s := make(map[int]bool, len(ADC2Pins))
	for _, p := range ADC2Pins {
		s[p] = true
	}
	return s
}()

// IsValidGPIO returns true if pin is a valid ESP32 GPIO that is broken out.
func IsValidGPIO(pin int) bool {
	return validPinSet[pin]
}

// IsADC2Pin returns true if pin is on ADC2 (conflicts with Wi-Fi).
func IsADC2Pin(pin int) bool {
	return adc2Set[pin]
}

// IsInputOnlyPin returns true if pin has no internal pull resistors.
func IsInputOnlyPin(pin int) bool {
	for _, p := range InputOnlyPins {
		if p == pin {
			return true
		}
	}
	return false
}

// ValidateBindingPin checks whether a pin is suitable for use in a capability
// binding. Returns an error string or empty string.
func ValidateBindingPin(pin int, capType string) string {
	if pin == 0 {
		return ""
	}
	if !IsValidGPIO(pin) {
		return "Not a valid ESP32 GPIO pin (see pinout reference)"
	}
	if IsADC2Pin(pin) && (capType == "analogInput" || capType == "analogOutput") {
		return "ADC2 pin conflicts with Wi-Fi — use ADC1 (GPIO 32-39) for analog input"
	}
	if IsInputOnlyPin(pin) && (capType == "digitalOutput" || capType == "analogOutput" || capType == "relay") {
		return "Input-only pin (GPIO 34-39) cannot be used as output"
	}
	return ""
}

// DefaultPinForType returns a safe default GPIO pin for a given capability type.
func DefaultPinForType(capType string) int {
	switch capType {
	case "digitalOutput":
		return 2
	case "analogOutput":
		return 26
	case "analogInput":
		return 34
	case "digitalInput":
		return 35
	case "relay":
		return 32
	default:
		return 0
	}
}
