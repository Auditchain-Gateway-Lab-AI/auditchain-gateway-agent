package snapshotstore

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"go-blockchain-api/internal/models"
)

const SnapshotEventType = "STORE_AUDIT_SNAPSHOT"
const RecoveryEventSnapshotEventType = "STORE_RECOVERY_EVENT_SNAPSHOT"

type OutboxBuilder interface {
	Build(log models.AuditLog) (*models.SnapshotOutbox, error)
}

type RecoveryEventOutboxBuilder interface {
	BuildRecoveryEvent(event models.RecoveryEvent) (*models.SnapshotOutbox, error)
}

type AuditSnapshotOutboxBuilder struct {
	Cipher *Cipher
	Now    func() time.Time
}

type RecoveryEventSnapshotOutboxBuilder struct {
	Cipher *Cipher
	Now    func() time.Time
}

func (b AuditSnapshotOutboxBuilder) Build(log models.AuditLog) (*models.SnapshotOutbox, error) {
	if b.Cipher == nil {
		return nil, fmt.Errorf("snapshot cipher belum dikonfigurasi")
	}
	now := time.Now
	if b.Now != nil {
		now = b.Now
	}

	plaintext, err := NewAuditSnapshot(log, now())
	if err != nil {
		return nil, err
	}
	encrypted, err := b.Cipher.Encrypt(plaintext)
	if err != nil {
		return nil, fmt.Errorf("enkripsi snapshot log %s gagal: %w", log.LogID, err)
	}

	return &models.SnapshotOutbox{
		ID:           uuid.NewString(),
		LogID:        log.LogID,
		ClientID:     log.ClientID,
		EventType:    SnapshotEventType,
		Payload:      encrypted,
		PayloadHash:  log.HashValue,
		Status:       models.OutboxStatusPending,
		AttemptCount: 0,
		CreatedAt:    now().UTC(),
	}, nil
}

func (b RecoveryEventSnapshotOutboxBuilder) BuildRecoveryEvent(event models.RecoveryEvent) (*models.SnapshotOutbox, error) {
	if b.Cipher == nil {
		return nil, fmt.Errorf("snapshot cipher belum dikonfigurasi")
	}
	now := time.Now
	if b.Now != nil {
		now = b.Now
	}
	plaintext, err := NewRecoveryEventSnapshot(event, now())
	if err != nil {
		return nil, err
	}
	encrypted, err := b.Cipher.Encrypt(plaintext)
	if err != nil {
		return nil, fmt.Errorf("enkripsi snapshot recovery event %s gagal: %w", event.ID, err)
	}
	return &models.SnapshotOutbox{
		ID:           uuid.NewString(),
		LogID:        event.ID,
		ClientID:     event.ClientID,
		EventType:    RecoveryEventSnapshotEventType,
		Payload:      encrypted,
		PayloadHash:  event.EventHash,
		Status:       models.OutboxStatusPending,
		AttemptCount: 0,
		CreatedAt:    now().UTC(),
	}, nil
}
