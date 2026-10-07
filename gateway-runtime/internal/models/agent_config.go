package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AgentConfig menyimpan URL dan token Agent milik masing-masing klien.
// Gateway menggunakan ini untuk memanggil GET /verify/:table/:source_record_id
// ke Agent saat verifikasi Lapis 3.
type AgentConfig struct {
	ID       string `gorm:"primaryKey;type:varchar(36)" json:"id"`
	ClientID string `gorm:"type:varchar(36);not null;uniqueIndex" json:"client_id"`

	// URL Agent yang dapat dihubungi dari jaringan Gateway.
	// Contoh: http://192.168.11.50:9090
	AgentURL string `gorm:"type:varchar(255);not null" json:"agent_url"`

	// Field auto-filled oleh install.sh telemetry callback
	TailscaleIP string `gorm:"type:varchar(45)" json:"tailscale_ip"`
	Hostname    string `gorm:"type:varchar(100)" json:"hostname"`

	// Informasi Database Klien (auto-filled dari install.sh telemetry)
	DBEngine        string `gorm:"type:varchar(50)" json:"db_engine"`        // postgres, mysql, oracle, sqlserver, mongodb
	DBName          string `gorm:"type:varchar(100)" json:"db_name"`         // Nama database yang diaudit
	DBTables        string `gorm:"type:text" json:"db_tables"`               // Daftar tabel yang dimonitor (comma-separated)
	ConnectorStatus string `gorm:"type:varchar(50)" json:"connector_status"` // running, skipped, failed_xxx
	UserSyncStatus  string `gorm:"type:varchar(50)" json:"user_sync_status"` // ok, failed, skipped
	UserSyncMessage string `gorm:"type:text" json:"user_sync_message"`       // detail warning/error sync user table

	// Deteksi Tabel User via CDC (Auto-filled dari install.sh telemetry)
	UserTableName  string `gorm:"type:varchar(100)" json:"user_table_name"`
	UserColumnName string `gorm:"type:varchar(100)" json:"user_column_name"`

	// Bearer token untuk autentikasi read — harus cocok dengan AGENT_VERIFY_TOKEN
	// di Agent. RecoveryToken sengaja dipisahkan agar token read tidak dapat
	// dipakai untuk menulis database client.
	VerifyToken   string `gorm:"type:varchar(255)" json:"-"`
	RecoveryToken string `gorm:"type:varchar(255)" json:"-"`

	// Timeout dalam detik untuk request ke Agent (default: 5)
	TimeoutSeconds int `gorm:"default:5" json:"timeout_seconds"`

	IsActive  bool           `gorm:"default:true" json:"is_active"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (a *AgentConfig) BeforeCreate(tx *gorm.DB) (err error) {
	if a.ID == "" {
		a.ID = uuid.New().String()
	}
	return
}
