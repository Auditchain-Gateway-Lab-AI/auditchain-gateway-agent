// Command gatewayworker runs only the CDC-to-offchain-to-Fabric data plane.
// The client Agent at the repository root remains a separate executable.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"auditchain-agent/internal/blockchain"
	"auditchain-agent/internal/engine/aggregator"
	"auditchain-agent/internal/engine/hasher"
	"auditchain-agent/internal/engine/kafkaconsumer"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// Starting a second consumer must require an intentional cutover.
	if strings.TrimSpace(os.Getenv("GATEWAY_BACKGROUND_WORKERS_ENABLED")) != "true" {
		return fmt.Errorf("Gateway worker disabled; enable only after the old consumer is stopped")
	}
	if strings.TrimSpace(os.Getenv("GATEWAY_V2_CUTOVER_CONFIRMED")) != "true" {
		return fmt.Errorf("GATEWAY_V2_CUTOVER_CONFIRMED=true is required after checking the old consumer is stopped")
	}
	startOffset := strings.TrimSpace(os.Getenv("KAFKA_CONSUMER_START_OFFSET"))
	if startOffset != "earliest" && startOffset != "latest" {
		return fmt.Errorf("KAFKA_CONSUMER_START_OFFSET must be earliest or latest")
	}
	if os.Getenv("FABRIC_CHANNEL") == "" || os.Getenv("FABRIC_CHAINCODE") == "" {
		return fmt.Errorf("FABRIC_CHANNEL and FABRIC_CHAINCODE are required")
	}
	cutoff, err := time.Parse(time.RFC3339, strings.TrimSpace(os.Getenv("GATEWAY_PIPELINE_CUTOFF_UTC")))
	if err != nil {
		return fmt.Errorf("GATEWAY_PIPELINE_CUTOFF_UTC must be RFC3339: %w", err)
	}
	dsn := strings.TrimSpace(os.Getenv("DB_DSN"))
	if dsn == "" {
		return fmt.Errorf("DB_DSN is required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("connect offchain PostgreSQL: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping offchain PostgreSQL: %w", err)
	}
	fabric, err := blockchain.InitFabricGateway(db)
	if err != nil {
		return fmt.Errorf("connect Besu Fabric: %w", err)
	}
	defer fabric.Close()
	fabric.AnchorCutoff = &cutoff
	consumer := &kafkaconsumer.Engine{DB: db, StartOffset: startOffset}
	hasherEngine := &hasher.Engine{DB: db}
	aggregatorEngine := &aggregator.Engine{DB: db, RecoveryCutoff: &cutoff}
	log.Printf("Gateway data plane started; cutoff=%s", cutoff.Format(time.RFC3339))
	if err := consumer.Reconcile(ctx); err != nil {
		log.Printf("initial Kafka reconcile failed; retrying: %v", err)
	}
	consumerTicker := time.NewTicker(15 * time.Second)
	defer consumerTicker.Stop()
	pipelineTicker := time.NewTicker(10 * time.Second)
	defer pipelineTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-consumerTicker.C:
			if err := consumer.Reconcile(ctx); err != nil {
				log.Printf("Kafka reconcile failed: %v", err)
			}
		case <-pipelineTicker.C:
			if err := hasherEngine.ProcessPendingLogsSince(cutoff); err != nil {
				log.Printf("hash pending logs: %v", err)
			}
			if err := aggregatorEngine.ProcessBatch(10); err != nil {
				log.Printf("aggregate logs: %v", err)
			}
			if err := fabric.AnchorPendingRoots(); err != nil {
				log.Printf("anchor roots: %v", err)
			}
		}
	}
}
