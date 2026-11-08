package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
)

// The monitoring dashboard shows only what can be watched or controlled. The hub
// turns every node's modules into "widgets" (one tile each) so the website and the
// app draw the same dashboard without knowing about individual module types.

type Widget struct {
	ID          string `json:"id"` // nodeId:cap:channel - stable, used for hide/show
	GatewayID   string `json:"gatewayId"`
	GatewayName string `json:"gatewayName"`
	NodeID      string `json:"nodeId"`
	NodeName    string `json:"nodeName"`
	Online      bool   `json:"online"`
	Cap         int    `json:"cap"`
	Pin         int    `json:"pin,omitempty"`
	// switch | slider | light | buzzer | gauge | bool | text
	Kind     string  `json:"kind"`
	Control  bool    `json:"control"` // can be operated, not only watched
	Label    string  `json:"label"`
	Module   string  `json:"module"` // what the module is (catalog label)
	Symbol   string  `json:"symbol"`
	Unit     string  `json:"unit,omitempty"`
	Value    float64 `json:"value"`
	On       bool    `json:"on"`
	Text     string  `json:"text,omitempty"`
	Min      float64 `json:"min"`
	Max      float64 `json:"max"`
	HasValue bool    `json:"hasValue"` // false until the node reported something
	Color    string  `json:"color,omitempty"`
	Pattern  string  `json:"pattern,omitempty"`
	// Params: the last settings of a light (pattern, colour, speed...), so the dashboard can switch it on/off without losing them.
	Params map[string]interface{} `json:"params,omitempty"`
	Hidden bool                   `json:"hidden"`
	// Sensor channel this tile shows and whether the gateway logs it: tiles with history open a chart.
	Channel int  `json:"channel"`
	History bool `json:"history"`
}

type MonitorGateway struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Online      bool   `json:"online"`
	Nodes       int    `json:"nodes"`
	OnlineNodes int    `json:"onlineNodes"`
}

type DashPrefs struct {
	Gateway string   `json:"gateway"` // "all" or a gateway id
	Hidden  []string `json:"hidden"`
	// Layout the user arranged in Customise: node cards in order, and the order of
	// the tiles inside each card (node id -> tile ids).
	Order []string            `json:"order"`
	Tiles map[string][]string `json:"tiles"`
}

func loadDashPrefs(u User) DashPrefs {
	p := DashPrefs{Gateway: "all", Hidden: []string{}}
	if u.DashPrefs != "" {
		_ = json.Unmarshal([]byte(u.DashPrefs), &p)
	}
	if p.Gateway == "" {
		p.Gateway = "all"
	}
	if p.Hidden == nil {
		p.Hidden = []string{}
	}
	if p.Order == nil {
		p.Order = []string{}
	}
	if p.Tiles == nil {
		p.Tiles = map[string][]string{}
	}
	return p
}

// defaultSymbol is the icon a module gets until the user picks another.
func defaultSymbol(capType string) string {
	switch capType {
	case "digitalOutput":
		return "power"
	case "relay":
		return "plug"
	case "analogOutput":
		return "sliders"
	case "neopixel":
		return "lightbulb"
	case "buzzer":
		return "bell"
	case "dht":
		return "thermometer"
	case "ld2410":
		return "radar"
	case "ds3231":
		return "clock"
	case "analogInput":
		return "gauge"
	case "digitalInput":
		return "input"
	}
	return "chip"
}

func buildWidgets(gw Gateway, node Node, hidden map[string]bool) []Widget {
	caps := parseCapabilitiesConfig(node.CapabilitiesConfig)
	if len(caps) == 0 {
		return nil
	}
	online := gw.IsOnline && isNodeOnline(node.LastSeen)
	pv := getPinValues(node.DeviceID)
	// Outputs: the live value if the hub has one, else the last value it commanded
	// (saved in the database), else off - a node always boots with its outputs off.
	saved := savedOutputs(outKey{gw.ID, node.DeviceID})
	outValue := func(pin int) uint16 {
		if v, ok := pv[uint8(pin)]; ok {
			return v
		}
		return saved[uint8(pin)]
	}
	rs := readingsFor(node)
	effects := effectsFor(node)
	name := node.Name
	if name == "" {
		name = node.NodeID
	}
	var out []Widget
	add := func(w Widget) {
		w.GatewayID, w.GatewayName = gw.ID, gw.Name
		w.NodeID, w.NodeName, w.Online = node.NodeID, name, online
		w.ID = fmt.Sprintf("%s:%d:%s", node.NodeID, w.Cap, w.Kind+"-"+w.Label)
		w.Hidden = hidden[w.ID]
		out = append(out, w)
	}
	for i, c := range caps {
		ct := capTypeByKey(c.Type)
		if ct == nil {
			continue
		}
		label := c.Label
		if label == "" {
			label = ct.Label
		}
		sym := c.Symbol
		own := sym != ""
		if sym == "" {
			sym = defaultSymbol(c.Type)
		}
		base := Widget{Cap: i, Pin: c.Pin, Label: label, Module: ct.Label, Symbol: sym}

		switch c.Type {
		case "digitalOutput", "relay":
			w := base
			w.Kind, w.Control = "switch", true
			v := outValue(c.Pin)
			w.HasValue, w.On, w.Value = true, v != 0, float64(v)
			add(w)
		case "analogOutput":
			w := base
			w.Kind, w.Control, w.Min, w.Max = "slider", true, 0, 255
			v := outValue(c.Pin)
			w.HasValue, w.On, w.Value = true, v != 0, float64(v)
			add(w)
		case "neopixel":
			w := base
			w.Kind, w.Control, w.Min, w.Max = "light", true, 1, 100
			w.Value = 60
			w.Color = "#00A8E8"
			if e, ok := effects[fmt.Sprint(i)]; ok {
				w.HasValue = true
				w.Params = e
				p := num(e["pattern"], 0)
				w.On = p != 0
				w.Value = float64(num(e["brightness"], 60))
				if s, ok := e["color"].(string); ok && s != "" {
					w.Color = s
				}
				for _, ch := range neoPatterns {
					if ch.Value == p {
						w.Pattern = ch.Label
					}
				}
			}
			add(w)
		case "buzzer":
			w := base
			w.Kind, w.Control = "buzzer", true
			w.HasValue = true
			add(w)
		default:
			for _, ch := range ct.Channels {
				if ch.Dash == "" {
					continue
				}
				w := base
				w.Kind, w.Unit, w.Min, w.Max = ch.Dash, ch.Unit, ch.Min, ch.Max
				w.Channel, w.History = ch.ID, ch.Dash != "text"
				if len(ct.Channels) > 1 || c.Label == "" {
					w.Label = ch.Label
					if c.Label != "" {
						w.Label = c.Label + " " + lower(ch.Label)
					}
				}
				if !own && ch.Icon != "" {
					w.Symbol = ch.Icon
				}
				for _, r := range rs {
					if r.Cap != i || r.Key != ch.Key {
						continue
					}
					w.HasValue, w.Value = true, r.Value
					switch ch.Dash {
					case "bool":
						w.On = r.Value != 0
					case "text":
						w.Text = time.Unix(int64(r.Value), 0).UTC().Format("15:04:05") + " UTC"
					}
				}
				if c.Type == "analogInput" || c.Type == "digitalInput" {
					if v, ok := pv[uint8(c.Pin)]; ok {
						w.HasValue, w.Value, w.On = true, float64(v), v != 0
					}
				}
				add(w)
			}
		}
	}
	return out
}

func lower(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'A' && b[0] <= 'Z' {
		b[0] += 'a' - 'A'
	}
	return string(b)
}

// GET /api/monitor
func GetMonitor(c *gin.Context) {
	userID := c.GetUint("userID")
	var user User
	DB.First(&user, userID)
	prefs := loadDashPrefs(user)
	hidden := map[string]bool{}
	for _, id := range prefs.Hidden {
		hidden[id] = true
	}

	var gws []Gateway
	DB.Where("user_id = ?", userID).Order("name").Find(&gws)
	gateways := make([]MonitorGateway, 0, len(gws))
	widgets := []Widget{}
	for _, gw := range gws {
		var nodes []Node
		DB.Where("gateway_id = ? AND connected_at IS NOT NULL", gw.ID).Order("name").Find(&nodes)
		mg := MonitorGateway{ID: gw.ID, Name: gw.Name, Online: gw.IsOnline, Nodes: len(nodes)}
		for _, n := range nodes {
			if gw.IsOnline && isNodeOnline(n.LastSeen) {
				mg.OnlineNodes++
			}
			widgets = append(widgets, buildWidgets(gw, n, hidden)...)
		}
		gateways = append(gateways, mg)
	}
	if prefs.Gateway != "all" {
		found := false
		for _, g := range gateways {
			found = found || g.ID == prefs.Gateway
		}
		if !found {
			prefs.Gateway = "all"
		}
	}
	c.JSON(http.StatusOK, gin.H{"gateways": gateways, "widgets": widgets, "prefs": prefs})
}

var symbolRe = regexp.MustCompile(`^[a-z0-9-]{1,24}$`)

// PUT /api/monitor/prefs  {gateway, hidden}
func SetMonitorPrefs(c *gin.Context) {
	var req DashPrefs
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	userID := c.GetUint("userID")
	if req.Gateway == "" {
		req.Gateway = "all"
	}
	if req.Gateway != "all" {
		var n int64
		DB.Model(&Gateway{}).Where("id = ? AND user_id = ?", req.Gateway, userID).Count(&n)
		if n == 0 {
			req.Gateway = "all"
		}
	}
	if req.Hidden == nil {
		req.Hidden = []string{}
	}
	if req.Order == nil {
		req.Order = []string{}
	}
	if req.Tiles == nil {
		req.Tiles = map[string][]string{}
	}
	if len(req.Order) > 500 || len(req.Tiles) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Layout too large"})
		return
	}
	for _, ids := range req.Tiles {
		if len(ids) > 200 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Layout too large"})
			return
		}
	}
	if len(req.Hidden) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Too many hidden tiles"})
		return
	}
	b, _ := json.Marshal(req)
	DB.Model(&User{}).Where("id = ?", userID).Update("dash_prefs", string(b))
	c.JSON(http.StatusOK, req)
}
