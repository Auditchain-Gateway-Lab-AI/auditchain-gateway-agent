package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Client merepresentasikan perusahaan/tenant penyewa layanan
type Client struct {
	ID          string `gorm:"primaryKey;type:varchar(36)" json:"id"`
	CompanyName string `gorm:"type:varchar(100);not null" json:"company_name"`

	// Keamanan API Key
	APIKeyPrefix string `gorm:"type:varchar(20);uniqueIndex;not null" json:"api_key_prefix"`
	APIKeyHash   string `gorm:"type:varchar(255);not null" json:"-"`

	// Konfigurasi SaaS
	Status string `gorm:"type:varchar(20);default:'active'" json:"status"`

	// Konfigurasi Mapping Field Dinamis
	// Digunakan untuk memetakan field kustom klien ke field standar Gateway.
	// Contoh untuk klien yang menggunakan auditchain-agent (payload dari audit_trail):
	//   ActorField           = "app_user"
	//   FallbackActorField   = "db_user"   ← baru: fallback jika app_user null
	//   ActionField          = "operasi"
	//   ResourceField        = "tabel"
	ActorField         string `gorm:"type:varchar(100)" json:"actor_field"`
	FallbackActorField string `gorm:"type:varchar(100)" json:"fallback_actor_field"`
	CreateActorField   string `gorm:"type:varchar(100)" json:"create_actor_field"`
	UpdateActorField   string `gorm:"type:varchar(100)" json:"update_actor_field"`
	DeleteActorField   string `gorm:"type:varchar(100)" json:"delete_actor_field"`
	ActionField        string `gorm:"type:varchar(100)" json:"action_field"`
	ResourceField      string `gorm:"type:varchar(100)" json:"resource_field"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// Relasi (Has Many)
	Users     []User     `gorm:"foreignKey:ClientID" json:"-"`
	AuditLogs []AuditLog `gorm:"foreignKey:ClientID" json:"-"`
}

func (c *Client) BeforeCreate(tx *gorm.DB) (err error) {
	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	return
}
