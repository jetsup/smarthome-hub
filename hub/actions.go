package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// GET /api/capabilities/catalog — everything the UIs need to build forms and controls.
func GetCapabilityCatalog(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"types": capCatalog})
}

// GET /api/system/time
func GetSystemTime(c *gin.Context) {
	c.JSON(http.StatusOK, timeStatus())
}

type ActionRequest struct {
	Cap    int                    `json:"cap"`    // capability slot index
	Action string                 `json:"action"` // catalog action key
	Params map[string]interface{} `json:"params"`
}

func num(v interface{}, def int) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		if n, err := strconv.Atoi(x); err == nil {
			return n
		}
	case bool:
		if x {
			return 1
		}
		return 0
	}
	return def
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func parseColor(v interface{}, def [3]byte) [3]byte {
	s, ok := v.(string)
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if !ok || len(s) != 6 {
		return def
	}
	n, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return def
	}
	return [3]byte{byte(n >> 16), byte(n >> 8), byte(n)}
}

// encodeAction turns a validated action into the node's argument bytes.
func encodeAction(ct *CapType, cap CapabilityConfig, act *CapAction, params map[string]interface{}) (byte, []byte, map[string]interface{}, error) {
	clean := map[string]interface{}{}
	switch ct.Key {
	case "neopixel":
		pat := num(params["pattern"], 1)
		valid := false
		for _, ch := range neoPatterns {
			if ch.Value == pat {
				valid = true
				if ch.Ring && !cap.Ring {
					return 0, nil, nil, fmt.Errorf("\"%s\" is only for NeoPixel rings. Turn on \"Arranged in a ring\" for this module first", ch.Label)
				}
			}
		}
		if !valid {
			return 0, nil, nil, fmt.Errorf("Unknown pattern %d", pat)
		}
		col := parseColor(params["color"], [3]byte{0, 168, 232})
		speed := clampInt(num(params["speed"], 50), 1, 100)
		bright := clampInt(num(params["brightness"], 60), 1, 100)
		rev := 0
		if b, ok := params["reverse"].(bool); ok && b {
			rev = 1
		}
		// Random colour only where the pattern can use it; otherwise it falls back
		// to the chosen (default) colour.
		rnd := 0
		if b, ok := params["random"].(bool); ok && b && neoRandomOK(pat) {
			rnd = 1
		}
		clean = map[string]interface{}{"pattern": pat, "color": fmt.Sprintf("#%02X%02X%02X", col[0], col[1], col[2]),
			"speed": speed, "brightness": bright, "reverse": rev == 1, "random": rnd == 1}
		return byte(act.ID), []byte{byte(pat), col[0], col[1], col[2], byte(speed), byte(bright), byte(rev), byte(rnd)}, clean, nil
	case "buzzer":
		pat := num(params["pattern"], 1)
		ok := false
		for _, ch := range buzzPatterns {
			if ch.Value == pat {
				ok = true
			}
		}
		if !ok {
			return 0, nil, nil, fmt.Errorf("Unknown sound %d", pat)
		}
		rep := clampInt(num(params["repeat"], 1), 0, 20)
		clean = map[string]interface{}{"pattern": pat, "repeat": rep}
		return byte(act.ID), []byte{byte(pat), byte(rep)}, clean, nil
	case "ds3231":
		now, _ := systemNow()
		e := uint32(now.Unix())
		return byte(act.ID), []byte{byte(e), byte(e >> 8), byte(e >> 16), byte(e >> 24)}, clean, nil
	}
	return 0, nil, nil, fmt.Errorf("%s has no actions", ct.Label)
}

// runAction validates and sends an action to a node. Used by the API and the rule engine.
func runAction(node Node, req ActionRequest) (gin.H, int, error) {
	caps := parseCapabilitiesConfig(node.CapabilitiesConfig)
	if req.Cap < 0 || req.Cap >= len(caps) {
		return nil, http.StatusBadRequest, fmt.Errorf("This node has no module %d", req.Cap)
	}
	cap := caps[req.Cap]
	ct := capTypeByKey(cap.Type)
	if ct == nil {
		return nil, http.StatusBadRequest, fmt.Errorf("Unknown module type")
	}
	act := ct.action(req.Action)
	if act == nil {
		return nil, http.StatusBadRequest, fmt.Errorf("%s does not support \"%s\"", ct.Label, req.Action)
	}
	code, args, clean, err := encodeAction(ct, cap, act, req.Params)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	// Remember the last setting so the UI (and a rebooted node) can show it.
	if len(clean) > 0 {
		var all map[string]map[string]interface{}
		_ = json.Unmarshal([]byte(node.Effects), &all)
		if all == nil {
			all = map[string]map[string]interface{}{}
		}
		all[strconv.Itoa(req.Cap)] = clean
		b, _ := json.Marshal(all)
		DB.Model(&Node{}).Where("id = ?", node.ID).Update("effects", string(b))
	}
	if err := sendFrameToGateway(node.GatewayID, packAction(node.DeviceID, byte(req.Cap), code, args)); err != nil {
		return gin.H{"status": "saved_offline", "message": "Saved, but the gateway is offline"}, http.StatusOK, nil
	}
	return gin.H{"status": "sent", "applied": clean}, http.StatusOK, nil
}

// POST /api/nodes/:nodeId/action
func NodeAction(c *gin.Context) {
	var node Node
	if err := DB.Joins("JOIN gateways ON gateways.id = nodes.gateway_id").
		Where("nodes.node_id = ? AND gateways.user_id = ?", c.Param("nodeId"), c.GetUint("userID")).
		First(&node).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}
	var req ActionRequest
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	resp, status, err := runAction(node, req)
	if err != nil {
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	LogAudit(c, "node_action", "node", &node.NodeID, fmt.Sprintf("%s on module %d of %s", req.Action, req.Cap, node.Name))
	c.JSON(status, resp)
}

// effectsFor returns the last action parameters per module (for the UI).
func effectsFor(node Node) map[string]map[string]interface{} {
	var all map[string]map[string]interface{}
	_ = json.Unmarshal([]byte(node.Effects), &all)
	return all
}

var _ = time.Now
