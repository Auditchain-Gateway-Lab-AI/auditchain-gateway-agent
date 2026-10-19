package audit

import (
	"context"
	"log"
	"time"
)

// RunnerConfig controls the background audit execution.
type RunnerConfig struct {
	Enabled      bool
	Interval     time.Duration
	Window       time.Duration
	AutoRecover  bool
}

// Runner periodically executes audit sweeps in the background.
type Runner struct {
	cfg     RunnerConfig
	service *Service
}

// NewRunner creates a background audit runner.
func NewRunner(cfg RunnerConfig, service *Service) *Runner {
	if cfg.Interval <= 0 {
		cfg.Interval = 1 * time.Hour
	}
	if cfg.Window <= 0 {
		cfg.Window = 24 * time.Hour
	}
	return &Runner{
		cfg:     cfg,
		service: service,
	}
}

// Start launches the periodic audit loop. It blocks until ctx is canceled.
func (r *Runner) Start(ctx context.Context) {
	if !r.cfg.Enabled {
		log.Println("[AuditRunner] Background audit scheduler is disabled")
		return
	}

	log.Printf("[AuditRunner] Background audit scheduler aktif (interval=%v, window=%v, auto_recover=%t)",
		r.cfg.Interval, r.cfg.Window, r.cfg.AutoRecover)

	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()

	// Run initial audit after a brief startup delay
	select {
	case <-time.After(10 * time.Second):
		r.runAuditOnce(ctx)
	case <-ctx.Done():
		return
	}

	for {
		select {
		case <-ticker.C:
			r.runAuditOnce(ctx)
		case <-ctx.Done():
			log.Println("[AuditRunner] Menghentikan background audit runner")
			return
		}
	}
}

func (r *Runner) runAuditOnce(ctx context.Context) {
	to := time.Now()
	from := to.Add(-r.cfg.Window)

	log.Printf("[AuditRunner] Memulai audit berkala dari %s hingga %s...", from.Format(time.RFC3339), to.Format(time.RFC3339))
	report, err := r.service.AuditTimeRange(ctx, from, to, r.cfg.AutoRecover)
	if err != nil {
		log.Printf("[AuditRunner] Gagal menjalankan audit: %v", err)
		return
	}

	log.Printf("[AuditRunner] Audit selesai dalam %s. TotalLogs: %d, Audited: %d, SkippedHist: %d, DataValid: %d, DataTampered: %d, ChainValid: %d, ChainTampered: %d, ChainPending: %d, AutoRecovered: %d",
		report.Duration,
		report.TotalLogs,
		report.AuditedLogs,
		report.HistoricalSkipped,
		report.DataValid,
		report.DataTampered,
		report.BlockchainValid,
		report.BlockchainTampered,
		report.BlockchainPending,
		report.AutoRecoveredCount,
	)

	// Kirim hasil audit ke auditchain-api Gateway untuk memperbarui statistik dashboard
	if err := r.service.SubmitReport(ctx, report); err != nil {
		log.Printf("[AuditRunner] Gagal mengirim audit report ke Gateway: %v", err)
	} else {
		log.Printf("[AuditRunner] Berhasil mengirim audit report ke Gateway (RunID: %s)", report.RunID)
	}
}
