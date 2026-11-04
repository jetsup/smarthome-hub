package hub

import (
	"sync"
	"time"

	"gorm.io/gorm"
)

// ── Database Models ──────────────────────────────────────────────────────────

type User struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	FirstName    string `gorm:"size:100;not null" json:"firstName"`
	LastName     string `gorm:"size:100;not null" json:"lastName"`
	Email        string `gorm:"size:255;uniqueIndex;not null" json:"email"`
	PasswordHash string `gorm:"size:255;not null" json:"-"`
	// Optional two-step verification (approve a number shown on a signed-in phone).
	// MfaEnabled is true when at least one method below is on.
	MfaEnabled   bool   `gorm:"default:false" json:"mfaEnabled"`
	MfaPhone     bool   `gorm:"default:false" json:"-"` // approve in the app (number matching)
	MfaTotp      bool   `gorm:"default:false" json:"-"` // authenticator app (Google Authenticator, ...)
	MfaEmail     bool   `gorm:"default:false" json:"-"` // code sent by email
	TotpSecret   string `gorm:"size:255" json:"-"`      // AES-GCM encrypted; set during setup, used once MfaTotp is on
	TotpLastStep int64  `gorm:"default:0" json:"-"`     // last accepted 30 s step (blocks code replay)
	MfaRecovery  string `gorm:"type:text" json:"-"`     // JSON array of sha256 hashes of unused recovery codes
	// TokenVersion invalidates every older session when the password changes.
	TokenVersion int       `gorm:"default:0" json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// MfaDevice is a phone (the mobile app) that can approve sign-ins.
type MfaDevice struct {
	Slugged
	ID           uint      `gorm:"primaryKey" json:"id"`
	UserID       uint      `gorm:"index;not null" json:"-"`
	DeviceKey    string    `gorm:"uniqueIndex;size:64;not null" json:"-"`
	Name         string    `gorm:"size:80;not null" json:"name"` // the name the owner gave the phone
	Model        string    `gorm:"size:80" json:"model"`         // e.g. "X6855"
	Manufacturer string    `gorm:"size:60" json:"manufacturer"`  // e.g. "INFINIX"
	OS           string    `gorm:"size:40" json:"os"`            // e.g. "Android 14"
	LastIP       string    `gorm:"size:64" json:"lastIp"`
	LastSeen     time.Time `json:"lastSeen"`
	CreatedAt    time.Time `json:"createdAt"`
}

// UserSession is one signed-in browser or app. Tokens carry its id, so revoking
// it signs that device out immediately.
type UserSession struct {
	Slugged
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index;not null" json:"-"`
	DeviceKey string    `gorm:"index;size:64" json:"-"` // links an app session to its MfaDevice
	Label     string    `gorm:"size:160;not null" json:"label"`
	Kind      string    `gorm:"size:16;not null" json:"kind"` // browser | app
	IP        string    `gorm:"size:64" json:"ip"`
	UserAgent string    `gorm:"size:255" json:"-"`
	CreatedAt time.Time `json:"createdAt"`
	LastSeen  time.Time `json:"lastSeen"`
}

type Gateway struct {
	ID               string     `gorm:"primaryKey;size:16" json:"id"`
	UserID           uint       `gorm:"index;not null" json:"userId"`
	Name             string     `gorm:"size:255;not null" json:"name"`
	APIKey           string     `gorm:"size:64;uniqueIndex;not null" json:"apiKey,omitempty"`
	IsOnline         bool       `gorm:"default:false" json:"online"`
	LastSeen         time.Time  `json:"lastSeen"`
	APIKeyAssignedAt *time.Time `json:"apiKeyAssignedAt"`
	// WifiDirty: the saved-network list was changed on the hub while (or without
	// the gateway confirming) so the hub list must overwrite the gateway's.
	WifiDirty bool      `gorm:"default:false" json:"-"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Nodes     []Node    `gorm:"foreignKey:GatewayID;constraint:OnDelete:CASCADE" json:"nodes,omitempty"`
}

type CapabilityConfig struct {
	Type  string  `json:"type"`
	Pin   int     `json:"pin,omitempty"`
	Extra int     `json:"extra,omitempty"`
	Label string  `json:"label,omitempty"`
	Value *uint16 `json:"value,omitempty"`
}

type Node struct {
	ID                 uint       `gorm:"primaryKey" json:"id"`
	NodeID             string     `gorm:"uniqueIndex;size:16" json:"nodeId"`
	GatewayID          string     `gorm:"index;not null;size:16" json:"gatewayId"`
	DeviceID           uint32     `gorm:"uniqueIndex:idx_device_gateway;not null" json:"deviceId"`
	APIKey             string     `gorm:"size:64;uniqueIndex" json:"apiKey,omitempty"`
	DeviceType         uint8      `gorm:"default:0" json:"deviceType"`
	Capabilities       uint32     `gorm:"default:0" json:"capabilities"`
	Name               string     `gorm:"size:64;default:''" json:"name"`
	CapabilitiesConfig string     `gorm:"type:text" json:"capabilitiesConfig,omitempty"`
	LastValue          uint16     `gorm:"default:0" json:"value"`
	LastSeen           time.Time  `json:"lastSeen"`
	ConnectedAt        *time.Time `json:"connectedAt"`
	// HmiID: the one display this node is shown on / controlled from. A node is
	// never shared between displays; nil = not assigned to any display.
	HmiID     *uint     `gorm:"index" json:"hmiId"`
	CreatedAt time.Time `json:"createdAt"`
}

// Hmi is a paired touch display. It is bound to one gateway and shows only the
// nodes assigned to it.
type Hmi struct {
	Slugged
	ID        uint      `gorm:"primaryKey" json:"id"`
	DeviceID  string    `gorm:"uniqueIndex;size:40;not null" json:"deviceId"` // hardware id (MAC) or legacy-*
	GatewayID string    `gorm:"index;size:16;not null" json:"gatewayId"`
	UserID    uint      `gorm:"index;not null" json:"-"`
	Name      string    `gorm:"size:64;not null" json:"name"`
	LastSeen  time.Time `json:"lastSeen"`
	CreatedAt time.Time `json:"createdAt"`

	// Appearance of the touchscreen, edited from the app / website.
	Theme      string `gorm:"size:8;default:dark" json:"theme"`    // dark | light
	Accent     string `gorm:"size:6;default:00A8E8" json:"accent"` // RRGGBB
	Brightness int    `gorm:"default:100" json:"brightness"`       // 10-100 (%)
	StatusLed  bool   `gorm:"default:true" json:"statusLed"`       // rainbow status LED
}

// PendingProvisionRow keeps a provisioning request until the node's first report,
// so restarting the hub in between does not strand a node that already holds its key.
type PendingProvisionRow struct {
	DeviceID           uint32 `gorm:"primaryKey;autoIncrement:false"`
	GatewayID          string `gorm:"size:16;not null"`
	APIKey             string `gorm:"size:64"`
	DeviceType         uint8
	Name               string `gorm:"size:64"`
	CapabilitiesConfig string `gorm:"type:text"`
	CreatedAt          time.Time
}

// BeforeCreate gives every new display the default look (GORM would insert the
// zero values, e.g. status light off).
func (h *Hmi) BeforeCreate(tx *gorm.DB) error {
	if h.Slug == "" {
		h.Slug = newSlug()
	}
	if h.Theme == "" {
		h.Theme = "dark"
	}
	if h.Accent == "" {
		h.Accent = "00A8E8"
	}
	if h.Brightness == 0 {
		h.Brightness = 100
	}
	h.StatusLed = true
	return nil
}

// WifiCredential stores SSID/password pairs for a gateway.
type WifiCredential struct {
	Slugged
	ID        uint      `gorm:"primaryKey" json:"id"`
	GatewayID string    `gorm:"index;not null;size:16" json:"gatewayId"`
	SSID      string    `gorm:"size:255;not null" json:"ssid"`
	Password  string    `gorm:"size:255;not null" json:"password"`
	Priority  int       `gorm:"default:0;index" json:"priority"` // 0 = tried first
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
	CreatedAt  time.Time `gorm:"index" json:"createdAt"`

	Actor     string `gorm:"size:160" json:"actor"`                 // who (email at the time, or display name)
	ActorKind string `gorm:"size:10;default:user" json:"actorKind"` // user | display | system
	Device    string `gorm:"size:200" json:"device"`                // "Chrome on Linux", "INFINIX X6855 · SmartHomes app"
	Category  string `gorm:"size:16;index" json:"category"`         // account | security | gateway | node | display | wifi | system
	Outcome   string `gorm:"size:8;default:ok" json:"outcome"`      // ok | failed
}

// PendingAction queues gateway/node deletion jobs for execution when the target comes online.
type PendingAction struct {
	Slugged
	ID        uint      `gorm:"primaryKey" json:"id"`
	GatewayID string    `gorm:"index;not null;size:16" json:"gatewayId"`
	Action    string    `gorm:"size:50;not null" json:"action"`          // "delete_gateway"
	TargetID  string    `gorm:"size:16;not null" json:"targetId"`        // gateway ID to delete
	Status    string    `gorm:"size:20;default:'pending'" json:"status"` // pending | completed | failed
	Error     string    `gorm:"type:text" json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type CapabilityBinding struct {
	Slugged
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
	DeviceTypeUnknown = 0
	DeviceTypeAnalog  = 1
	DeviceTypeDigital = 2
	DeviceTypeRelay   = 3
	DeviceTypeIR      = 4
	DeviceTypeHybrid  = 5
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

var (
	networkRegistry   = make(map[uint32]DeviceState)
	networkRegistryMu sync.RWMutex
)

func registrySet(s DeviceState) {
	networkRegistryMu.Lock()
	networkRegistry[s.DeviceID] = s
	networkRegistryMu.Unlock()
}

func registryGet(id uint32) (DeviceState, bool) {
	networkRegistryMu.RLock()
	defer networkRegistryMu.RUnlock()
	s, ok := networkRegistry[id]
	return s, ok
}

func registryDelete(id uint32) bool {
	networkRegistryMu.Lock()
	defer networkRegistryMu.Unlock()
	_, ok := networkRegistry[id]
	delete(networkRegistry, id)
	return ok
}

// RegistrySnapshot returns a copy of the real-time device registry.
func RegistrySnapshot() map[uint32]DeviceState {
	networkRegistryMu.RLock()
	defer networkRegistryMu.RUnlock()
	out := make(map[uint32]DeviceState, len(networkRegistry))
	for k, v := range networkRegistry {
		out[k] = v
	}
	return out
}

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
	if err := db.AutoMigrate(&User{}, &Gateway{}, &Node{}, &AuditLog{}, &WifiCredential{}, &CapabilityBinding{}, &PendingAction{}, &Hmi{}, &MfaDevice{}, &UserSession{}, &PendingProvisionRow{}); err != nil {
		return err
	}
	backfillSlugs(db)
	// Create unique composite index for bindings (no-op if already exists)
	if !db.Migrator().HasIndex(&CapabilityBinding{}, "idx_unique_binding") {
		db.Migrator().CreateIndex(&CapabilityBinding{}, "idx_unique_binding")
	}
	return nil
}
