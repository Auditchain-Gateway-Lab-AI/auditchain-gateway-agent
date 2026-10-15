package audit

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

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

// VerificationJobService exposes durable range-verification enqueue and read
// operations to the portal API. The Besu backend owns background processing;
// PostgreSQL keeps the queue and progress shared across runtime instances.
type VerificationJobService struct {
	db        *gorm.DB
	audit     Service
	batchSize int
}

func NewVerificationJobService(db *gorm.DB, audit Service) *VerificationJobService {
	batchSize := configuredVerificationBatchSize()
	return &VerificationJobService{
		db:        db,
		audit:     audit,
		batchSize: batchSize,
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
