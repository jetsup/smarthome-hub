package hub

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// Hardware (MAC) addresses of every device. Nodes report theirs in a small
// frame (also while still unpaired, so it is known before the node is added),
// gateways send theirs when they connect, and displays are identified by it.

var macRe = regexp.MustCompile(`^([0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}$`)

func normMac(s string) string {
	s = strings.TrimSpace(s)
	if !macRe.MatchString(s) {
		return ""
	}
	return strings.ToUpper(strings.ReplaceAll(s, "-", ":"))
}

var (
	macMu    sync.RWMutex
	nodeMacs = map[uint32]string{} // device id -> MAC, from the nodes' own frames
)

func macOf(deviceID uint32) string {
	macMu.RLock()
	defer macMu.RUnlock()
	return nodeMacs[deviceID]
}

func recordMac(deviceID uint32, mac string) {
	mac = normMac(mac)
	if mac == "" {
		return
	}
	macMu.Lock()
	changed := nodeMacs[deviceID] != mac
	nodeMacs[deviceID] = mac
	macMu.Unlock()
	if changed {
		DB.Model(&Node{}).Where("device_id = ?", deviceID).Update("mac", mac)
	}
}

func recordGatewayMac(gatewayID, mac string) {
	if mac = normMac(mac); mac != "" {
		DB.Model(&Gateway{}).Where("id = ?", gatewayID).Update("mac", mac)
	}
}

// backfillMacs fills the MAC of displays (their id is the MAC).
func backfillMacs() {
	var hs []Hmi
	DB.Where("mac IS NULL OR mac = ''").Find(&hs)
	for _, h := range hs {
		if m := normMac(h.DeviceID); m != "" {
			DB.Model(&Hmi{}).Where("id = ?", h.ID).Update("mac", m)
		}
	}
}

// adoptSettings makes a freshly activated node take over what the node it
// replaces had: the display it is shown on, its last light/sound settings, and
// the bindings and rules that pointed at it. The replaced record is removed.
func adoptSettings(n *Node, oldDevice uint32) {
	if oldDevice == 0 {
		return
	}
	// A node handed over from a deleted gateway gets its saved state back.
	var det DetachedNode
	if DB.Where("device_id = ?", oldDevice).First(&det).Error == nil && oldDevice == n.DeviceID {
		n.Effects, n.Outputs = det.Effects, det.Outputs
		DB.Delete(&det)
		return
	}
	if oldDevice == n.DeviceID {
		return
	}
	var old Node
	if DB.Where("device_id = ?", oldDevice).First(&old).Error != nil {
		return
	}
	if old.HmiID != nil {
		n.HmiID = old.HmiID
	}
	n.Effects = old.Effects
	n.Outputs = old.Outputs
	for _, m := range []any{&CapabilityBinding{}, &Rule{}} {
		DB.Model(m).Where("source_device_id = ?", oldDevice).Updates(map[string]any{"source_device_id": n.DeviceID, "gateway_id": n.GatewayID})
		DB.Model(m).Where("target_device_id = ?", oldDevice).Updates(map[string]any{"target_device_id": n.DeviceID, "gateway_id": n.GatewayID})
	}
	invalidateTelemetryCache(oldDevice)
	DB.Delete(&old)
	LogAudit(nil, "node_replaced", "gateway", &n.GatewayID, fmt.Sprintf("Node %d took over the settings of node %d", n.DeviceID, oldDevice))
}

// resolveAdopt handles "take over the settings of an earlier node": the
// request names it by its pairing code. Unless the caller gave their own, the
// name and module setup are copied from it.
func resolveAdopt(userID uint, code string, newDevice uint32, name *string, caps *[]CapabilityConfig) (*Node, int, gin.H) {
	code = normCode(code)
	if code == "" {
		return nil, 0, nil
	}
	var old Node
	err := DB.Joins("JOIN gateways ON gateways.id = nodes.gateway_id").
		Where("nodes.pair_code = ? AND gateways.user_id = ?", code, userID).First(&old).Error
	if err != nil {
		return nil, http.StatusNotFound, gin.H{"status": "adopt_invalid", "error": "No node of yours has that pairing code."}
	}
	if *name == "" {
		*name = old.Name
	}
	if len(*caps) == 0 {
		*caps = parseCapabilitiesConfig(old.CapabilitiesConfig)
	}
	return &old, 0, nil
}

func adoptFromID(adopt *Node, newDevice uint32) uint32 {
	if adopt == nil || adopt.DeviceID == newDevice {
		return 0
	}
	return adopt.DeviceID
}

// capsOrStored: a node that was reset and is added again keeps the setup the hub remembers.
func capsOrStored(given []CapabilityConfig, n Node) []CapabilityConfig {
	if len(given) > 0 {
		return given
	}
	return parseCapabilitiesConfig(n.CapabilitiesConfig)
}
