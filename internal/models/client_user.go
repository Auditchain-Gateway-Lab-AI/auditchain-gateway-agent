package models

import (
	"time"

	"gorm.io/gorm"
)

// ClientUser menyimpan data user yang didapat dari tabel users database client via CDC.
type ClientUser struct {
	ID          uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	ClientID    string         `gorm:"type:varchar(36);not null;index:idx_client_user,unique" json:"client_id"`
	UserID      string         `gorm:"type:varchar(255);not null;index:idx_client_user,unique" json:"user_id"`
	Username    string         `gorm:"type:varchar(255)" json:"username"`
	Email       string         `gorm:"type:varchar(255)" json:"email"`
	FullName    string         `gorm:"type:varchar(255)" json:"full_name"`
	SourceTable string         `gorm:"type:varchar(255)" json:"source_table"` // Nama tabel asal, contoh "public.users"
	RawData     string         `gorm:"type:jsonb" json:"raw_data"`            // Seluruh raw data CDC
	LastSeenAt  time.Time      `json:"last_seen_at"`
	CreatedAt   time.Time      `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt   time.Time      `gorm:"autoUpdateTime" json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}
