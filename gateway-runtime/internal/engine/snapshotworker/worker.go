package snapshotworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"go-blockchain-api/internal/models"
	"go-blockchain-api/internal/storage/snapshotstore"
)

const (
	defaultPollInterval = 2 * time.Second
	defaultLeaseTimeout = 2 * time.Minute
	defaultMaxAttempts  = 10
	defaultRetryBase    = 5 * time.Second
)

type Config struct {
	PollInterval time.Duration
	LeaseTimeout time.Duration
	MaxAttempts  int
	RetryBase    time.Duration
	Concurrency  int
	Environment  string
}

type Worker struct {
	DB    *gorm.DB
	Store snapshotstore.SnapshotStore
	Config
	workerID string
	now      func() time.Time
}

func New(db *gorm.DB, store snapshotstore.SnapshotStore, cfg Config) (*Worker, error) {
	if db == nil {
		return nil, fmt.Errorf("snapshot worker membutuhkan database")
	}
	if store == nil {
		return nil, fmt.Errorf("snapshot worker membutuhkan snapshot store")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.LeaseTimeout <= 0 {
		cfg.LeaseTimeout = defaultLeaseTimeout
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = defaultMaxAttempts
	}
	if cfg.RetryBase <= 0 {
		cfg.RetryBase = defaultRetryBase
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	if cfg.Concurrency > 32 {
		cfg.Concurrency = 32
	}
	if strings.TrimSpace(cfg.Environment) == "" {
		cfg.Environment = "local"
	}
	return &Worker{
		DB:       db,
		Store:    store,
		Config:   cfg,
		workerID: fmt.Sprintf("snapshot-worker-%d", os.Getpid()),
		now:      time.Now,
	}, nil
}

func (w *Worker) Run(ctx context.Context) {
	if err := w.requeueExpiredClaims(ctx); err != nil {
		log.Printf("[SnapshotWorker] requeue claim lama gagal: %v", err)
	}
	var workers sync.WaitGroup
	for i := 0; i < w.Concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			w.runLoop(ctx)
		}()
	}
	workers.Wait()
}

func (w *Worker) runLoop(ctx context.Context) {
	ticker := time.NewTicker(w.PollInterval)
	defer ticker.Stop()
	log.Printf("[SnapshotWorker] aktif; environment=%s poll=%s concurrency=%d", w.Environment, w.PollInterval, w.Concurrency)

	for {
		select {
		case <-ctx.Done():
			log.Println("[SnapshotWorker] berhenti")
			return
		case <-ticker.C:
			if err := w.processOne(ctx); err != nil {
				log.Printf("[SnapshotWorker] proses outbox gagal: %v", err)
			}
		}
	}
}

func (w *Worker) processOne(ctx context.Context) error {
	outbox, found, err := w.claimNext(ctx)
	if err != nil || !found {
		return err
	}

	// Rows created before event_type was introduced are audit snapshots. Keep
	// them processable during a rolling deployment instead of dead-lettering
	// them as an unknown type.
	eventType := outbox.EventType
	if eventType == "" {
		eventType = snapshotstore.SnapshotEventType
	}
	key := BuildObjectKey(w.Environment, outbox.ClientID, outbox.CreatedAt, outbox.LogID, outbox.PayloadHash)
	if eventType == snapshotstore.RecoveryEventSnapshotEventType {
		key = BuildRecoveryEventObjectKey(w.Environment, outbox.ClientID, outbox.CreatedAt, outbox.LogID, outbox.PayloadHash)
	}
	recordType := "audit-log"
	if eventType == snapshotstore.RecoveryEventSnapshotEventType {
		recordType = "recovery-event"
	}
	metadata := map[string]string{
		"record-id":      outbox.LogID,
		"record-type":    recordType,
		"log-id":         outbox.LogID,
		"client-id":      outbox.ClientID,
		"payload-hash":   outbox.PayloadHash,
		"schema-version": "1",
	}
	info, err := w.putIdempotent(ctx, key, outbox.Payload, metadata)
	if err == nil && info.VersionID == "" {
		err = fmt.Errorf("MinIO tidak mengembalikan version ID untuk object %q", key)
	}
	if err == nil {
		var stored []byte
		stored, _, err = w.Store.GetVersion(ctx, key, info.VersionID)
		if err == nil && checksum(stored) != info.ChecksumSHA {
			err = fmt.Errorf("checksum snapshot %q berubah setelah upload", key)
		}
	}
	if err != nil {
		return w.markFailure(outbox, err)
	}
	return w.markSuccess(outbox, info)
}

func (w *Worker) claimNext(ctx context.Context) (*models.SnapshotOutbox, bool, error) {
	var outbox models.SnapshotOutbox
	now := w.now().UTC()
	leaseCutoff := now.Add(-w.LeaseTimeout)
	err := w.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// A crashed worker may leave PROCESSING rows behind. They become
		// eligible again after the lease expires.
		if err := tx.Model(&models.SnapshotOutbox{}).
			Where("status = ? AND locked_at IS NOT NULL AND locked_at < ?", models.OutboxStatusProcessing, leaseCutoff).
			Updates(map[string]interface{}{
				"status":          models.OutboxStatusRetry,
				"locked_at":       nil,
				"locked_by":       "",
				"next_attempt_at": now,
			}).Error; err != nil {
			return err
		}

		query := tx.Where("status IN ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)",
			[]string{models.OutboxStatusPending, models.OutboxStatusRetry}, now).
			Order("created_at ASC").
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		if err := query.First(&outbox).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return err
		}

		return tx.Model(&models.SnapshotOutbox{}).Where("id = ?", outbox.ID).Updates(map[string]interface{}{
			"status":    models.OutboxStatusProcessing,
			"locked_at": now,
			"locked_by": w.workerID,
		}).Error
	})
	if err != nil {
		return nil, false, err
	}
	if outbox.ID == "" {
		return nil, false, nil
	}
	return &outbox, true, nil
}

func (w *Worker) putIdempotent(ctx context.Context, key string, payload []byte, metadata map[string]string) (snapshotstore.ObjectInfo, error) {
	if latestStore, ok := w.Store.(snapshotstore.LatestObjectStore); ok {
		latest, err := latestStore.StatLatest(ctx, key)
		if err == nil {
			if latest.VersionID == "" {
				return snapshotstore.ObjectInfo{}, fmt.Errorf("object %q sudah ada tanpa version ID", key)
			}
			stored, verified, getErr := latestStore.GetVersion(ctx, key, latest.VersionID)
			if getErr != nil {
				return snapshotstore.ObjectInfo{}, fmt.Errorf("memverifikasi object lama %q gagal: %w", key, getErr)
			}
			if checksum(stored) != checksum(payload) {
				return snapshotstore.ObjectInfo{}, fmt.Errorf("object key %q sudah dipakai payload berbeda", key)
			}
			return verified, nil
		}
		if !snapshotstore.IsNotFound(err) {
			return snapshotstore.ObjectInfo{}, err
		}
	}
	return w.Store.Put(ctx, key, payload, metadata)
}

func (w *Worker) markSuccess(outbox *models.SnapshotOutbox, info snapshotstore.ObjectInfo) error {
	now := w.now().UTC()
	return w.DB.Transaction(func(tx *gorm.DB) error {
		updates := map[string]interface{}{
			"snapshot_status":         models.SnapshotStatusVerified,
			"snapshot_object_key":     info.Key,
			"snapshot_version_id":     info.VersionID,
			"snapshot_checksum":       info.ChecksumSHA,
			"snapshot_plaintext_hash": outbox.PayloadHash,
			"snapshot_stored_at":      now,
			"snapshot_verified_at":    now,
			"snapshot_last_error":     "",
		}
		var result *gorm.DB
		if outbox.EventType == snapshotstore.RecoveryEventSnapshotEventType {
			result = tx.Model(&models.RecoveryEvent{}).Where("id = ?", outbox.LogID).Updates(updates)
		} else if outbox.EventType == "" || outbox.EventType == snapshotstore.SnapshotEventType {
			result = tx.Model(&models.AuditLog{}).Where("log_id = ?", outbox.LogID).Updates(updates)
		} else {
			return fmt.Errorf("event type snapshot tidak dikenal: %s", outbox.EventType)
		}
		if result.Error != nil {
			return fmt.Errorf("update snapshot reference gagal: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("record %s tidak ditemukan saat menandai snapshot selesai", outbox.LogID)
		}
		if err := tx.Model(&models.SnapshotOutbox{}).Where("id = ?", outbox.ID).Updates(map[string]interface{}{
			"status":       models.OutboxStatusCompleted,
			"processed_at": now,
			"locked_at":    nil,
			"locked_by":    "",
			"last_error":   "",
		}).Error; err != nil {
			return fmt.Errorf("update snapshot outbox selesai gagal: %w", err)
		}
		return nil
	})
}

func (w *Worker) markFailure(outbox *models.SnapshotOutbox, failure error) error {
	now := w.now().UTC()
	attempt := outbox.AttemptCount + 1
	status := models.OutboxStatusRetry
	nextAttempt := now.Add(retryDelay(w.RetryBase, attempt))
	if attempt >= w.MaxAttempts {
		status = models.OutboxStatusDeadLetter
		nextAttempt = now
	}
	errorText := sanitizeError(failure)
	return w.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.SnapshotOutbox{}).Where("id = ?", outbox.ID).Updates(map[string]interface{}{
			"status":          status,
			"attempt_count":   attempt,
			"next_attempt_at": nextAttempt,
			"locked_at":       nil,
			"locked_by":       "",
			"last_error":      errorText,
		}).Error; err != nil {
			return err
		}
		logStatus := models.SnapshotStatusRetry
		if status == models.OutboxStatusDeadLetter {
			logStatus = models.SnapshotStatusFailed
		}
		updates := map[string]interface{}{
			"snapshot_status":     logStatus,
			"snapshot_last_error": errorText,
		}
		if outbox.EventType == snapshotstore.RecoveryEventSnapshotEventType {
			return tx.Model(&models.RecoveryEvent{}).Where("id = ?", outbox.LogID).Updates(updates).Error
		}
		if outbox.EventType != "" && outbox.EventType != snapshotstore.SnapshotEventType {
			return fmt.Errorf("event type snapshot tidak dikenal: %s", outbox.EventType)
		}
		return tx.Model(&models.AuditLog{}).Where("log_id = ?", outbox.LogID).Updates(updates).Error
	})
}

func (w *Worker) requeueExpiredClaims(ctx context.Context) error {
	cutoff := w.now().UTC().Add(-w.LeaseTimeout)
	return w.DB.WithContext(ctx).Model(&models.SnapshotOutbox{}).
		Where("status = ? AND locked_at IS NOT NULL AND locked_at < ?", models.OutboxStatusProcessing, cutoff).
		Updates(map[string]interface{}{
			"status":          models.OutboxStatusRetry,
			"next_attempt_at": w.now().UTC(),
			"locked_at":       nil,
			"locked_by":       "",
		}).Error
}

var unsafeObjectKeyChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func BuildObjectKey(environment, clientID string, createdAt time.Time, logID, payloadHash string) string {
	environment = sanitizeSegment(environment, "local")
	clientID = sanitizeSegment(clientID, "unknown-client")
	logID = sanitizeSegment(logID, "unknown-log")
	payloadHash = sanitizeSegment(payloadHash, "unknown-hash")
	return fmt.Sprintf("%s/%s/%04d/%02d/%02d/%s/%s.snapshot",
		environment, clientID, createdAt.UTC().Year(), createdAt.UTC().Month(), createdAt.UTC().Day(), logID, payloadHash)
}

func BuildRecoveryEventObjectKey(environment, clientID string, createdAt time.Time, eventID, eventHash string) string {
	environment = sanitizeSegment(environment, "local")
	clientID = sanitizeSegment(clientID, "unknown-client")
	eventID = sanitizeSegment(eventID, "unknown-event")
	eventHash = sanitizeSegment(eventHash, "unknown-hash")
	return fmt.Sprintf("%s/%s/recovery-events/%04d/%02d/%02d/%s/%s.snapshot",
		environment, clientID, createdAt.UTC().Year(), createdAt.UTC().Month(), createdAt.UTC().Day(), eventID, eventHash)
}

func sanitizeSegment(value, fallback string) string {
	value = unsafeObjectKeyChars.ReplaceAllString(strings.TrimSpace(value), "-")
	value = strings.Trim(value, "/.")
	if value == "" {
		return fallback
	}
	return value
}

func retryDelay(base time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for i := 1; i < attempt; i++ {
		if delay >= 15*time.Minute {
			return 30 * time.Minute
		}
		delay *= 2
	}
	if delay > 30*time.Minute {
		return 30 * time.Minute
	}
	return delay
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 1000 {
		message = message[:1000]
	}
	return message
}

func checksum(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
