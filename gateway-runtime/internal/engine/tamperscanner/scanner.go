package tamperscanner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"gorm.io/gorm"

	"go-blockchain-api/internal/models"
)

// Verifier is intentionally narrower than the dashboard audit service. The
// scanner must verify only the gateway PostgreSQL row and Fabric anchor; it
// must never contact a client database or Agent.
type Verifier interface {
	VerifyGatewayIntegrity(logID, clientID string) (string, error)
}

// BatchVerificationResult is returned by the optional batch verifier. A
// batch verifier lets the audit module cache one Fabric anchor read for all
// logs that share the same anchor during a scanner cycle.
type BatchVerificationResult struct {
	Status string
	Err    error
}

type BatchVerifier interface {
	VerifyGatewayIntegrityBatch(logs []models.AuditLog) map[string]BatchVerificationResult
}

type Config struct {
	Enabled        bool
	Interval       time.Duration
	BatchSize      int
	Concurrency    int
	RecoveryCutoff *time.Time
}

type Worker struct {
	db       *gorm.DB
	verifier Verifier
	cfg      Config
}

func New(db *gorm.DB, verifier Verifier, cfg Config) (*Worker, error) {
	if db == nil {
		return nil, errors.New("tamper scanner membutuhkan database")
	}
	if verifier == nil {
		return nil, errors.New("tamper scanner membutuhkan verifier")
	}
	if cfg.Interval <= 0 {
		return nil, errors.New("interval tamper scanner harus positif")
	}
	if cfg.BatchSize <= 0 {
		return nil, errors.New("batch size tamper scanner harus positif")
	}
	if cfg.Concurrency <= 0 {
		return nil, errors.New("concurrency tamper scanner harus positif")
	}
	return &Worker{db: db, verifier: verifier, cfg: cfg}, nil
}

func (w *Worker) Run(ctx context.Context) {
	if !w.cfg.Enabled {
		return
	}

	if err := w.Scan(ctx); err != nil {
		log.Printf("⚠️ [TamperScanner] scan awal gagal: %v", err)
	}
	ticker := time.NewTicker(w.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.Scan(ctx); err != nil {
				log.Printf("⚠️ [TamperScanner] scan gagal: %v", err)
			}
		}
	}
}

// Scan verifies the least-recently checked anchored logs. A single failing
// log does not stop the remainder of the batch; the returned error is only a
// signal for operational logging.
func (w *Worker) Scan(ctx context.Context) error {
	var logs []models.AuditLog
	// Legacy RECOVERY rows may still exist in audit_logs for forensics, but
	// they are no longer client-originated audit records and must not create
	// normal tamper incidents. New recovery executions live in recovery_events.
	query := w.db.WithContext(ctx).Where("status = ? AND COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY'", "ANCHORED")
	if w.cfg.RecoveryCutoff != nil {
		// Legacy history remains available for manual integrity checks, but
		// the scheduled recovery scanner is scoped to snapshot-backed rows.
		query = query.Where("db_timestamp IS NOT NULL AND db_timestamp >= ?", *w.cfg.RecoveryCutoff)
	}
	if err := query.
		Order("integrity_checked_at ASC NULLS FIRST, blockchain_timestamp ASC NULLS FIRST, log_id ASC").
		Limit(w.cfg.BatchSize).
		Find(&logs).Error; err != nil {
		return fmt.Errorf("ambil batch tamper scanner gagal: %w", err)
	}

	if len(logs) == 0 {
		return nil
	}
	if batchVerifier, ok := w.verifier.(BatchVerifier); ok {
		return w.persistBatchResults(logs, batchVerifier.VerifyGatewayIntegrityBatch(logs))
	}

	sem := make(chan struct{}, w.cfg.Concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var scanErrors []error

	for _, logRow := range logs {
		if err := ctx.Err(); err != nil {
			return err
		}
		row := logRow
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			status, err := w.verifier.VerifyGatewayIntegrity(row.LogID, row.ClientID)
			if err != nil {
				message := err.Error()
				if updateErr := w.mark(row, models.IntegrityStatusUnreachable, message); updateErr != nil {
					message = message + "; persist status gagal: " + updateErr.Error()
				}
				mu.Lock()
				scanErrors = append(scanErrors, fmt.Errorf("%s/%s: %s", row.ClientID, row.LogID, message))
				mu.Unlock()
				return
			}
			if updateErr := w.mark(row, status, ""); updateErr != nil {
				mu.Lock()
				scanErrors = append(scanErrors, fmt.Errorf("%s/%s: persist status gagal: %w", row.ClientID, row.LogID, updateErr))
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	if len(scanErrors) > 0 {
		return errors.Join(scanErrors...)
	}
	return nil
}

func (w *Worker) persistBatchResults(logs []models.AuditLog, results map[string]BatchVerificationResult) error {
	var scanErrors []error
	for _, row := range logs {
		result, ok := results[row.LogID]
		if !ok {
			result = BatchVerificationResult{
				Status: models.IntegrityStatusUnreachable,
				Err:    errors.New("verifier tidak mengembalikan hasil untuk log"),
			}
		}
		status := result.Status
		message := ""
		if result.Err != nil {
			status = models.IntegrityStatusUnreachable
			message = result.Err.Error()
		}
		if err := w.mark(row, status, message); err != nil {
			scanErrors = append(scanErrors, fmt.Errorf("%s/%s: persist status gagal: %w", row.ClientID, row.LogID, err))
		}
		if result.Err != nil {
			scanErrors = append(scanErrors, fmt.Errorf("%s/%s: %w", row.ClientID, row.LogID, result.Err))
		}
	}
	if len(scanErrors) > 0 {
		return errors.Join(scanErrors...)
	}
	return nil
}

func (w *Worker) mark(row models.AuditLog, status, message string) error {
	if status == "" {
		status = models.IntegrityStatusUnreachable
	}
	now := time.Now().UTC()
	return w.db.Model(&models.AuditLog{}).
		Where("log_id = ? AND client_id = ?", row.LogID, row.ClientID).
		Updates(map[string]interface{}{
			"integrity_status":     status,
			"integrity_checked_at": now,
			"integrity_error":      message,
			"integrity_source":     models.IntegritySourceTamperScanner,
			"integrity_run_id":     "",
		}).Error
}
