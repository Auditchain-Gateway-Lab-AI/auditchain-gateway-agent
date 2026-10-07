package models

import "time"

// ClientDashboardStats menyimpan statistik agregat per klien untuk dashboard.
// Tabel ini di-update setiap kali:
//   - Log baru masuk (via Kafka consumer atau HTTP ingestion)
//   - Proses verify-table selesai dijalankan
type ClientDashboardStats struct {
	ID       uint   `gorm:"primaryKey;autoIncrement" json:"id"`
	ClientID string `gorm:"type:varchar(36);not null;uniqueIndex" json:"client_id"`

	// -- Statistik Log --
	// Update setiap log baru masuk
	TotalLogs          int64      `gorm:"type:bigint;not null;default:0" json:"total_logs"`
	TotalTablesAudited int        `gorm:"type:int;not null;default:0" json:"total_tables_audited"`
	TotalRowsTracked   int64      `gorm:"type:bigint;not null;default:0" json:"total_rows_tracked"`
	LogsToday          int64      `gorm:"type:bigint;not null;default:0" json:"logs_today"`
	LogsTodayDate      *time.Time `gorm:"type:date" json:"-"` // internal: tanggal terakhir reset
	LastLogAt          *time.Time `gorm:"type:timestamptz" json:"last_log_at"`

	// -- Statistik Blockchain --
	TotalAnchored    int64   `gorm:"type:bigint;not null;default:0" json:"total_anchored"`
	TotalPending     int64   `gorm:"type:bigint;not null;default:0" json:"total_pending"`
	AnchorPercentage float64 `gorm:"type:decimal(5,2);not null;default:0" json:"anchor_percentage"` // (anchored/total)*100

	// -- Statistik Verifikasi --
	// Update setiap verify-table selesai
	LastVerifiedAt     *time.Time `gorm:"type:timestamptz" json:"last_verified_at"`
	LastVerifiedTable  string     `gorm:"type:varchar(255)" json:"last_verified_table"`
	LastCheckAt        *time.Time `gorm:"column:last_integrity_check_at;type:timestamptz" json:"last_integrity_check_at"`
	LastCheckSource    string     `gorm:"column:last_integrity_check_source;type:varchar(40);not null;default:''" json:"last_integrity_check_source"`
	LastCheckRunID     string     `gorm:"column:last_integrity_check_run_id;type:varchar(36);default:''" json:"last_integrity_check_run_id"`
	LastCheckLogs      int64      `gorm:"column:last_integrity_check_logs;type:bigint;not null;default:0" json:"last_integrity_check_logs"`
	TotalVerifications int64      `gorm:"type:bigint;not null;default:0" json:"total_verifications"`
	TotalRowsVerified  int64      `gorm:"type:bigint;not null;default:0" json:"total_rows_verified"`
	TotalValid         int64      `gorm:"type:bigint;not null;default:0" json:"total_valid"`
	TotalTampered      int64      `gorm:"type:bigint;not null;default:0" json:"total_tampered"`
	TotalVerifyPending int64      `gorm:"type:bigint;not null;default:0" json:"total_verify_pending"`
	TotalAgentError    int64      `gorm:"type:bigint;not null;default:0" json:"total_agent_error"`
	TotalFabricError   int64      `gorm:"type:bigint;not null;default:0" json:"total_fabric_error"`
	IntegrityScore     float64    `gorm:"type:decimal(5,2);not null;default:100" json:"integrity_score"` // (valid/verified)*100

	// -- Statistik Aktivitas --
	TotalInserts int64 `gorm:"type:bigint;not null;default:0" json:"total_inserts"`
	TotalUpdates int64 `gorm:"type:bigint;not null;default:0" json:"total_updates"`
	TotalDeletes int64 `gorm:"type:bigint;not null;default:0" json:"total_deletes"`

	// -- Statistik Recovery & Incident --
	TotalRecoveries      int64 `gorm:"type:bigint;not null;default:0" json:"total_recoveries"`
	TotalTamperIncidents int64 `gorm:"type:bigint;not null;default:0" json:"total_tamper_incidents"`
	OpenTamperIncidents  int64 `gorm:"type:bigint;not null;default:0" json:"open_tamper_incidents"`

	// -- Hasil Verifikasi per Tabel (JSONB) --
	// Format: {"RUANGAN": {"valid":5,"tampered":0,"pending":1}, ...}
	TableVerifyResults string `gorm:"type:jsonb;default:'{}'" json:"table_verify_results"`

	// -- Timestamp --
	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}
