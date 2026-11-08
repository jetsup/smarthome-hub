package hub

// The catalog is the single description of every kind of pin, sensor and actuator a
// node can carry. Adding a new module means adding one entry here (plus its driver in
// the node firmware): validation, the wire format, the web UI and the app UI all read
// this table, so none of them need to change.

type CapField struct {
	Key     string `json:"key"` // pin | pin2 | count | ring | model | extra
	Label   string `json:"label"`
	Default int    `json:"default"`
	Min     int    `json:"min,omitempty"`
	Max     int    `json:"max,omitempty"`
	Options []int  `json:"options,omitempty"` // for model-like choices
}

type CapChannel struct {
	ID    int     `json:"id"`
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Unit  string  `json:"unit,omitempty"`
	Scale float64 `json:"scale"` // reported integer / scale = real value
	// Dash says how the monitoring dashboard shows this channel: "gauge" (a number),
	// "bool" (an on/off indicator), "text" (a clock...). Empty = not shown.
	Dash string  `json:"dash,omitempty"`
	Min  float64 `json:"min,omitempty"`
	Max  float64 `json:"max,omitempty"`
	Icon string  `json:"icon,omitempty"` // default symbol
}

type ActionParam struct {
	Key     string      `json:"key"`
	Label   string      `json:"label"`
	Kind    string      `json:"kind"` // pattern | color | range | bool | choice | number
	Min     int         `json:"min,omitempty"`
	Max     int         `json:"max,omitempty"`
	Default interface{} `json:"default,omitempty"`
	Choices []Choice    `json:"choices,omitempty"`
	// Colour params only: a "random colour" choice exists, except for the listed patterns.
	AllowRandom bool  `json:"allowRandom,omitempty"`
	NoRandom    []int `json:"noRandom,omitempty"`
}

type Choice struct {
	Value int    `json:"value"`
	Label string `json:"label"`
	Ring  bool   `json:"ring,omitempty"`
}

type CapAction struct {
	ID     int           `json:"id"`
	Key    string        `json:"key"`
	Label  string        `json:"label"`
	Params []ActionParam `json:"params,omitempty"`
}

type CapType struct {
	Key       string       `json:"key"`
	Code      int          `json:"code"` // wire value (matches the node firmware)
	Label     string       `json:"label"`
	Kind      string       `json:"kind"` // io | sensor | actuator
	Hint      string       `json:"hint,omitempty"`
	Fields    []CapField   `json:"fields"`
	Channels  []CapChannel `json:"channels,omitempty"`
	Actions   []CapAction  `json:"actions,omitempty"`
	Singleton bool         `json:"singleton,omitempty"` // at most one per node (owns a bus)
}

var neoPatterns = []Choice{
	{0, "Off", false}, {1, "Solid colour", false}, {2, "Blink", false}, {3, "Breathe", false},
	{4, "Rainbow", false}, {5, "Rainbow cycle", false}, {6, "Chase", false}, {7, "Theater chase", false},
	{8, "Comet", false}, {9, "Scanner (Knight Rider)", false}, {10, "Twinkle", false}, {11, "Fire", false},
	{12, "Colour wipe", false}, {13, "Gradient pulse", false},
	{20, "Spinner", true}, {21, "Dual spinner", true}, {22, "Ring comet", true}, {23, "Ring pulse", true},
	{24, "Rainbow spin", true}, {25, "Loader fill", true}, {26, "Wave", true}, {27, "Clock sweep", true},
}

// Patterns that already bring their own colours (or only use black/white), so
// a random colour makes no sense: Off, Solid colour, Rainbow, Rainbow cycle,
// Fire, Gradient pulse, Rainbow spin.
var neoNoRandom = []int{0, 1, 4, 5, 11, 13, 24}

func neoRandomOK(pattern int) bool {
	for _, p := range neoNoRandom {
		if p == pattern {
			return false
		}
	}
	return true
}

var buzzPatterns = []Choice{
	{0, "Stop", false}, {1, "Beep", false}, {2, "Double beep", false}, {3, "Triple beep", false},
	{4, "Alarm", false}, {5, "Siren", false}, {6, "Chime", false}, {7, "Doorbell", false},
}

var capCatalog = []CapType{
	{Key: "digitalOutput", Code: 3, Label: "Digital output", Kind: "io", Hint: "On/off: LED, relay board input…",
		Fields: []CapField{{Key: "pin", Label: "GPIO", Default: 2, Max: 33}}},
	{Key: "analogOutput", Code: 1, Label: "Analog output (PWM)", Kind: "io", Hint: "Dimming, motor speed…",
		Fields: []CapField{{Key: "pin", Label: "GPIO", Default: 26, Max: 33}}},
	{Key: "relay", Code: 4, Label: "Relay", Kind: "io",
		Fields: []CapField{{Key: "pin", Label: "GPIO", Default: 32, Max: 33}}},
	{Key: "analogInput", Code: 0, Label: "Analog input", Kind: "io", Hint: "Potentiometer, light sensor…",
		Fields:   []CapField{{Key: "pin", Label: "GPIO", Default: 34, Max: 39}},
		Channels: []CapChannel{{ID: 0, Key: "value", Label: "Value", Scale: 1, Dash: "gauge", Min: 0, Max: 4095, Icon: "gauge"}}},
	{Key: "digitalInput", Code: 2, Label: "Digital input", Kind: "io", Hint: "Button, switch, PIR…",
		Fields:   []CapField{{Key: "pin", Label: "GPIO", Default: 35, Max: 39}},
		Channels: []CapChannel{{ID: 0, Key: "state", Label: "State", Scale: 1, Dash: "bool", Icon: "input"}}},
	{Key: "neopixel", Code: 9, Label: "NeoPixel LEDs", Kind: "actuator", Hint: "WS2812 strip or ring",
		Fields: []CapField{
			{Key: "pin", Label: "Data GPIO", Default: 27, Max: 33},
			{Key: "count", Label: "Number of LEDs", Default: 8, Min: 1, Max: 1000},
			{Key: "ring", Label: "Arranged in a ring", Default: 0, Max: 1},
		},
		Actions: []CapAction{{ID: 1, Key: "setEffect", Label: "Set pattern", Params: []ActionParam{
			{Key: "pattern", Label: "Pattern", Kind: "pattern", Default: 1, Choices: neoPatterns},
			{Key: "color", Label: "Colour", Kind: "color", Default: "#00A8E8", AllowRandom: true, NoRandom: neoNoRandom},
			{Key: "speed", Label: "Speed", Kind: "range", Min: 1, Max: 100, Default: 50},
			{Key: "brightness", Label: "Brightness", Kind: "range", Min: 1, Max: 100, Default: 60},
			{Key: "reverse", Label: "Reverse direction", Kind: "bool", Default: false},
		}}}},
	{Key: "buzzer", Code: 10, Label: "Buzzer", Kind: "actuator", Hint: "Active or passive piezo",
		Fields: []CapField{{Key: "pin", Label: "GPIO", Default: 33, Max: 33}},
		Actions: []CapAction{{ID: 1, Key: "play", Label: "Play sound", Params: []ActionParam{
			{Key: "pattern", Label: "Sound", Kind: "pattern", Default: 1, Choices: buzzPatterns},
			{Key: "repeat", Label: "Repeat (0 = until stopped)", Kind: "number", Min: 0, Max: 20, Default: 1},
		}}}},
	{Key: "dht", Code: 11, Label: "DHT temperature / humidity", Kind: "sensor", Hint: "DHT11 or DHT22 (AM2302)",
		Fields: []CapField{
			{Key: "pin", Label: "Data GPIO", Default: 4, Max: 33},
			{Key: "extra", Label: "Model", Default: 22, Options: []int{11, 22}},
		},
		Channels: []CapChannel{
			{ID: 0, Key: "temperature", Label: "Temperature", Unit: "°C", Scale: 100, Dash: "gauge", Min: -10, Max: 50, Icon: "thermometer"},
			{ID: 1, Key: "humidity", Label: "Humidity", Unit: "%", Scale: 100, Dash: "gauge", Min: 0, Max: 100, Icon: "droplet"},
		}},
	{Key: "ld2410", Code: 12, Label: "LD2410 presence radar", Kind: "sensor", Singleton: true, Hint: "mmWave presence; uses UART2",
		Fields: []CapField{
			{Key: "pin", Label: "Node RX GPIO (sensor TX)", Default: 16, Max: 39},
			{Key: "pin2", Label: "Node TX GPIO (sensor RX)", Default: 17, Max: 33},
		},
		Channels: []CapChannel{
			{ID: 0, Key: "presence", Label: "Presence", Scale: 1, Dash: "bool", Icon: "radar"},
			{ID: 1, Key: "movingDistance", Label: "Moving distance", Unit: "cm", Scale: 1},
			{ID: 2, Key: "movingEnergy", Label: "Moving energy", Scale: 1},
			{ID: 3, Key: "stillDistance", Label: "Still distance", Unit: "cm", Scale: 1},
			{ID: 4, Key: "stillEnergy", Label: "Still energy", Scale: 1},
			{ID: 5, Key: "detectionDistance", Label: "Detection distance", Unit: "cm", Scale: 1, Dash: "gauge", Min: 0, Max: 600, Icon: "ruler"},
		}},
	{Key: "ds3231", Code: 13, Label: "DS3231 real-time clock", Kind: "sensor", Singleton: true,
		Hint: "Supplies the time to the whole system; uses I2C",
		Fields: []CapField{
			{Key: "pin", Label: "SDA GPIO", Default: 21, Max: 33},
			{Key: "pin2", Label: "SCL GPIO", Default: 22, Max: 33},
		},
		Channels: []CapChannel{
			{ID: 0, Key: "time", Label: "Clock", Unit: "s", Scale: 1, Dash: "text", Icon: "clock"},
			{ID: 1, Key: "temperature", Label: "Chip temperature", Unit: "°C", Scale: 100, Dash: "gauge", Min: 0, Max: 60, Icon: "thermometer"},
			{ID: 2, Key: "valid", Label: "Clock valid (0 = battery was lost)", Scale: 1},
		},
		Actions: []CapAction{{ID: 2, Key: "syncTime", Label: "Set clock from the hub"}}},
	{Key: "i2c", Code: 7, Label: "I2C (raw)", Kind: "io", Fields: []CapField{{Key: "extra", Label: "Address", Default: 0}}},
	{Key: "uart", Code: 8, Label: "UART (raw)", Kind: "io", Fields: []CapField{{Key: "extra", Label: "UART no.", Default: 1}}},
	{Key: "irTx", Code: 5, Label: "IR transmitter", Kind: "io", Fields: []CapField{{Key: "pin", Label: "GPIO", Default: 26, Max: 33}}},
	{Key: "irRx", Code: 6, Label: "IR receiver", Kind: "io", Fields: []CapField{{Key: "pin", Label: "GPIO", Default: 27, Max: 39}}},
}

func capTypeByKey(key string) *CapType {
	for i := range capCatalog {
		if capCatalog[i].Key == key {
			return &capCatalog[i]
		}
	}
	return nil
}

func capTypeByCode(code int) *CapType {
	for i := range capCatalog {
		if capCatalog[i].Code == code {
			return &capCatalog[i]
		}
	}
	return nil
}

func (t *CapType) channel(id int) *CapChannel {
	for i := range t.Channels {
		if t.Channels[i].ID == id {
			return &t.Channels[i]
		}
	}
	return nil
}

func (t *CapType) action(key string) *CapAction {
	for i := range t.Actions {
		if t.Actions[i].Key == key {
			return &t.Actions[i]
		}
	}
	return nil
}
