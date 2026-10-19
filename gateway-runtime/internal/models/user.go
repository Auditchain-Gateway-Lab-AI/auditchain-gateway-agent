package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type User struct {
	ID string `gorm:"primaryKey;type:varchar(36)" json:"id"`

	// 👇 TAMBAHAN SAAS: FK ke tabel Client
	ClientID *string `gorm:"type:varchar(36);index" json:"client_id"`

	FullName  string         `gorm:"type:varchar(120)" json:"full_name"`
	Username  string         `gorm:"uniqueIndex;not null" json:"username"`
	Password  string         `gorm:"not null" json:"-"`
	Role      string         `gorm:"not null;default:'Auditor'" json:"role"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (u *User) BeforeCreate(tx *gorm.DB) (err error) {
	if u.ID == "" {
		u.ID = uuid.New().String()
	}
	return
}
