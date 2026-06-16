package hub

// keeps track of known devices dynamically
type DeviceState struct {
	DeviceID uint32 `json:"deviceId"`
	Value    uint16 `json:"value"`
}

// In-memory registry for dynamic device addition
var NetworkRegistry = make(map[uint32]DeviceState)
