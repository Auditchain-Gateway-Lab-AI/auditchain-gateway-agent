package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ClientKafkaConfig menyimpan konfigurasi Kafka per klien
// TopicPrefix digunakan untuk menentukan topic mana milik klien ini
type ClientKafkaConfig struct {
	ID           string `gorm:"primaryKey;type:varchar(36)" json:"id"`
	ClientID     string `gorm:"type:varchar(36);not null;uniqueIndex" json:"client_id"`
	TopicPrefix  string `gorm:"type:varchar(100);not null" json:"topic_prefix"`
	KafkaBrokers string `gorm:"type:varchar(255);not null" json:"kafka_brokers"`
	SourceSystem string `gorm:"type:varchar(100);not null" json:"source_system"`
	ActorField   string `gorm:"type:varchar(100);default:'__user_name'" json:"actor_field"`
	PKField      string `gorm:"type:varchar(100);default:'ID'" json:"pk_field"`
	DBEngine     string `gorm:"type:varchar(50)" json:"db_engine"`
	IsActive     bool   `gorm:"default:true" json:"is_active"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (c *ClientKafkaConfig) BeforeCreate(tx *gorm.DB) (err error) {
	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	return
}
