// @title AuditChain Gateway API
// @version 1.0
// @description API Enterprise untuk sistem audit log berbasis Blockchain dan Merkle Tree.
// @termsOfService http://swagger.io/terms/
// @contact.name API Support
// @contact.email support@auditchain.local
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Masukkan token dengan format: Bearer {token}
// @host localhost:8080
// @BasePath /api
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"gorm.io/gorm"

	"go-blockchain-api/internal/api"
	"go-blockchain-api/internal/blockchain"
	"go-blockchain-api/internal/blockchain/agentverifier"
	"go-blockchain-api/internal/config"
	"go-blockchain-api/internal/engine/aggregator"
	"go-blockchain-api/internal/engine/hasher"
	"go-blockchain-api/internal/engine/kafkaconsumer"
	"go-blockchain-api/internal/engine/snapshotworker"
	"go-blockchain-api/internal/engine/tamperscanner"
	"go-blockchain-api/internal/modules/audit"
	"go-blockchain-api/internal/modules/auth"
	"go-blockchain-api/internal/modules/client"
	"go-blockchain-api/internal/modules/recovery"
	"go-blockchain-api/internal/modules/report"
	"go-blockchain-api/internal/storage/snapshotstore"
)

func snapshotWriterEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("SNAPSHOT_WRITER_ENABLED")), "true")
}

func snapshotRequiredForAnchor() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("SNAPSHOT_REQUIRED_FOR_ANCHOR")), "true")
}

func recoveryEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("RECOVERY_ENABLED")), "true")
}

func tamperScannerEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("TAMPER_SCANNER_ENABLED")), "true")
}

func configuredRecoveryMode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("RECOVERY_MODE")))
	if mode == "" {
		return "agent_direct"
	}
	return mode
}

func validateRecoveryScopeFlags(recovery, snapshotRequired bool, cutoff *time.Time, modes ...string) error {
	mode := "agent_direct"
	if len(modes) > 0 && strings.TrimSpace(modes[0]) != "" {
		mode = strings.ToLower(strings.TrimSpace(modes[0]))
	}
	if mode != "snapshot_legacy" && mode != "agent_direct" {
		return fmt.Errorf("RECOVERY_MODE tidak dikenal: %s", mode)
	}
	if mode != "agent_direct" && (recovery || snapshotRequired) && cutoff == nil {
		return fmt.Errorf("RECOVERY_CUTOFF_AT wajib diisi saat recovery atau anchoring gate aktif")
	}
	if mode != "agent_direct" && recovery && !snapshotRequired {
		return fmt.Errorf("RECOVERY_ENABLED=true membutuhkan SNAPSHOT_REQUIRED_FOR_ANCHOR=true")
	}
	return nil
}

func buildSnapshotRuntime(recoveryMode string) (snapshotstore.OutboxBuilder, snapshotstore.SnapshotStore, *snapshotstore.Cipher, error) {
	// agent_direct writes through the client Agent and must remain independent
	// of stale snapshot/MinIO flags left in an older environment file.
	if recoveryMode == "agent_direct" || !snapshotWriterEnabled() {
		return nil, nil, nil, nil
	}

	minioCfg, err := config.LoadMinIOConfig()
	if err != nil {
		return nil, nil, nil, err
	}
	keyID := strings.TrimSpace(os.Getenv("SNAPSHOT_ENCRYPTION_ACTIVE_KEY_ID"))
	encodedKey := strings.TrimSpace(os.Getenv("SNAPSHOT_ENCRYPTION_KEY"))
	cipher, err := snapshotstore.NewCipherFromEncodedKey(keyID, encodedKey)
	if err != nil {
		return nil, nil, nil, err
	}
	store, err := snapshotstore.NewMinIOStore(minioCfg)
	if err != nil {
		return nil, nil, nil, err
	}
	return snapshotstore.AuditSnapshotOutboxBuilder{Cipher: cipher}, store, cipher, nil
}

func loadSnapshotWorkerConfig() (snapshotworker.Config, error) {
	parsePositiveInt := func(name string, fallback int) (int, error) {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			return fallback, nil
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return 0, fmt.Errorf("%s harus berupa bilangan bulat positif", name)
		}
		return value, nil
	}

	maxAttempts, err := parsePositiveInt("SNAPSHOT_MAX_ATTEMPTS", 10)
	if err != nil {
		return snapshotworker.Config{}, err
	}
	concurrency, err := parsePositiveInt("SNAPSHOT_WORKER_CONCURRENCY", 1)
	if err != nil {
		return snapshotworker.Config{}, err
	}
	retrySeconds, err := parsePositiveInt("SNAPSHOT_RETRY_BASE_SECONDS", 5)
	if err != nil {
		return snapshotworker.Config{}, err
	}
	pollSeconds, err := parsePositiveInt("SNAPSHOT_POLL_INTERVAL_SECONDS", 2)
	if err != nil {
		return snapshotworker.Config{}, err
	}

	return snapshotworker.Config{
		Environment:  os.Getenv("APP_ENV"),
		MaxAttempts:  maxAttempts,
		Concurrency:  concurrency,
		RetryBase:    time.Duration(retrySeconds) * time.Second,
		PollInterval: time.Duration(pollSeconds) * time.Second,
	}, nil
}

func loadTamperScannerConfig(recoveryCutoff *time.Time) (tamperscanner.Config, error) {
	parsePositiveInt := func(name string, fallback int) (int, error) {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			return fallback, nil
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return 0, fmt.Errorf("%s harus berupa bilangan bulat positif", name)
		}
		return value, nil
	}

	intervalSeconds, err := parsePositiveInt("TAMPER_SCAN_INTERVAL_SECONDS", 300)
	if err != nil {
		return tamperscanner.Config{}, err
	}
	batchSize, err := parsePositiveInt("TAMPER_SCAN_BATCH_SIZE", 100)
	if err != nil {
		return tamperscanner.Config{}, err
	}
	concurrency, err := parsePositiveInt("TAMPER_SCAN_CONCURRENCY", 2)
	if err != nil {
		return tamperscanner.Config{}, err
	}
	return tamperscanner.Config{
		Enabled:        tamperScannerEnabled(),
		Interval:       time.Duration(intervalSeconds) * time.Second,
		BatchSize:      batchSize,
		Concurrency:    concurrency,
		RecoveryCutoff: recoveryCutoff,
	}, nil
}

func loadVerificationSchedulerConfig() (audit.VerificationSchedulerConfig, error) {
	parsePositiveInt := func(name string, fallback int) (int, error) {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			return fallback, nil
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return 0, fmt.Errorf("%s harus berupa bilangan bulat positif", name)
		}
		return value, nil
	}
	parseNonNegativeInt := func(name string, fallback int) (int, error) {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			return fallback, nil
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return 0, fmt.Errorf("%s harus berupa bilangan bulat nol atau positif", name)
		}
		return value, nil
	}
	parseBool := func(name string, fallback bool) (bool, error) {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			return fallback, nil
		}
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return false, fmt.Errorf("%s harus berupa true atau false", name)
		}
		return value, nil
	}

	enabled, err := parseBool("VERIFICATION_SCHEDULER_ENABLED", false)
	if err != nil {
		return audit.VerificationSchedulerConfig{}, err
	}
	intervalSeconds, err := parsePositiveInt("VERIFICATION_SCHEDULER_INTERVAL_SECONDS", 86400)
	if err != nil {
		return audit.VerificationSchedulerConfig{}, err
	}
	lookbackHours, err := parsePositiveInt("VERIFICATION_SCHEDULER_LOOKBACK_HOURS", 24)
	if err != nil {
		return audit.VerificationSchedulerConfig{}, err
	}
	overlapSeconds, err := parseNonNegativeInt("VERIFICATION_SCHEDULER_OVERLAP_SECONDS", 300)
	if err != nil {
		return audit.VerificationSchedulerConfig{}, err
	}
	batchSize, err := parsePositiveInt("VERIFICATION_SCHEDULER_BATCH_SIZE", 100)
	if err != nil {
		return audit.VerificationSchedulerConfig{}, err
	}
	runOnStart, err := parseBool("VERIFICATION_SCHEDULER_RUN_ON_START", false)
	if err != nil {
		return audit.VerificationSchedulerConfig{}, err
	}

	timezone := strings.TrimSpace(os.Getenv("VERIFICATION_SCHEDULER_TIMEZONE"))
	if timezone == "" {
		timezone = "UTC"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return audit.VerificationSchedulerConfig{}, fmt.Errorf("VERIFICATION_SCHEDULER_TIMEZONE tidak valid: %w", err)
	}

	return audit.VerificationSchedulerConfig{
		Enabled:    enabled,
		ClientID:   strings.TrimSpace(os.Getenv("VERIFICATION_SCHEDULER_CLIENT_ID")),
		Interval:   time.Duration(intervalSeconds) * time.Second,
		Lookback:   time.Duration(lookbackHours) * time.Hour,
		Overlap:    time.Duration(overlapSeconds) * time.Second,
		BatchSize:  batchSize,
		Location:   location,
		RunOnStart: runOnStart,
	}, nil
}

func startPipelineWorker(ctx context.Context, db *gorm.DB, fabricSvc *blockchain.FabricService, snapshotBuilder snapshotstore.OutboxBuilder, recoveryCutoff *time.Time, recoveryService *recovery.Service, snapshotRequired bool) {
	hashEngine := &hasher.Engine{DB: db}
	aggEngine := &aggregator.Engine{DB: db, RecoveryCutoff: recoveryCutoff, SnapshotRequired: &snapshotRequired}
	kafkaEngine := &kafkaconsumer.Engine{DB: db, SnapshotBuilder: snapshotBuilder}

	// Ticker pipeline: hasher + aggregator (batch=10) + anchoring setiap 10 detik
	go func() {
		log.Println("⚙️  Pipeline Worker mulai berjalan...")
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Println("⚙️  Pipeline Worker berhenti.")
				return
			case <-ticker.C:
				if err := hashEngine.ProcessPendingLogs(); err != nil {
					log.Printf("❌ [Hasher] Error: %v\n", err)
				}
				if recoveryService != nil {
					if err := recoveryService.ReconcilePendingCDC(ctx, 100); err != nil {
						log.Printf("⚠️ [RecoveryCDC] Rekonsiliasi CDC selesai dengan status non-valid: %v\n", err)
					}
				}
				if err := aggEngine.ProcessBatch(10); err != nil {
					log.Printf("❌ [Aggregator] Error: %v\n", err)
				}
				if err := aggEngine.ProcessRecoveryBatch(10); err != nil {
					log.Printf("❌ [RecoveryAggregator] Error: %v\n", err)
				}
				if fabricSvc != nil {
					if err := fabricSvc.AnchorPendingRoots(); err != nil {
						log.Printf("❌ [Anchoring] Error: %v\n", err)
					}
					if err := fabricSvc.AnchorPendingRecoveryRoots(); err != nil {
						log.Printf("❌ [RecoveryAnchoring] Error: %v\n", err)
					}
					if recoveryService != nil {
						if err := recoveryService.VerifyReadyEvents(ctx, 100); err != nil {
							log.Printf("⚠️ [RecoveryVerification] Event auto-verification selesai dengan status non-valid: %v\n", err)
						}
					}
				}
			}
		}
	}()

	// Kafka consumer: satu goroutine per klien aktif
	go func() {
		if err := kafkaEngine.Reconcile(ctx); err != nil {
			log.Printf("⚠️  [KafkaConsumer] Reconcile awal gagal: %v\n", err)
		}
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := kafkaEngine.Reconcile(ctx); err != nil {
					log.Printf("⚠️  [KafkaConsumer] Reconcile gagal: %v\n", err)
				}
			}
		}
	}()
}

const (
	runtimeCommandServe   = "serve"
	runtimeCommandMigrate = "migrate"
)

func parseRuntimeCommand(args []string) (string, error) {
	if len(args) == 0 {
		return runtimeCommandServe, nil
	}
	if len(args) != 1 {
		return "", fmt.Errorf("provide exactly one command: serve or migrate")
	}
	switch args[0] {
	case runtimeCommandServe, runtimeCommandMigrate:
		return args[0], nil
	default:
		return "", fmt.Errorf("unknown command %q: expected serve or migrate", args[0])
	}
}

func runDatabaseMigration() error {
	db, err := config.OpenDB(os.Getenv("DB_MIGRATION_DSN"))
	if err != nil {
		return fmt.Errorf("open migration database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get migration database pool: %w", err)
	}
	defer sqlDB.Close()
	return config.MigrateDB(db)
}

func main() {
	godotenv.Load()

	command, err := parseRuntimeCommand(os.Args[1:])
	if err != nil {
		log.Fatalf("❌ Perintah runtime tidak valid: %v", err)
	}
	if command == runtimeCommandMigrate {
		if err := runDatabaseMigration(); err != nil {
			log.Fatalf("❌ Migrasi database eksplisit gagal: %v", err)
		}
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	db, err := config.OpenDB(os.Getenv("DB_DSN"))
	if err != nil {
		log.Fatalf("❌ Gagal koneksi ke database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("❌ Gagal mendapatkan pool koneksi database: %v", err)
	}
	if err := config.ValidateDBSchema(db); err != nil {
		_ = sqlDB.Close()
		log.Fatalf("❌ Schema database belum siap; jalankan perintah gateway-app migrate secara terpisah: %v", err)
	}
	defer sqlDB.Close()
	log.Println("✅ Koneksi dan schema database siap; startup tidak menjalankan migrasi.")
	recoveryCutoff, cutoffErr := config.LoadRecoveryCutoff()
	if cutoffErr != nil {
		log.Fatalf("❌ Konfigurasi recovery scope tidak valid: %v", cutoffErr)
	}
	recoveryMode := configuredRecoveryMode()
	// The direct client-DB path has no MinIO dependency. Keep the legacy
	// snapshot gate for the compatibility mode, but never let an old
	// SNAPSHOT_REQUIRED_FOR_ANCHOR=true setting block direct recovery startup.
	effectiveSnapshotRequired := snapshotRequiredForAnchor() && recoveryMode != "agent_direct"
	if scopeErr := validateRecoveryScopeFlags(recoveryEnabled(), effectiveSnapshotRequired, recoveryCutoff, recoveryMode); scopeErr != nil {
		log.Fatalf("❌ Konfigurasi recovery scope tidak valid: %v", scopeErr)
	}

	snapshotBuilder, snapshotStore, snapshotCipher, err := buildSnapshotRuntime(recoveryMode)
	if err != nil {
		log.Fatalf("❌ Konfigurasi snapshot writer tidak valid: %v", err)
	}
	if effectiveSnapshotRequired && snapshotBuilder == nil {
		log.Fatal("❌ SNAPSHOT_REQUIRED_FOR_ANCHOR=true membutuhkan SNAPSHOT_WRITER_ENABLED=true dan konfigurasi MinIO yang valid")
	}
	if snapshotStore != nil {
		snapshotWorkerConfig, configErr := loadSnapshotWorkerConfig()
		if configErr != nil {
			log.Fatalf("❌ Konfigurasi snapshot worker tidak valid: %v", configErr)
		}
		worker, workerErr := snapshotworker.New(db, snapshotStore, snapshotWorkerConfig)
		if workerErr != nil {
			log.Fatalf("❌ Snapshot worker tidak dapat dibuat: %v", workerErr)
		}
		go worker.Run(ctx)
	}

	fabricSvc, err := blockchain.InitFabricGateway(db)
	if err != nil {
		log.Printf("⚠️  Gagal terhubung ke Fabric: %v\n", err)
	} else {
		defer fabricSvc.Close()
	}
	if effectiveSnapshotRequired && fabricSvc == nil {
		log.Fatal("❌ SNAPSHOT_REQUIRED_FOR_ANCHOR=true membutuhkan koneksi Fabric yang valid")
	}
	if recoveryEnabled() && recoveryMode != "agent_direct" && (snapshotStore == nil || snapshotCipher == nil || snapshotBuilder == nil || fabricSvc == nil) {
		log.Fatal("❌ RECOVERY_ENABLED=true membutuhkan MinIO, encryption key, snapshot builder, dan Fabric yang valid")
	}
	if recoveryEnabled() && recoveryMode == "agent_direct" && fabricSvc == nil {
		log.Fatal("❌ RECOVERY_MODE=agent_direct membutuhkan koneksi Fabric yang valid")
	}
	if tamperScannerEnabled() && fabricSvc == nil {
		log.Fatal("❌ TAMPER_SCANNER_ENABLED=true membutuhkan koneksi Fabric yang valid")
	}

	var recoverySnapshotBuilder snapshotstore.RecoveryEventOutboxBuilder
	if snapshotCipher != nil {
		recoverySnapshotBuilder = snapshotstore.RecoveryEventSnapshotOutboxBuilder{Cipher: snapshotCipher}
	}
	recoveryService := recovery.NewService(db, snapshotStore, snapshotCipher, fabricSvc, recoverySnapshotBuilder)
	recoveryService.SetRecoveryCutoff(recoveryCutoff)
	recoveryService.SetRecoveryMode(recoveryMode)
	agentService := agentverifier.NewService(db)
	recoveryService.SetAgentVerifier(agentService)

	startPipelineWorker(ctx, db, fabricSvc, snapshotBuilder, recoveryCutoff, recoveryService, effectiveSnapshotRequired)

	auditRepo := audit.NewAuditRepository(db)
	auditService := audit.NewService(auditRepo, fabricSvc, db)
	verificationJobs := audit.NewVerificationJobService(db, auditService)
	go verificationJobs.Run(ctx)
	verificationSchedulerConfig, schedulerConfigErr := loadVerificationSchedulerConfig()
	if schedulerConfigErr != nil {
		log.Fatalf("âŒ Konfigurasi verification scheduler tidak valid: %v", schedulerConfigErr)
	}
	verificationScheduler := audit.NewVerificationScheduler(db, verificationJobs, verificationSchedulerConfig)
	go verificationScheduler.Run(ctx)
	auditHandler := audit.NewHandlerWithVerificationJobs(auditService, verificationJobs)
	if tamperScannerEnabled() {
		scannerConfig, configErr := loadTamperScannerConfig(recoveryCutoff)
		if configErr != nil {
			log.Fatalf("❌ Konfigurasi tamper scanner tidak valid: %v", configErr)
		}
		scanner, scannerErr := tamperscanner.New(db, auditService, scannerConfig)
		if scannerErr != nil {
			log.Fatalf("❌ Tamper scanner tidak dapat dibuat: %v", scannerErr)
		}
		go scanner.Run(ctx)
		log.Printf("🔍 Tamper scanner aktif; interval=%s batch=%d concurrency=%d", scannerConfig.Interval, scannerConfig.BatchSize, scannerConfig.Concurrency)
	}

	authRepo := auth.NewRepository(db)
	authService := auth.NewService(authRepo)
	authHandler := &auth.Handler{Service: authService}

	clientRepo := client.NewRepository(db)
	clientService := client.NewService(clientRepo)
	clientHandler := &client.Handler{
		Service: clientService,
		DB:      db,
	}

	agentHandler := agentverifier.NewHandler(db)

	reportService := report.NewService(auditService)
	reportHandler := report.NewHandler(reportService)
	recoveryHandler := recovery.NewHandler(recoveryService)

	router := api.SetupRouter(auditHandler, authHandler, clientHandler, agentHandler, reportHandler, recoveryHandler, db)
	api.RegisterHealthRoutes(router, db)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{Addr: "0.0.0.0:" + port, Handler: router}

	go func() {
		log.Printf("🚀 AuditChain Gateway berjalan di port %s...\n", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ Server error: %v\n", err)
		}
	}()

	<-ctx.Done()
	log.Println("🛑 Sinyal shutdown diterima...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	srv.Shutdown(shutdownCtx)
	log.Println("✅ Server berhenti dengan bersih.")
}
