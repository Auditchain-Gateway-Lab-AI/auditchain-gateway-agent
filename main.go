// @title AuditChain Agent API
// @version 1.0
// @description API AuditChain Agent untuk verifikasi integritas data operasional (Lapis 3), pemulihan data (recovery), dan audit independen terhadap blockchain.
// @termsOfService http://swagger.io/terms/

// @contact.name Tim AuditChain
// @contact.url https://auditchain.id
// @contact.email support@auditchain.id

// @license.name Apache 2.0
// @license.url http://www.apache.org/licenses/LICENSE-2.0.html

// @host localhost:9090
// @BasePath /

// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Masukkan token otentikasi dengan format: Bearer <token>
package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "auditchain-agent/docs"
	"auditchain-agent/internal/audit"
	"auditchain-agent/internal/config"
	"auditchain-agent/internal/recovery"
	"auditchain-agent/internal/verify"

	goora "github.com/sijms/go-ora/v2"
)

func main() {
	cfgPath := "config.yml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("❌ Gagal load config: %v", err)
	}

	log.Println("✅ Konfigurasi berhasil dimuat")
	log.Printf("   Gateway     : %s", cfg.Gateway.URL)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Ekstrak host dan port dari config
	host := cfg.SourceDB.Host
	port := cfg.SourceDB.Port
	if strings.Contains(host, ":") {
		parts := strings.Split(host, ":")
		host = parts[0]
		if p, err := strconv.Atoi(parts[1]); err == nil {
			port = p
		}
	}

	// Gunakan BuildUrl dari go-ora dengan opsi SID
	urlOptions := map[string]string{
		"SID": cfg.SourceDB.DBName,
	}
	dsn := goora.BuildUrl(host, port, "", cfg.SourceDB.User, cfg.SourceDB.Password, urlOptions)

	db, err := sql.Open("oracle", dsn)
	if err != nil {
		log.Fatalf("❌ Gagal koneksi ke database: %v", err)
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("❌ Database tidak bisa dijangkau: %v", err)
	}
	log.Println("✅ Koneksi ke database Oracle (SID) berhasil")

	// Verify server untuk Lapis 3 AuditChain
	verifyToken := cfg.VerifyToken()
	verifyPort := getEnv("AGENT_VERIFY_PORT", "9090")
	verifyServer := verify.NewServer(db, verifyToken, verifyPort)
	if cfg.JWTSecret != "" {
		verifyServer.SetJWTSecret(cfg.JWTSecret)
		log.Println("🔑 Shared JWT Authentication aktif (menggunakan JWT_SECRET dari Gateway)")
	}
	verifyServer.SetAuditTrailProjection(verify.AuditTrailProjection{
		Schema: strings.TrimSpace(os.Getenv("AGENT_AUDIT_TRAIL_SCHEMA")),
		Table:  getEnv("AGENT_AUDIT_TRAIL_TABLE", "AUDIT_TRAIL"),
	})
	if enabled, err := getEnvBool("AGENT_ENABLE_TABLE_ENDPOINT", false); err != nil {
		log.Fatalf("❌ AGENT_ENABLE_TABLE_ENDPOINT tidak valid: %v", err)
	} else {
		verifyServer.EnableTableEndpoint(enabled)
	}

	verifyPolicyPath := getEnv("AGENT_VERIFY_POLICY_PATH", "./verify-policy.yml")
	verifyPolicy, err := verify.LoadReadPolicy(verifyPolicyPath)
	if err != nil {
		log.Fatalf("❌ Gagal load verify read policy: %v", err)
	}
	verifyServer.SetReadProjection(verifyPolicy.ReadMapping)
	log.Printf("✅ Verify read policy dimuat (%d tabel)", verifyPolicy.TableCount())

	// Modul Recovery (Single-row client restoration)
	var recoveryService *recovery.Service
	if cfg.Recovery.Enabled {
		recoveryService = recovery.NewService(db)
		if verifyPolicy != nil {
			recoveryService.SetReadProjection(verifyPolicy.ReadMapping)
		}
		verifyServer.SetRecoveryHandler(recovery.NewHandler(recoveryService, verifyToken, cfg.JWTSecret))
		log.Println("✅ Modul Recovery aktif di endpoint /recover/")
	} else {
		verifyServer.SetRecoveryHandler(recovery.NewHandler(nil, verifyToken, cfg.JWTSecret))
	}

	// Inisialisasi Modul Audit (Outbound Verification)
	gatewayClient := audit.NewGatewayClient(
		cfg.Gateway.URL,
		cfg.Gateway.Token,
		time.Duration(cfg.Gateway.TimeoutSeconds)*time.Second,
	)

	var recoveryExecutor audit.RecoveryExecutor
	if recoveryService != nil {
		recoveryExecutor = recoveryService
	}

	auditService := audit.NewService(
		audit.ServiceConfig{
			ClientID:    cfg.Recovery.ClientID,
			AutoRecover: cfg.Audit.AutoRecover,
		},
		gatewayClient,
		verifyServer,
		recoveryExecutor,
	)

	auditHandler := audit.NewHandler(auditService, verifyToken, cfg.JWTSecret)
	verifyServer.SetAuditHandler(auditHandler)
	log.Println("✅ Modul Audit (Outbound Verification) aktif di /audit/")

	if cfg.Audit.Enabled {
		auditRunner := audit.NewRunner(audit.RunnerConfig{
			Enabled:     cfg.Audit.Enabled,
			Interval:    time.Duration(cfg.Audit.IntervalMinutes) * time.Minute,
			Window:      time.Duration(cfg.Audit.WindowMinutes) * time.Minute,
			AutoRecover: cfg.Audit.AutoRecover,
		}, auditService)
		go auditRunner.Start(ctx)
	}

	log.Println("🚀 AuditChain Agent siap melayani request")

	serverErr := make(chan error, 1)
	go func() { serverErr <- verifyServer.Run(ctx) }()
	select {
	case err := <-serverErr:
		if err != nil {
			log.Fatalf("❌ Verify server berhenti: %v", err)
		}
	case <-ctx.Done():
		if err := <-serverErr; err != nil {
			log.Printf("⚠️ Shutdown server: %v", err)
		}
	}

	log.Println("✅ Agent berhenti dengan bersih.")
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvBool(key string, fallback bool) (bool, error) {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return false, err
		}
		return parsed, nil
	}
	return fallback, nil
}
