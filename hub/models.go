package hub

import (
	"sync"
	"time"

	"gorm.io/gorm"
)

// ── Database Models ──────────────────────────────────────────────────────────

type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	FirstName    string    `gorm:"size:100;not null" json:"firstName"`
	LastName     string    `gorm:"size:100;not null" json:"lastName"`
	Email        string    `gorm:"size:255;uniqueIndex;not null" json:"email"`
	PasswordHash string    `gorm:"size:255;not null" json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Gateway struct {
	ID               string     `gorm:"primaryKey;size:16" json:"id"`
	UserID           uint       `gorm:"index;not null" json:"userId"`
	Name             string     `gorm:"size:255;not null" json:"name"`
	APIKey           string     `gorm:"size:64;uniqueIndex;not null" json:"apiKey,omitempty"`
	IsOnline         bool       `gorm:"default:false" json:"online"`
	LastSeen         time.Time  `json:"lastSeen"`
	APIKeyAssignedAt *time.Time `json:"apiKeyAssignedAt"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	Nodes            []Node     `gorm:"foreignKey:GatewayID;constraint:OnDelete:CASCADE" json:"nodes,omitempty"`
}

type CapabilityConfig struct {
	Type   string  `json:"type"`
	Pin    int     `json:"pin,omitempty"`
	Extra  int     `json:"extra,omitempty"`
	Label  string  `json:"label,omitempty"`
	Value  *uint16 `json:"value,omitempty"`
}

type Node struct {
	ID                 uint               `gorm:"primaryKey" json:"id"`
	NodeID             string             `gorm:"uniqueIndex;size:16" json:"nodeId"`
	GatewayID          string             `gorm:"index;not null;size:16" json:"gatewayId"`
	DeviceID           uint32             `gorm:"uniqueIndex:idx_device_gateway;not null" json:"deviceId"`
	APIKey             string             `gorm:"size:64;uniqueIndex" json:"apiKey,omitempty"`
	DeviceType         uint8              `gorm:"default:0" json:"deviceType"`
	Capabilities       uint32             `gorm:"default:0" json:"capabilities"`
	Name               string             `gorm:"size:64;default:''" json:"name"`
	CapabilitiesConfig string             `gorm:"type:text" json:"capabilitiesConfig,omitempty"`
	LastValue          uint16             `gorm:"default:0" json:"value"`
	LastSeen           time.Time          `json:"lastSeen"`
	ConnectedAt        *time.Time         `json:"connectedAt"`
	CreatedAt          time.Time          `json:"createdAt"`
}

// WifiCredential stores SSID/password pairs for a gateway.
type WifiCredential struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	GatewayID string    `gorm:"index;not null;size:16" json:"gatewayId"`
	SSID      string    `gorm:"size:255;not null" json:"ssid"`
	Password  string    `gorm:"size:255;not null" json:"password"`
	CreatedAt time.Time `json:"createdAt"`
}

type AuditLog struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	UserID     *uint     `gorm:"index" json:"userId"`
	Action     string    `gorm:"size:100;not null" json:"action"`
	EntityType string    `gorm:"size:50" json:"entityType"`
	EntityID   *string   `gorm:"size:16" json:"entityId"`
	Details    string    `gorm:"type:text" json:"details"`
	IPAddress  string    `gorm:"size:45" json:"ipAddress"`
	CreatedAt  time.Time `json:"createdAt"`
}

// PendingAction queues gateway/node deletion jobs for execution when the target comes online.
type PendingAction struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	GatewayID string    `gorm:"index;not null;size:16" json:"gatewayId"`
	Action    string    `gorm:"size:50;not null" json:"action"`   // "delete_gateway"
	TargetID  string    `gorm:"size:16;not null" json:"targetId"` // gateway ID to delete
	Status    string    `gorm:"size:20;default:'pending'" json:"status"` // pending | completed | failed
	Error     string    `gorm:"type:text" json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type CapabilityBinding struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	GatewayID      string    `gorm:"index:idx_binding_source;uniqueIndex:idx_unique_binding;not null;size:16" json:"gatewayId"`
	SourceDeviceID uint32    `gorm:"index:idx_binding_source;uniqueIndex:idx_unique_binding;not null" json:"sourceDeviceId"`
	SourcePin      uint8     `gorm:"uniqueIndex:idx_unique_binding;not null" json:"sourcePin"`
	TargetDeviceID uint32    `gorm:"uniqueIndex:idx_unique_binding;not null" json:"targetDeviceId"`
	TargetPin      uint8     `gorm:"uniqueIndex:idx_unique_binding;not null" json:"targetPin"`
	CreatedAt      time.Time `json:"createdAt"`
}

// ── Device type constants ─────────────────────────────────────────────────────
const (
	DeviceTypeUnknown  = 0
	DeviceTypeAnalog   = 1
	DeviceTypeDigital  = 2
	DeviceTypeRelay    = 3
	DeviceTypeIR       = 4
	DeviceTypeHybrid   = 5
)

// Capability bitmask constants
const (
	CapAnalogInput  = 1 << 0
	CapDigitalInput = 1 << 1
	CapRelayOutput  = 1 << 2
	CapIRTX         = 1 << 3
	CapIRRX         = 1 << 4
)

// ── Config ────────────────────────────────────────────────────────────────────

// NodeOfflineTimeout defines how long without a ping/telemetry before a node
// is considered offline. Override via env var NODE_OFFLINE_TIMEOUT (in seconds).
var NodeOfflineTimeout = 300 * time.Second // default 5 minutes

// GatewayOfflineTimeout defines how long without a heartbeat before a gateway
// is considered offline.
var GatewayOfflineTimeout = 60 * time.Second

// IsNodeOnline returns true if the node's LastSeen is within NodeOfflineTimeout.
func IsNodeOnline(lastSeen time.Time) bool {
	return time.Since(lastSeen) <= NodeOfflineTimeout
}

// ── In-memory registry (real-time device state from TCP) ────────────────────

type DeviceState struct {
	DeviceID uint32 `json:"deviceId"`
	Value    uint16 `json:"value"`
}

var NetworkRegistry = make(map[uint32]DeviceState)

// DiscoveredNodeTTL is how long a discovered node remains in the set without refresh (5 min).
const DiscoveredNodeTTL = 5 * 60 * 1000 // milliseconds

// DiscoveredNodes tracks unprovisioned nodes reported by each gateway (gatewayID → deviceID → lastSeen).
var (
	DiscoveredNodes = make(map[string]map[uint32]int64)
	discoveredMu    sync.RWMutex
)

func AddDiscoveredNode(gatewayID string, deviceID uint32) {
	discoveredMu.Lock()
	defer discoveredMu.Unlock()
	if DiscoveredNodes[gatewayID] == nil {
		DiscoveredNodes[gatewayID] = make(map[uint32]int64)
	}
	DiscoveredNodes[gatewayID][deviceID] = nowMillis()
}

func nowMillis() int64 {
	return time.Now().UnixMilli()
}

func GetDiscoveredNodeSet(gatewayID string) map[uint32]bool {
	discoveredMu.RLock()
	defer discoveredMu.RUnlock()
	nodes := DiscoveredNodes[gatewayID]
	if nodes == nil {
		return nil
	}
	cutoff := nowMillis() - DiscoveredNodeTTL
	result := make(map[uint32]bool, len(nodes))
	for k, lastSeen := range nodes {
		if lastSeen >= cutoff {
			result[k] = true
		}
	}
	return result
}

// PurgeStaleDiscoveredNodes removes entries older than TTL for all gateways.
// Called periodically from status monitor.
func PurgeStaleDiscoveredNodes() {
	discoveredMu.Lock()
	defer discoveredMu.Unlock()
	cutoff := nowMillis() - DiscoveredNodeTTL
	for gwID, nodes := range DiscoveredNodes {
		for devID, lastSeen := range nodes {
			if lastSeen < cutoff {
				delete(nodes, devID)
			}
		}
		if len(nodes) == 0 {
			delete(DiscoveredNodes, gwID)
		}
	}
}

func ClearDiscoveredNodes(gatewayID string) {
	discoveredMu.Lock()
	defer discoveredMu.Unlock()
	DiscoveredNodes[gatewayID] = make(map[uint32]int64)
}

func RemoveDiscoveredNode(gatewayID string, deviceID uint32) {
	discoveredMu.Lock()
	defer discoveredMu.Unlock()
	if nodes := DiscoveredNodes[gatewayID]; nodes != nil {
		delete(nodes, deviceID)
	}
}

type DiscoveredNodeInfo struct {
	DeviceID   uint32 `json:"deviceId"`
	GatewayID  string `json:"gatewayId"`
	DeviceType uint8  `json:"deviceType"`
}

func GetAllDiscoveredNodes() []DiscoveredNodeInfo {
	discoveredMu.RLock()
	defer discoveredMu.RUnlock()
	var result []DiscoveredNodeInfo
	for gwID, nodes := range DiscoveredNodes {
		for devID := range nodes {
			result = append(result, DiscoveredNodeInfo{
				DeviceID:  devID,
				GatewayID: gwID,
			})
		}
	}
	return result
}

// ── Auto-migrate all models ──────────────────────────────────────────────────

func AutoMigrate(db *gorm.DB) error {
	if err := db.AutoMigrate(&User{}, &Gateway{}, &Node{}, &AuditLog{}, &WifiCredential{}, &CapabilityBinding{}, &PendingAction{}); err != nil {
		return err
	}
	// Create unique composite index for bindings (no-op if already exists)
	if !db.Migrator().HasIndex(&CapabilityBinding{}, "idx_unique_binding") {
		db.Migrator().CreateIndex(&CapabilityBinding{}, "idx_unique_binding")
	}
	return nil
}
