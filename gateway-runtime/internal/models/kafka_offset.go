package models

import "time"

// KafkaOffset menyimpan offset message Kafka per log
// digunakan untuk verifikasi Lapis 3 — query balik ke Kafka
type KafkaOffset struct {
	ID        uint      `gorm:"primaryKey"`
	LogID     string    `gorm:"type:varchar(100);uniqueIndex;not null"`
	Topic     string    `gorm:"type:varchar(255);not null"`
	Partition int32     `gorm:"not null"`
	Offset    int64     `gorm:"not null"`
	CreatedAt time.Time `json:"created_at"`
}
