package models

import "time"

// ClientTable menyimpan ringkasan agregat tabel per client (Counter Cache)
type ClientTable struct {
	ID            uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	ClientID      string    `gorm:"type:varchar(36);not null;index:idx_client_table,unique" json:"client_id"`
	TableName     string    `gorm:"type:varchar(255);not null;index:idx_client_table,unique" json:"table_name"`
	RowCount      int64     `gorm:"type:bigint;not null;default:0" json:"row_count"`
	LastAction    string    `gorm:"type:varchar(100)" json:"last_action"`
	LastActor     string    `gorm:"type:varchar(100)" json:"last_actor"`
	LastUpdatedAt time.Time `gorm:"type:timestamptz" json:"last_updated_at"`
	CreatedAt     time.Time `gorm:"autoCreateTime" json:"created_at"`

	// Field alias untuk kompatibilitas frontend
	Resource string `gorm:"-" json:"resource,omitempty"`
}
