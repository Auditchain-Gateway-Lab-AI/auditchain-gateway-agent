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
	log.Printf("   Kafka       : %s", cfg.Kafka.Brokers)
	log.Printf("   Topic prefix: %s", cfg.Kafka.TopicPrefix)

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
	if enabled, err := getEnvBool("AGENT_ENABLE_TABLE_ENDPOINT", false); err != nil {
		log.Fatalf("❌ AGENT_ENABLE_TABLE_ENDPOINT tidak valid: %v", err)
	} else {
		verifyServer.EnableTableEndpoint(enabled)
	}

	var recoveryPolicy *recovery.PolicySet
	if cfg.Recovery.Enabled {
		recoveryPolicy, err = recovery.LoadPolicy(cfg.Recovery.PolicyPath)
		if err != nil {
			log.Fatalf("❌ Gagal load recovery policy: %v", err)
		}
		policyHash, hashErr := recoveryPolicy.PolicyHash()
		if hashErr != nil {
			log.Fatalf("❌ Gagal menghitung hash recovery policy: %v", hashErr)
		}
		log.Printf("✅ Recovery policy dimuat (hash=%s)", policyHash[:16])

		verifyServer.SetReadProjection(func(resourceTable string) (verify.ReadProjection, bool) {
			mapping, ok := recoveryPolicy.ReadMapping(resourceTable)
			if !ok {
				return verify.ReadProjection{}, false
			}
			return verify.ReadProjection{
				Schema:       mapping.Schema,
				Table:        mapping.Table,
				PrimaryKey:   mapping.PrimaryKey,
				StateColumns: mapping.StateColumns,
			}, true
		})

		stateStore, storeErr := recovery.OpenIdempotencyStore(cfg.Recovery.StatePath)
		if storeErr != nil {
			log.Fatalf("❌ Gagal membuka recovery state store: %v", storeErr)
		}
		repository := recovery.NewOracleRepository(db, cfg.Recovery.ClientID)
		recoveryService := recovery.NewService(recovery.ServiceConfig{
			Enabled:      cfg.Recovery.Enabled,
			ClientID:     cfg.Recovery.ClientID,
			MaxColumns:   cfg.Recovery.MaxColumns,
			MaxClockSkew: time.Duration(cfg.Recovery.MaxClockSkewSeconds) * time.Second,
			Retention:    time.Duration(cfg.Recovery.RetentionDays) * 24 * time.Hour,
		}, recoveryPolicy, repository, stateStore)
		verifyServer.SetRecoveryHandler(recovery.NewHandler(recoveryService, recovery.HTTPConfig{
			Enabled:        cfg.Recovery.Enabled,
			Token:          cfg.Recovery.Token,
			MaxBodyBytes:   cfg.Recovery.MaxBodyBytes,
			RequestTimeout: time.Duration(cfg.Recovery.RequestTimeoutSeconds) * time.Second,
		}))
		verifyServer.SetMetricsHandler(recoveryService.MetricsHandler())
	} else {
		// Keep the route visible for operators while making the safe default
		// explicit. No recovery service or state store is opened in this mode.
		verifyServer.SetRecoveryHandler(recovery.NewHandler(nil, recovery.HTTPConfig{
			Enabled:      false,
			MaxBodyBytes: cfg.Recovery.MaxBodyBytes,
		}))
	}

	if cfg.Recovery.Enabled {
		log.Println("🚀 AuditChain Agent mulai berjalan (verify + recovery pilot aktif)")
	} else {
		log.Println("🚀 AuditChain Agent mulai berjalan (recovery disabled)")
	}

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
