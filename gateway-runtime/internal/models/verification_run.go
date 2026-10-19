package models

import "time"

const (
	VerificationRunQueued    = "QUEUED"
	VerificationRunRunning   = "RUNNING"
	VerificationRunCompleted = "COMPLETED"
	VerificationRunFailed    = "FAILED"
)

// VerificationRun stores the durable summary of a background range
// verification. Individual log state remains on AuditLog; this table keeps
// the run history and progress that a client dashboard can safely consume.
type VerificationRun struct {
	ID       string `gorm:"primaryKey;type:varchar(36)" json:"id"`
	ClientID string `gorm:"type:varchar(36);not null;index" json:"client_id"`

	RequestedBy string    `gorm:"type:varchar(100)" json:"requested_by,omitempty"`
	FromTime    time.Time `gorm:"column:from_time;not null;index" json:"from_time"`
	ToTime      time.Time `gorm:"column:to_time;not null;index" json:"to_time"`
	Status      string    `gorm:"type:varchar(20);not null;index" json:"status"`
	BatchSize   int       `gorm:"not null;default:100" json:"batch_size"`

	TotalItems      int64 `gorm:"not null;default:0" json:"total_items"`
	ProcessedItems  int64 `gorm:"not null;default:0" json:"processed_items"`
	TotalValid      int64 `gorm:"not null;default:0" json:"total_valid"`
	TotalInvalid    int64 `gorm:"not null;default:0" json:"total_invalid"`
	TotalPending    int64 `gorm:"not null;default:0" json:"total_pending"`
	AlreadyVerified int64 `gorm:"not null;default:0" json:"already_verified"`
	VerifiedNow     int64 `gorm:"not null;default:0" json:"verified_now"`

	ErrorMessage string     `gorm:"type:text" json:"error_message,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	CreatedAt    time.Time  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"autoUpdateTime" json:"updated_at"`
}
