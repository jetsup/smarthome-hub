package hub

import (
	"crypto/rand"
	"math/big"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Every node gets a unique pairing code the first time it is paired. The code
// is stored on the node (flash) and on the hub. Nodes always answer scans from
// any gateway, but a node that is already paired only accepts a new owner when
// the request carries its code, so a node that is out of reach does not need a
// physical reset to be moved.

// 8 characters, upper-case letters and digits, without the look-alikes 0/O/1/I.
const pairCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
const pairCodeLen = 8

func randomPairCode() string {
	b := make([]byte, pairCodeLen)
	n := big.NewInt(int64(len(pairCodeAlphabet)))
	for i := range b {
		v, err := rand.Int(rand.Reader, n)
		if err != nil {
			v = big.NewInt(int64(i))
		}
		b[i] = pairCodeAlphabet[v.Int64()]
	}
	return string(b)
}

// newNodeCode returns a code no node (or in-flight pairing) uses yet.
func newNodeCode() string {
	for {
		code := randomPairCode()
		var n int64
		DB.Model(&Node{}).Where("pair_code = ?", code).Count(&n)
		if n > 0 {
			continue
		}
		DB.Model(&PendingProvisionRow{}).Where("pair_code = ?", code).Count(&n)
		if n == 0 {
			return code
		}
	}
}

// normCode accepts what a person types or a QR holds: any case, spaces or
// dashes, optionally with the "SHNODE:" prefix of the QR payload.
func normCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "SHNODE:")
	s = strings.NewReplacer(" ", "", "-", "").Replace(s)
	return s
}

// ensureNodeCode gives an existing node a code if it has none (older nodes).
func ensureNodeCode(n *Node) string {
	if n.PairCode != nil && *n.PairCode != "" {
		return *n.PairCode
	}
	code := newNodeCode()
	n.PairCode = &code
	DB.Model(&Node{}).Where("id = ?", n.ID).Update("pair_code", code)
	return code
}

func backfillPairCodes() {
	var nodes []Node
	DB.Where("pair_code IS NULL OR pair_code = ''").Find(&nodes)
	for i := range nodes {
		ensureNodeCode(&nodes[i])
	}
}

type pairDecision struct {
	Code        string
	Conflicting *Node // record on another gateway that must be replaced
	Status      int   // non-zero = refuse with this status
	Body        gin.H // refusal body
}

// decidePairing works out which code the node will hold after being paired to
// gwID, and whether the caller is allowed to take it from where it is.
func decidePairing(deviceID uint32, gwID, reqCode string) pairDecision {
	var existing Node
	if DB.Where("gateway_id = ? AND device_id = ?", gwID, deviceID).First(&existing).Error == nil {
		return pairDecision{Code: ensureNodeCode(&existing)}
	}

	seen, paired := discoveryState(deviceID)
	supplied := normCode(reqCode)

	var other Node
	hasOther := DB.Where("device_id = ? AND gateway_id != ?", deviceID, gwID).First(&other).Error == nil
	if hasOther {
		// A node that was just heard saying "I am not paired" was reset by hand:
		// physical access is proof enough.
		if seen && !paired {
			return pairDecision{Code: newNodeCode(), Conflicting: &other}
		}
		code := ensureNodeCode(&other)
		if supplied == "" {
			return pairDecision{Status: http.StatusForbidden, Body: gin.H{
				"status": "code_required", "error": "This node is paired to another gateway. Enter its pairing code to move it."}}
		}
		if supplied != code {
			return pairDecision{Status: http.StatusForbidden, Body: gin.H{
				"status": "code_invalid", "error": "That pairing code does not match this node."}}
		}
		return pairDecision{Code: code, Conflicting: &other}
	}

	if p := pendingFor(deviceID); p != nil && p.PairCode != "" {
		return pairDecision{Code: p.PairCode}
	}
	if seen && paired {
		// Paired, but this hub has no record (e.g. it was paired elsewhere):
		// the node itself checks whatever code we pass on.
		if supplied == "" {
			return pairDecision{Status: http.StatusForbidden, Body: gin.H{
				"status": "code_required", "error": "This node is already paired. Enter its pairing code to take it over."}}
		}
		return pairDecision{Code: supplied}
	}
	return pairDecision{Code: newNodeCode()}
}
