package audit

import (
	"context"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"go-blockchain-api/internal/models"
)

const (
	defaultVerificationBatchSize = 100
	maxVerificationBatchSize     = 500
)

// VerificationRunResponse is the API-safe projection of a background run.
// It intentionally contains summary/progress only; per-log results remain
// available through the gateway dashboard's normal log endpoints.
type VerificationRunResponse struct {
	ID              string     `json:"id"`
	ClientID        string     `json:"client_id"`
	From            string     `json:"from"`
	To              string     `json:"to"`
	Status          string     `json:"status"`
	BatchSize       int        `json:"batch_size"`
	TotalItems      int64      `json:"total_items"`
	ProcessedItems  int64      `json:"processed_items"`
	ProgressPercent float64    `json:"progress_percent"`
	TotalValid      int64      `json:"total_valid"`
	TotalInvalid    int64      `json:"total_invalid"`
	TotalPending    int64      `json:"total_pending"`
	AlreadyVerified int64      `json:"already_verified"`
	VerifiedNow     int64      `json:"verified_now"`
	ErrorMessage    string     `json:"error_message,omitempty"`
	RequestedBy     string     `json:"requested_by,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// VerificationJobService owns durable, batch-oriented range verification.
// Jobs are stored in PostgreSQL so a process restart can safely requeue a run;
// per-log integrity markers make replaying a batch idempotent.
type VerificationJobService struct {
	db        *gorm.DB
	audit     Service
	batchSize int
	wake      chan struct{}
}

func NewVerificationJobService(db *gorm.DB, audit Service) *VerificationJobService {
	batchSize := configuredVerificationBatchSize()
	return &VerificationJobService{
		db:        db,
		audit:     audit,
		batchSize: batchSize,
		wake:      make(chan struct{}, 1),
	}
}

func configuredVerificationBatchSize() int {
	raw := strings.TrimSpace(os.Getenv("VERIFICATION_JOB_BATCH_SIZE"))
	if raw == "" {
		return defaultVerificationBatchSize
	}
	size, err := strconv.Atoi(raw)
	if err != nil || size <= 0 {
		return defaultVerificationBatchSize
	}
	if size > maxVerificationBatchSize {
		return maxVerificationBatchSize
	}
	return size
}

func integritySourceForVerificationRun(requestedBy string) string {
	if strings.EqualFold(strings.TrimSpace(requestedBy), "system:scheduler") {
		return models.IntegritySourceScheduledRun
	}
	return models.IntegritySourceBackgroundRun
}

// Run starts a single process worker. PostgreSQL row locking prevents two
// gateway instances from claiming the same queued run.
func (s *VerificationJobService) Run(ctx context.Context) {
	if s == nil || s.db == nil || s.audit == nil {
		return
	}

	if err := s.requeueInterruptedRuns(); err != nil {
		log.Printf("[VerificationJob] gagal me-requeue run yang terputus: %v", err)
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	log.Printf("[VerificationJob] worker aktif; batch=%d", s.batchSize)

	for {
		select {
		case <-ctx.Done():
			log.Println("[VerificationJob] worker berhenti")
			return
		case <-ticker.C:
			s.processNext(ctx)
		case <-s.wake:
			s.processNext(ctx)
		}
	}
}

func (s *VerificationJobService) Enqueue(from, to time.Time, clientID, requestedBy string, batchSize int) (*models.VerificationRun, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("verification job service tidak tersedia")
	}
	if clientID == "" {
		return nil, errors.New("client_id wajib diisi")
	}
	if batchSize <= 0 {
		batchSize = s.batchSize
	}
	if batchSize > maxVerificationBatchSize {
		batchSize = maxVerificationBatchSize
	}

	var active models.VerificationRun
	err := s.db.Where(
		"client_id = ? AND from_time = ? AND to_time = ? AND status IN ?",
		clientID,
		from,
		to,
		[]string{models.VerificationRunQueued, models.VerificationRunRunning},
	).Order("created_at DESC").First(&active).Error
	if err == nil {
		signal := s.wake
		select {
		case signal <- struct{}{}:
		default:
		}
		return &active, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	totalItems, err := s.audit.EstimateLogRange(from, to, clientID)
	if err != nil {
		return nil, err
	}

	run := &models.VerificationRun{
		ID:          uuid.NewString(),
		ClientID:    clientID,
		RequestedBy: requestedBy,
		FromTime:    from,
		ToTime:      to,
		Status:      models.VerificationRunQueued,
		BatchSize:   batchSize,
		TotalItems:  totalItems,
	}
	if err := s.db.Create(run).Error; err != nil {
		// A concurrent enqueue can win the partial unique active-range index.
		// Return that existing run instead of exposing a duplicate-job failure.
		if lookupErr := s.db.Where(
			"client_id = ? AND from_time = ? AND to_time = ? AND status IN ?",
			clientID,
			from,
			to,
			[]string{models.VerificationRunQueued, models.VerificationRunRunning},
		).Order("created_at DESC").First(&active).Error; lookupErr == nil {
			return &active, nil
		}
		return nil, err
	}

	select {
	case s.wake <- struct{}{}:
	default:
	}
	return run, nil
}

func (s *VerificationJobService) GetRun(id, clientID string) (*models.VerificationRun, error) {
	var run models.VerificationRun
	err := s.db.Where("id = ? AND client_id = ?", id, clientID).First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, gorm.ErrRecordNotFound
	}
	return &run, err
}

func (s *VerificationJobService) GetLatestRun(clientID string) (*models.VerificationRun, error) {
	var run models.VerificationRun
	err := s.db.Where("client_id = ?", clientID).Order("created_at DESC").First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &run, err
}

func ToVerificationRunResponse(run *models.VerificationRun) VerificationRunResponse {
	progress := float64(0)
	if run.TotalItems > 0 {
		progress = float64(run.ProcessedItems) / float64(run.TotalItems) * 100
		if progress > 100 {
			progress = 100
		}
	}
	return VerificationRunResponse{
		ID:              run.ID,
		ClientID:        run.ClientID,
		From:            run.FromTime.UTC().Format(time.RFC3339Nano),
		To:              run.ToTime.UTC().Format(time.RFC3339Nano),
		Status:          run.Status,
		BatchSize:       run.BatchSize,
		TotalItems:      run.TotalItems,
		ProcessedItems:  run.ProcessedItems,
		ProgressPercent: progress,
		TotalValid:      run.TotalValid,
		TotalInvalid:    run.TotalInvalid,
		TotalPending:    run.TotalPending,
		AlreadyVerified: run.AlreadyVerified,
		VerifiedNow:     run.VerifiedNow,
		ErrorMessage:    run.ErrorMessage,
		RequestedBy:     run.RequestedBy,
		StartedAt:       run.StartedAt,
		CompletedAt:     run.CompletedAt,
		CreatedAt:       run.CreatedAt,
		UpdatedAt:       run.UpdatedAt,
	}
}

func (s *VerificationJobService) processNext(ctx context.Context) {
	run, claimed, err := s.claimNextRun()
	if err != nil {
		log.Printf("[VerificationJob] gagal mengambil queued run: %v", err)
		return
	}
	if !claimed {
		return
	}
	if err := s.processRun(ctx, run); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("[VerificationJob] run %s gagal: %v", run.ID, err)
		_ = s.markFailed(run.ID, err)
	}
}

func (s *VerificationJobService) claimNextRun() (*models.VerificationRun, bool, error) {
	var run models.VerificationRun
	err := s.db.Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ?", models.VerificationRunQueued).
			Order("created_at ASC").
			First(&run).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		return tx.Model(&run).Updates(map[string]interface{}{
			"status":        models.VerificationRunRunning,
			"started_at":    now,
			"error_message": "",
		}).Error
	})
	if err != nil {
		return nil, false, err
	}
	return &run, run.ID != "", nil
}

func (s *VerificationJobService) processRun(ctx context.Context, run *models.VerificationRun) error {
	if run == nil {
		return errors.New("verification run kosong")
	}

	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		requestID := "verification-run-" + run.ID
		integritySource := integritySourceForVerificationRun(run.RequestedBy)
		summary, loaded, err := s.audit.VerifyRangeBatch(
			run.FromTime,
			run.ToTime,
			run.ClientID,
			requestID,
			run.ID,
			integritySource,
			run.BatchSize,
			offset,
		)
		if err != nil {
			return err
		}
		if loaded == 0 {
			break
		}

		if err := s.updateProgress(run.ID, loaded, summary); err != nil {
			return err
		}
		offset += loaded
		if loaded < run.BatchSize {
			break
		}
	}

	now := time.Now().UTC()
	return s.db.Model(&models.VerificationRun{}).
		Where("id = ?", run.ID).
		Updates(map[string]interface{}{
			"status":          models.VerificationRunCompleted,
			"processed_items": gorm.Expr("GREATEST(processed_items, total_items)"),
			"completed_at":    now,
		}).Error
}

func (s *VerificationJobService) updateProgress(id string, loaded int, summary RangeSummary) error {
	return s.db.Model(&models.VerificationRun{}).Where("id = ?", id).Updates(map[string]interface{}{
		"processed_items":  gorm.Expr("processed_items + ?", loaded),
		"total_valid":      gorm.Expr("total_valid + ?", summary.Valid),
		"total_invalid":    gorm.Expr("total_invalid + ?", summary.Invalid),
		"total_pending":    gorm.Expr("total_pending + ?", summary.Pending),
		"already_verified": gorm.Expr("already_verified + ?", summary.AlreadyVerified),
		"verified_now":     gorm.Expr("verified_now + ?", summary.VerifiedNow),
	}).Error
}

func (s *VerificationJobService) markFailed(id string, runErr error) error {
	message := "verification job gagal"
	if runErr != nil {
		message = runErr.Error()
	}
	return s.db.Model(&models.VerificationRun{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":        models.VerificationRunFailed,
		"error_message": message,
	}).Error
}

func (s *VerificationJobService) requeueInterruptedRuns() error {
	return s.db.Model(&models.VerificationRun{}).
		Where("status = ?", models.VerificationRunRunning).
		Updates(map[string]interface{}{
			"status":           models.VerificationRunQueued,
			"processed_items":  0,
			"total_valid":      0,
			"total_invalid":    0,
			"total_pending":    0,
			"already_verified": 0,
			"verified_now":     0,
			"error_message":    "",
		}).Error
}
