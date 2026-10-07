package config

import (
	"go-blockchain-api/internal/models"
	"log"
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func ConnectDB() *gorm.DB {
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		log.Fatal("DB_DSN environment variable is not set")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("Gagal koneksi ke database: %v", err)
	}

	err = db.AutoMigrate(
		&models.Client{},
		&models.User{},
		&models.AuditLog{},
		&models.MerkleMetadata{},
		&models.MerkleProof{},
		&models.AgentConfig{},
		&models.ClientKafkaConfig{},
		&models.KafkaOffset{},
		&models.ClientTable{},
		&models.ClientUser{},
		&models.SnapshotOutbox{},
		&models.TamperIncident{},
		&models.RecoveryRequest{},
		&models.RecoveryEvent{},
		&models.ClientDashboardStats{},
		&models.VerificationRun{},
	)
	if err != nil {
		log.Fatalf("Gagal migrasi database: %v", err)
	}
	// event_type was added after the first snapshot outbox deployment. Keep
	// the additive migration safe for existing rows and make the legacy value
	// explicit before the worker starts processing the queue.
	if err := db.Exec("ALTER TABLE snapshot_outboxes ALTER COLUMN event_type SET DEFAULT 'STORE_AUDIT_SNAPSHOT'").Error; err != nil {
		log.Fatalf("Gagal menyiapkan default snapshot outbox event type: %v", err)
	}
	if err := db.Exec("UPDATE snapshot_outboxes SET event_type = 'STORE_AUDIT_SNAPSHOT' WHERE event_type IS NULL OR event_type = ''").Error; err != nil {
		log.Fatalf("Gagal menormalisasi event type snapshot outbox lama: %v", err)
	}
	// The incident lifecycle is intentionally limited to OPEN and RESOLVED.
	// Older deployments persisted intermediate/terminal states that no longer
	// have a user-facing action. Keep unresolved legacy incidents actionable
	// and keep legacy dismissed rows closed before rebuilding the active index.
	if err := normalizeLegacyTamperStatuses(db); err != nil {
		log.Fatalf("Gagal menormalisasi status incident tamper lama: %v", err)
	}

	// Satu log boleh memiliki banyak incident historis (misalnya setelah
	// recovery dan kemudian tamper lagi), tetapi hanya boleh ada satu incident
	// yang masih aktif untuk kombinasi client/log/jenis yang sama. GORM tidak
	// dapat mengekspresikan partial unique index ini melalui tag model, dan
	// index lama yang mencakup kolom status akan gagal saat incident baru
	// ditutup menjadi RESOLVED setelah incident RESOLVED sebelumnya sudah ada.
	if err := ensureTamperIncidentActiveIndex(db); err != nil {
		log.Fatalf("Gagal menyiapkan index incident tamper aktif: %v", err)
	}
	// New recovery requests are self-service: a client user may execute after
	// the preflight checks, without a platform-admin approval transition.
	// Keep the database default aligned with the model for any future insert
	// that does not explicitly set Status. Existing legacy rows are preserved.
	if err := db.Exec("ALTER TABLE recovery_requests ALTER COLUMN status SET DEFAULT 'PENDING_EXECUTION'").Error; err != nil {
		log.Fatalf("Gagal menyiapkan default status recovery request: %v", err)
	}
	if err := ensureDirectRecoverySchema(db); err != nil {
		log.Fatalf("Gagal menyiapkan schema direct client recovery: %v", err)
	}
	if err := ensureVerificationRunSchema(db); err != nil {
		log.Fatalf("Gagal menyiapkan schema verification run: %v", err)
	}

	log.Println("✅ Database terhubung dan schema telah di-migrate.")
	if err := db.Exec("ALTER TABLE users ALTER COLUMN client_id DROP NOT NULL").Error; err != nil {
		log.Printf("Gagal mengubah kolom users.client_id menjadi nullable: %v", err)
	}

	return db
}

func ensureVerificationRunSchema(db *gorm.DB) error {
	return db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_verification_runs_active_range
		ON verification_runs (client_id, from_time, to_time)
		WHERE status IN ('QUEUED', 'RUNNING')
	`).Error
}

func ensureTamperIncidentActiveIndex(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DROP INDEX IF EXISTS idx_tamper_active").Error; err != nil {
			return err
		}
		return tx.Exec(`
			CREATE UNIQUE INDEX idx_tamper_active
			ON tamper_incidents (client_id, log_id, incident_type)
			WHERE status = 'OPEN'
		`).Error
	})
}

func normalizeLegacyTamperStatuses(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE tamper_incidents SET status = 'OPEN' WHERE status IN ('UNDER_REVIEW', 'RECOVERING')").Error; err != nil {
			return err
		}
		return tx.Exec("UPDATE tamper_incidents SET status = 'RESOLVED' WHERE status = 'DISMISSED'").Error
	})
}

// ensureDirectRecoverySchema keeps the new client-DB recovery fields
// compatible with installations that were created with the snapshot/MinIO
// workflow. The statements are intentionally idempotent so a rolling deploy
// can restart the gateway safely.
func ensureDirectRecoverySchema(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		statements := []string{
			`ALTER TABLE recovery_requests ALTER COLUMN snapshot_object_key DROP NOT NULL`,
			`ALTER TABLE recovery_requests ALTER COLUMN snapshot_version_id DROP NOT NULL`,
			`ALTER TABLE tamper_incidents ALTER COLUMN incident_scope SET DEFAULT 'GATEWAY_INTEGRITY'`,
			`UPDATE tamper_incidents SET incident_scope = 'GATEWAY_INTEGRITY' WHERE incident_scope IS NULL OR incident_scope = ''`,
			`ALTER TABLE recovery_requests ALTER COLUMN cdc_status SET DEFAULT 'PENDING'`,
			`UPDATE recovery_requests SET cdc_status = 'PENDING' WHERE cdc_status IS NULL OR cdc_status = ''`,
			`ALTER TABLE recovery_events ALTER COLUMN cdc_status SET DEFAULT 'PENDING'`,
			`UPDATE recovery_events SET cdc_status = 'PENDING' WHERE cdc_status IS NULL OR cdc_status = ''`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_recovery_active_resource
			 ON recovery_requests (client_id, resource)
			 WHERE resource <> '' AND status IN ('PENDING_EXECUTION', 'PENDING_APPROVAL', 'APPROVED', 'EXECUTING', 'APPLIED_AWAITING_CDC')`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_recovery_agent_command
			 ON recovery_requests (agent_command_id)
			 WHERE agent_command_id <> ''`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_tamper_active_client_source
			 ON tamper_incidents (client_id, resource, incident_type)
			 WHERE incident_scope = 'CLIENT_SOURCE' AND status = 'OPEN'`,
		}
		for _, statement := range statements {
			if err := tx.Exec(statement).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
