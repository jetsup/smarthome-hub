package hub

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// RuleAction is what a rule does to its target module.
//
//	{"action":"setEffect","params":{...}}  (any catalog action)
//	{"action":"setPin","value":1}          (plain digital/analog output on the target cap's pin)
type RuleAction struct {
	Action string                 `json:"action"`
	Value  int                    `json:"value,omitempty"`
	Params map[string]interface{} `json:"params,omitempty"`
}

var (
	ruleMu    sync.Mutex
	ruleState = map[uint]bool{}
)

func compare(op string, v, th float64) bool {
	switch op {
	case ">":
		return v > th
	case "<":
		return v < th
	case ">=":
		return v >= th
	case "<=":
		return v <= th
	case "==":
		return v == th
	case "!=":
		return v != th
	}
	return false
}

// evaluateRules runs every enabled rule that watches this reading.
func evaluateRules(gatewayID string, deviceID uint32, rv ReadingValue) {
	var rules []Rule
	DB.Where("gateway_id = ? AND enabled = ? AND source_device_id = ? AND source_cap = ? AND channel = ?",
		gatewayID, true, deviceID, rv.Cap, rv.Channel).Find(&rules)
	for _, r := range rules {
		now := compare(r.Op, rv.Value, r.Threshold)
		ruleMu.Lock()
		prev, known := ruleState[r.ID]
		ruleState[r.ID] = now
		ruleMu.Unlock()
		if known && prev == now {
			continue // only act on a change
		}
		if !known && !now && r.OnFalse == "" {
			continue
		}
		raw := r.OnTrue
		if !now {
			raw = r.OnFalse
		}
		if raw == "" {
			continue
		}
		go fireRule(r, raw, now)
	}
}

func fireRule(r Rule, raw string, state bool) {
	var act RuleAction
	if json.Unmarshal([]byte(raw), &act) != nil {
		return
	}
	var node Node
	if DB.Where("gateway_id = ? AND device_id = ?", r.GatewayID, r.TargetDeviceID).First(&node).Error != nil {
		return
	}
	switch act.Action {
	case "setPin":
		caps := parseCapabilitiesConfig(node.CapabilitiesConfig)
		if r.TargetCap < 0 || r.TargetCap >= len(caps) {
			return
		}
		pin := caps[r.TargetCap].Pin
		setPinValue(node.DeviceID, uint8(pin), uint16(act.Value))
		_ = SendPinCommandToGateway(node.GatewayID, node.DeviceID, uint8(pin), uint16(act.Value))
	default:
		if _, _, err := runAction(node, ActionRequest{Cap: r.TargetCap, Action: act.Action, Params: act.Params}); err != nil {
			log.Printf("rule %q: %v", r.Name, err)
			return
		}
	}
	id := r.GatewayID
	LogAudit(nil, "rule_fired", "gateway", &id, fmt.Sprintf("Rule \"%s\" %s → %s on device %d", r.Name, map[bool]string{true: "triggered", false: "cleared"}[state], act.Action, r.TargetDeviceID))
}

// ── API ──────────────────────────────────────────────────────────────────────

func ownedGatewayID(c *gin.Context, id string) bool {
	var n int64
	DB.Model(&Gateway{}).Where("id = ? AND user_id = ?", id, c.GetUint("userID")).Count(&n)
	return n > 0
}

func ListRules(c *gin.Context) {
	gw := c.Param("id")
	if !ownedGatewayID(c, gw) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}
	var rs []Rule
	DB.Where("gateway_id = ?", gw).Order("id").Find(&rs)
	c.JSON(http.StatusOK, rs)
}

type ruleReq struct {
	Name           string      `json:"name"`
	Enabled        *bool       `json:"enabled"`
	SourceDeviceID uint32      `json:"sourceDeviceId"`
	SourceCap      int         `json:"sourceCap"`
	Channel        int         `json:"channel"`
	Op             string      `json:"op"`
	Threshold      float64     `json:"threshold"`
	TargetDeviceID uint32      `json:"targetDeviceId"`
	TargetCap      int         `json:"targetCap"`
	OnTrue         *RuleAction `json:"onTrue"`
	OnFalse        *RuleAction `json:"onFalse"`
}

func CreateRule(c *gin.Context) {
	gw := c.Param("id")
	if !ownedGatewayID(c, gw) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}
	var req ruleReq
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	switch req.Op {
	case ">", "<", ">=", "<=", "==", "!=":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Operator must be one of > < >= <= == !="})
		return
	}
	var src, dst Node
	if DB.Where("gateway_id = ? AND device_id = ?", gw, req.SourceDeviceID).First(&src).Error != nil ||
		DB.Where("gateway_id = ? AND device_id = ?", gw, req.TargetDeviceID).First(&dst).Error != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Both nodes must belong to this gateway"})
		return
	}
	sc := parseCapabilitiesConfig(src.CapabilitiesConfig)
	if req.SourceCap < 0 || req.SourceCap >= len(sc) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "The source node has no such module"})
		return
	}
	if ct := capTypeByKey(sc[req.SourceCap].Type); ct == nil || ct.channel(req.Channel) == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "That module has no such reading"})
		return
	}
	dc := parseCapabilitiesConfig(dst.CapabilitiesConfig)
	if req.TargetCap < 0 || req.TargetCap >= len(dc) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "The target node has no such module"})
		return
	}
	if req.OnTrue == nil && req.OnFalse == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Choose what should happen"})
		return
	}
	enc := func(a *RuleAction) string {
		if a == nil {
			return ""
		}
		b, _ := json.Marshal(a)
		return string(b)
	}
	r := Rule{GatewayID: gw, Name: strings.TrimSpace(req.Name), Enabled: req.Enabled == nil || *req.Enabled,
		SourceDeviceID: req.SourceDeviceID, SourceCap: req.SourceCap, Channel: req.Channel, Op: req.Op, Threshold: req.Threshold,
		TargetDeviceID: req.TargetDeviceID, TargetCap: req.TargetCap, OnTrue: enc(req.OnTrue), OnFalse: enc(req.OnFalse)}
	if r.Name == "" {
		r.Name = "Rule"
	}
	DB.Create(&r)
	id := gw
	LogAudit(c, "rule_created", "gateway", &id, "Rule \""+r.Name+"\" created")
	c.JSON(http.StatusCreated, r)
}

func ownedRule(c *gin.Context) (Rule, bool) {
	var r Rule
	if DB.Joins("JOIN gateways ON gateways.id = rules.gateway_id").
		Where("rules.slug = ? AND gateways.user_id = ?", c.Param("ruleId"), c.GetUint("userID")).First(&r).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Rule not found"})
		return r, false
	}
	return r, true
}

func SetRuleEnabled(c *gin.Context) {
	r, ok := ownedRule(c)
	if !ok {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	DB.Model(&r).Update("enabled", req.Enabled)
	ruleMu.Lock()
	delete(ruleState, r.ID)
	ruleMu.Unlock()
	r.Enabled = req.Enabled
	c.JSON(http.StatusOK, r)
}

func DeleteRule(c *gin.Context) {
	r, ok := ownedRule(c)
	if !ok {
		return
	}
	DB.Delete(&r)
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}
