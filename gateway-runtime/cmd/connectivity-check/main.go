package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"go-blockchain-api/internal/blockchain"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const probeTimeout = 10 * time.Second

type ledgerAnchor struct {
	AnchorID   string `json:"anchor_id"`
	MerkleRoot string `json:"merkle_root"`
}

func main() {
	if err := run(); err != nil {
		log.Printf("connectivity probe gagal: %v", err)
		os.Exit(1)
	}
}

func run() error {
	if err := loadConnectivityEnv(); err != nil {
		return err
	}

	dsn := strings.TrimSpace(os.Getenv("CONNECTIVITY_DB_DSN"))
	clientID := strings.TrimSpace(os.Getenv("CONNECTIVITY_CLIENT_ID"))
	if dsn == "" {
		return errors.New("CONNECTIVITY_DB_DSN wajib diisi")
	}
	if _, err := uuid.Parse(clientID); err != nil {
		return errors.New("CONNECTIVITY_CLIENT_ID wajib berisi UUID client yang valid")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return fmt.Errorf("inisialisasi PostgreSQL gagal: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("membuka koneksi PostgreSQL gagal: %w", err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(0)

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	err = sqlDB.PingContext(ctx)
	cancel()
	if err != nil {
		return fmt.Errorf("PostgreSQL tidak terjangkau: %w", err)
	}
	fmt.Println("PostgreSQL: PASS (koneksi berhasil; tidak ada migrasi/schema write)")

	anchorID, expectedRoot, err := findProbeAnchor(db, clientID, strings.TrimSpace(os.Getenv("CONNECTIVITY_ANCHOR_ID")))
	if err != nil {
		return fmt.Errorf("membaca anchor referensi PostgreSQL gagal: %w", err)
	}

	fabric, err := blockchain.InitFabricGateway(db)
	if err != nil {
		return fmt.Errorf("inisialisasi koneksi Fabric gagal: %w", err)
	}
	defer fabric.Close()

	result, err := fabric.Contract.EvaluateTransaction("QueryMerkleRoot", anchorID)
	if err != nil {
		return fmt.Errorf("Fabric Evaluate QueryMerkleRoot gagal: %w", err)
	}
	if err := validateLedgerAnchor(result, anchorID, expectedRoot); err != nil {
		return fmt.Errorf("hasil baca Fabric tidak cocok dengan referensi PostgreSQL: %w", err)
	}
	fmt.Println("Fabric: PASS (Evaluate/read-only; anchor dan Merkle root cocok dengan PostgreSQL)")
	return nil
}

func loadConnectivityEnv() error {
	path := strings.TrimSpace(os.Getenv("CONNECTIVITY_ENV_FILE"))
	if path == "" {
		path = ".env.connectivity"
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if strings.TrimSpace(os.Getenv("CONNECTIVITY_DB_DSN")) != "" {
			return nil // Allow deployment environments to inject variables directly.
		}
		return fmt.Errorf("file konfigurasi %q tidak ditemukan; salin .env.connectivity.example dan isi konfigurasi lokal", path)
	} else if err != nil {
		return fmt.Errorf("memeriksa file konfigurasi gagal: %w", err)
	}
	if err := godotenv.Load(path); err != nil {
		return fmt.Errorf("memuat file konfigurasi %q gagal: %w", path, err)
	}
	return nil
}

func findProbeAnchor(db *gorm.DB, clientID, requestedAnchorID string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	var anchorID, merkleRoot string
	statement := `
		SELECT blockchain_tx_id, merkle_root
		FROM audit_logs
		WHERE client_id = ?
		  AND status = 'ANCHORED'
		  AND COALESCE(blockchain_tx_id, '') <> ''
		  AND COALESCE(merkle_root, '') <> ''
	`
	args := []interface{}{clientID}
	if requestedAnchorID != "" {
		statement += " AND blockchain_tx_id = ?"
		args = append(args, requestedAnchorID)
	}
	statement += " ORDER BY db_timestamp DESC NULLS LAST LIMIT 1"
	err := db.WithContext(ctx).Raw(statement, args...).Row().Scan(&anchorID, &merkleRoot)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", errors.New("tidak ada anchor ANCHORED untuk client ini; isi CONNECTIVITY_ANCHOR_ID dengan anchor yang diketahui valid atau tunggu log ter-anchor")
		}
		return "", "", err
	}
	return anchorID, merkleRoot, nil
}

func validateLedgerAnchor(raw []byte, expectedAnchorID, expectedRoot string) error {
	var actual ledgerAnchor
	if err := json.Unmarshal(raw, &actual); err != nil {
		return fmt.Errorf("respons chaincode bukan JSON anchor yang valid: %w", err)
	}
	if strings.TrimSpace(actual.AnchorID) == "" || strings.TrimSpace(actual.MerkleRoot) == "" {
		return errors.New("respons chaincode tidak memuat anchor_id dan merkle_root")
	}
	if actual.AnchorID != expectedAnchorID {
		return fmt.Errorf("anchor_id ledger %q berbeda dari PostgreSQL", actual.AnchorID)
	}
	if !strings.EqualFold(actual.MerkleRoot, expectedRoot) {
		return errors.New("merkle_root ledger berbeda dari PostgreSQL")
	}
	return nil
}
