package models

import "time"

// ReportRecord menyimpan metadata laporan yang telah dibuat
type ReportRecord struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	ClientID    string     `gorm:"type:varchar(36);not null;index" json:"client_id"`
	Name        string     `gorm:"type:varchar(255)" json:"name"`
	PeriodFrom  time.Time  `json:"period_from"`
	PeriodTo    time.Time  `json:"period_to"`
	Format      string     `gorm:"type:varchar(10)" json:"format"` // "csv" | "pdf"
	Type        string     `gorm:"type:varchar(20)" json:"type"`   // "on_demand" | "scheduled"
	Status      string     `gorm:"type:varchar(20);default:'processing'" json:"status"`
	FileBinary  []byte     `gorm:"type:bytea" json:"-"` // untuk re-download (Phase 2)
	GeneratedAt *time.Time `json:"generated_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// ReportSchedule menyimpan konfigurasi jadwal laporan per client
type ReportSchedule struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	ClientID   string    `gorm:"type:varchar(36);not null;uniqueIndex" json:"client_id"`
	IsActive   bool      `gorm:"default:false" json:"is_active"`
	Frequency  string    `gorm:"type:varchar(20)" json:"frequency"` // "daily"|"weekly"|"monthly"
	SendDay    int       `json:"send_day"`                          // weekly: 1-7, monthly: 1-28
	SendHour   int       `json:"send_hour"`                         // 0-23, default: 7
	Format     string    `gorm:"type:varchar(10)" json:"format"`    // "csv" | "pdf"
	Recipients string    `gorm:"type:text" json:"recipients"`       // JSON array of emails
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
