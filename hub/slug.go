package hub

import (
	"crypto/rand"

	"gorm.io/gorm"
)

// Public links must never expose database ids. Every table that appears in a URL
// carries a random, unguessable slug instead.

const slugAlphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no look-alikes (i l o 0 1)

func newSlug() string {
	b := make([]byte, 12)
	rand.Read(b)
	out := make([]byte, 12)
	for i, v := range b {
		out[i] = slugAlphabet[int(v)%len(slugAlphabet)]
	}
	return string(out)
}

// Slugged is embedded in models that are addressed by URL.
type Slugged struct {
	Slug string `gorm:"size:24;index" json:"slug"`
}

// BeforeCreate gives a new row its slug.
func (s *Slugged) BeforeCreate(tx *gorm.DB) error {
	if s.Slug == "" {
		s.Slug = newSlug()
	}
	return nil
}

// backfillSlugs adds slugs to rows created before slugs existed.
func backfillSlugs(db *gorm.DB) {
	for _, table := range []string{"hmis", "mfa_devices", "user_sessions", "pending_actions", "capability_bindings", "wifi_credentials"} {
		var ids []uint
		db.Table(table).Where("slug = '' OR slug IS NULL").Pluck("id", &ids)
		for _, id := range ids {
			db.Table(table).Where("id = ?", id).UpdateColumn("slug", newSlug())
		}
	}
}
