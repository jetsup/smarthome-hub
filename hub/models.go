package hub

import (
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
	ID        string    `gorm:"primaryKey;size:16" json:"id"`
	UserID    uint      `gorm:"index;not null" json:"userId"`
	Name      string    `gorm:"size:255;not null" json:"name"`
	APIKey    string    `gorm:"size:64;uniqueIndex;not null" json:"apiKey,omitempty"`
	IsOnline  bool      `gorm:"default:false" json:"online"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Nodes     []Node    `gorm:"foreignKey:GatewayID" json:"nodes,omitempty"`
}

type Node struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	GatewayID string    `gorm:"index;not null;size:16" json:"gatewayId"`
	DeviceID  uint32    `gorm:"not null" json:"deviceId"`
	LastValue uint16    `gorm:"default:0" json:"value"`
	LastSeen  time.Time `json:"lastSeen"`
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

// ── In-memory registry (real-time device state from TCP) ────────────────────

type DeviceState struct {
	DeviceID uint32 `json:"deviceId"`
	Value    uint16 `json:"value"`
}

var NetworkRegistry = make(map[uint32]DeviceState)

// ── Auto-migrate all models ──────────────────────────────────────────────────

func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(&User{}, &Gateway{}, &Node{}, &AuditLog{})
}
