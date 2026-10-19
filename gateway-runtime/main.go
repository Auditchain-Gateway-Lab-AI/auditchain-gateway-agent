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
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"go-blockchain-api/internal/api"
	"go-blockchain-api/internal/blockchain"
	"go-blockchain-api/internal/blockchain/agentverifier"
	"go-blockchain-api/internal/config"
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
	var recoverySnapshotBuilder snapshotstore.RecoveryEventOutboxBuilder
	if snapshotCipher != nil {
		recoverySnapshotBuilder = snapshotstore.RecoveryEventSnapshotOutboxBuilder{Cipher: snapshotCipher}
	}
	recoveryService := recovery.NewService(db, snapshotStore, snapshotCipher, fabricSvc, recoverySnapshotBuilder)
	recoveryService.SetRecoveryCutoff(recoveryCutoff)
	recoveryService.SetRecoveryMode(recoveryMode)
	agentService := agentverifier.NewService(db)
	recoveryService.SetAgentVerifier(agentService)

	auditRepo := audit.NewAuditRepository(db)
	auditService := audit.NewService(auditRepo, fabricSvc, db)
	verificationJobs := audit.NewVerificationJobService(db, auditService)
	auditHandler := audit.NewHandlerWithVerificationJobs(auditService, verificationJobs)

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
