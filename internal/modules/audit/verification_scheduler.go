package audit

import (
	"context"
	"errors"
	"log"
	"time"

	"go-blockchain-api/internal/models"

	"gorm.io/gorm"
)

// VerificationSchedulerConfig controls the opt-in backend scheduler. The
// scheduler creates durable verification runs; the existing worker performs
// the actual batch verification.
type VerificationSchedulerConfig struct {
	Enabled    bool
	ClientID   string
	Interval   time.Duration
	Lookback   time.Duration
	Overlap    time.Duration
	BatchSize  int
	Location   *time.Location
	RunOnStart bool
}

// VerificationScheduler creates one range run per active client on each tick.
// It intentionally has no HTTP dependency, so validation continues when both
// dashboards are closed.
type VerificationScheduler struct {
	db     *gorm.DB
	jobs   *VerificationJobService
	config VerificationSchedulerConfig
}

func NewVerificationScheduler(db *gorm.DB, jobs *VerificationJobService, config VerificationSchedulerConfig) *VerificationScheduler {
	location := config.Location
	if location == nil {
		location = time.UTC
	}
	if config.Interval <= 0 {
		config.Interval = 24 * time.Hour
	}
	if config.Lookback <= 0 {
		config.Lookback = config.Interval
	}
	if config.Overlap < 0 {
		config.Overlap = 0
	}
	config.Location = location

	return &VerificationScheduler{db: db, jobs: jobs, config: config}
}

func (s *VerificationScheduler) Run(ctx context.Context) {
	if s == nil || !s.config.Enabled {
		log.Println("[VerificationScheduler] disabled")
		return
	}
	if s.db == nil || s.jobs == nil {
		log.Println("[VerificationScheduler] disabled: database atau worker tidak tersedia")
		return
	}

	log.Printf(
		"[VerificationScheduler] aktif; client_scope=%s interval=%s lookback=%s overlap=%s timezone=%s batch=%d",
		schedulerClientScope(s.config.ClientID),
		s.config.Interval,
		s.config.Lookback,
		s.config.Overlap,
		s.config.Location,
		s.config.BatchSize,
	)

	if s.config.RunOnStart {
		s.enqueueDueRuns(ctx)
	}

	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[VerificationScheduler] berhenti")
			return
		case <-ticker.C:
			s.enqueueDueRuns(ctx)
		}
	}
}

func (s *VerificationScheduler) enqueueDueRuns(ctx context.Context) {
	clientIDs, err := s.activeClientIDs(ctx)
	if err != nil {
		log.Printf("[VerificationScheduler] gagal membaca client aktif: %v", err)
		return
	}

	now := time.Now().In(s.config.Location)
	for _, clientID := range clientIDs {
		if err := ctx.Err(); err != nil {
			return
		}

		active, err := s.hasActiveRun(ctx, clientID)
		if err != nil {
			log.Printf("[VerificationScheduler] gagal mengecek run aktif client=%s: %v", clientID, err)
			continue
		}
		if active {
			log.Printf("[VerificationScheduler] melewati client=%s karena masih ada run aktif", clientID)
			continue
		}

		latest, err := s.latestCompletedRun(ctx, clientID)
		if err != nil {
			log.Printf("[VerificationScheduler] gagal membaca cursor client=%s: %v", clientID, err)
			continue
		}

		from, to := calculateVerificationRange(now, latest, s.config.Lookback, s.config.Overlap)
		run, err := s.jobs.Enqueue(from, to, clientID, "system:scheduler", s.config.BatchSize)
		if err != nil {
			log.Printf("[VerificationScheduler] gagal enqueue client=%s range=%s..%s: %v", clientID, from.Format(time.RFC3339), to.Format(time.RFC3339), err)
			continue
		}
		log.Printf("[VerificationScheduler] enqueue client=%s run=%s range=%s..%s", clientID, run.ID, from.Format(time.RFC3339), to.Format(time.RFC3339))
	}
}

func (s *VerificationScheduler) activeClientIDs(ctx context.Context) ([]string, error) {
	var clientIDs []string
	query := s.db.WithContext(ctx).
		Model(&models.Client{}).
		Where("status = ?", "active")
	if s.config.ClientID != "" {
		query = query.Where("id = ?", s.config.ClientID)
	}
	err := query.Order("id ASC").Pluck("id", &clientIDs).Error
	return clientIDs, err
}

func (s *VerificationScheduler) hasActiveRun(ctx context.Context, clientID string) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Model(&models.VerificationRun{}).
		Where("client_id = ? AND status IN ?", clientID, []string{
			models.VerificationRunQueued,
			models.VerificationRunRunning,
		}).Count(&count).Error
	return count > 0, err
}

func (s *VerificationScheduler) latestCompletedRun(ctx context.Context, clientID string) (*models.VerificationRun, error) {
	var run models.VerificationRun
	err := s.db.WithContext(ctx).
		Where("client_id = ? AND status = ?", clientID, models.VerificationRunCompleted).
		Order("to_time DESC, completed_at DESC, created_at DESC").
		First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &run, err
}

func calculateVerificationRange(now time.Time, latest *models.VerificationRun, lookback, overlap time.Duration) (time.Time, time.Time) {
	to := now.UTC()
	from := to.Add(-lookback)
	if latest != nil {
		from = latest.ToTime.UTC().Add(-overlap)
		if !from.Before(to) {
			from = to.Add(-lookback)
		}
	}
	return from, to
}

func schedulerClientScope(clientID string) string {
	if clientID == "" {
		return "all-active"
	}
	return clientID
}
