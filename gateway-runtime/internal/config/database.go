package config

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"go-blockchain-api/internal/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// OpenDB connects and pings PostgreSQL without issuing schema or data writes.
func OpenDB(dsn string) (*gorm.DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("database DSN is required")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL connection: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get PostgreSQL connection pool: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return db, nil
}

// MigrateDB applies the explicit schema/data compatibility migration. Call it
// only from the dedicated migrate command using DB_MIGRATION_DSN.
func MigrateDB(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("database connection is required")
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(databaseModels()...); err != nil {
			return fmt.Errorf("auto-migrate gateway tables: %w", err)
		}

		// event_type was added after the first snapshot outbox deployment. Keep
		// the legacy value explicit before a worker processes the queue.
		if err := tx.Exec("ALTER TABLE snapshot_outboxes ALTER COLUMN event_type SET DEFAULT 'STORE_AUDIT_SNAPSHOT'").Error; err != nil {
			return fmt.Errorf("set snapshot outbox event type default: %w", err)
		}
		if err := tx.Exec("UPDATE snapshot_outboxes SET event_type = 'STORE_AUDIT_SNAPSHOT' WHERE event_type IS NULL OR event_type = ''").Error; err != nil {
			return fmt.Errorf("normalize legacy snapshot outbox event types: %w", err)
		}
		if err := normalizeLegacyTamperStatuses(tx); err != nil {
			return fmt.Errorf("normalize legacy tamper statuses: %w", err)
		}
		if err := ensureTamperIncidentActiveIndex(tx); err != nil {
			return fmt.Errorf("ensure active tamper incident index: %w", err)
		}
		if err := tx.Exec("ALTER TABLE recovery_requests ALTER COLUMN status SET DEFAULT 'PENDING_EXECUTION'").Error; err != nil {
			return fmt.Errorf("set recovery request status default: %w", err)
		}
		if err := ensureDirectRecoverySchema(tx); err != nil {
			return fmt.Errorf("ensure direct recovery schema: %w", err)
		}
		if err := ensureVerificationRunSchema(tx); err != nil {
			return fmt.Errorf("ensure verification run schema: %w", err)
		}
		if err := tx.Exec("ALTER TABLE users ALTER COLUMN client_id DROP NOT NULL").Error; err != nil {
			return fmt.Errorf("allow users without client_id: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("gateway database migration failed: %w", err)
	}
	log.Println("✅ Explicit gateway database migration completed.")
	return nil
}

// ValidateDBSchema performs catalog reads only and fails before workers start
// when a required model table or column is missing.
func ValidateDBSchema(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("database connection is required")
	}

	type expectedTable struct {
		name    string
		columns []string
	}

	var expected []expectedTable
	for _, model := range databaseModels() {
		statement := &gorm.Statement{DB: db}
		if err := statement.Parse(model); err != nil {
			return fmt.Errorf("parse required database model: %w", err)
		}
		table := expectedTable{name: statement.Schema.Table}
		for _, field := range statement.Schema.Fields {
			if field.DBName != "" {
				table.columns = append(table.columns, field.DBName)
			}
		}
		expected = append(expected, table)
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(expected)), ",")
	query := `SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = ANY(current_schemas(false)::text[])
		  AND table_name IN (` + placeholders + `)`
	args := make([]interface{}, 0, len(expected))
	for _, table := range expected {
		args = append(args, table.name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := db.WithContext(ctx).Raw(query, args...).Rows()
	if err != nil {
		return fmt.Errorf("read required PostgreSQL schema: %w", err)
	}
	defer rows.Close()
	actual := make(map[string]map[string]struct{}, len(expected))
	for rows.Next() {
		var tableName, columnName string
		if err := rows.Scan(&tableName, &columnName); err != nil {
			return fmt.Errorf("read required PostgreSQL schema row: %w", err)
		}
		if actual[tableName] == nil {
			actual[tableName] = make(map[string]struct{})
		}
		actual[tableName][columnName] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate required PostgreSQL schema rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close required PostgreSQL schema rows: %w", err)
	}

	for _, table := range expected {
		columns, tableExists := actual[table.name]
		if !tableExists {
			return fmt.Errorf("required table %q is missing; run the explicit gateway migrate command", table.name)
		}
		for _, column := range table.columns {
			if _, exists := columns[column]; !exists {
				return fmt.Errorf("required column %s.%s is missing; run the explicit gateway migrate command", table.name, column)
			}
		}
	}
	if err := validateRequiredColumnProperties(db.WithContext(ctx)); err != nil {
		return err
	}

	// These indexes enforce lifecycle/idempotency invariants that GORM model
	// tags cannot fully express. Check them read-only before starting workers.
	requiredIndexes := []string{
		"idx_tamper_active",
		"idx_recovery_idempotency",
		"idx_recovery_active_resource",
		"idx_recovery_agent_command",
		"idx_tamper_active_client_source",
		"idx_verification_runs_active_range",
	}
	indexPlaceholders := strings.TrimSuffix(strings.Repeat("?,", len(requiredIndexes)), ",")
	indexQuery := `SELECT indexname FROM pg_indexes
		WHERE schemaname = ANY(current_schemas(false)::text[])
		  AND indexname IN (` + indexPlaceholders + `)`
	indexRows, err := db.WithContext(ctx).Raw(indexQuery, stringArgs(requiredIndexes)...).Rows()
	if err != nil {
		return fmt.Errorf("read required PostgreSQL indexes: %w", err)
	}
	defer indexRows.Close()
	indexes := make(map[string]struct{}, len(requiredIndexes))
	for indexRows.Next() {
		var indexName string
		if err := indexRows.Scan(&indexName); err != nil {
			return fmt.Errorf("read required PostgreSQL index row: %w", err)
		}
		indexes[indexName] = struct{}{}
	}
	if err := indexRows.Err(); err != nil {
		return fmt.Errorf("iterate required PostgreSQL indexes: %w", err)
	}
	if err := indexRows.Close(); err != nil {
		return fmt.Errorf("close required PostgreSQL index rows: %w", err)
	}
	for _, indexName := range requiredIndexes {
		if _, exists := indexes[indexName]; !exists {
			return fmt.Errorf("required index %q is missing; run the explicit gateway migrate command", indexName)
		}
	}
	return nil
}

func validateRequiredColumnProperties(db *gorm.DB) error {
	type columnProperty struct {
		table           string
		column          string
		mustBeNullable  bool
		defaultContains string
	}

	required := []columnProperty{
		{table: "users", column: "client_id", mustBeNullable: true},
		{table: "recovery_requests", column: "snapshot_object_key", mustBeNullable: true},
		{table: "recovery_requests", column: "snapshot_version_id", mustBeNullable: true},
		{table: "snapshot_outboxes", column: "event_type", defaultContains: "STORE_AUDIT_SNAPSHOT"},
		{table: "recovery_requests", column: "status", defaultContains: "PENDING_EXECUTION"},
		{table: "recovery_requests", column: "cdc_status", defaultContains: "PENDING"},
		{table: "recovery_events", column: "cdc_status", defaultContains: "PENDING"},
		{table: "tamper_incidents", column: "incident_scope", defaultContains: "GATEWAY_INTEGRITY"},
	}

	conditions := make([]string, 0, len(required))
	args := make([]interface{}, 0, len(required)*2)
	for _, property := range required {
		conditions = append(conditions, "(table_name = ? AND column_name = ?)")
		args = append(args, property.table, property.column)
	}
	query := `SELECT table_name, column_name, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_schema = ANY(current_schemas(false)::text[])
		  AND (` + strings.Join(conditions, " OR ") + `)`
	rows, err := db.Raw(query, args...).Rows()
	if err != nil {
		return fmt.Errorf("read required PostgreSQL column properties: %w", err)
	}
	defer rows.Close()
	actual := make(map[string]struct {
		nullable bool
		def      string
	}, len(required))
	for rows.Next() {
		var tableName, columnName, nullable string
		var defaultValue *string
		if err := rows.Scan(&tableName, &columnName, &nullable, &defaultValue); err != nil {
			return fmt.Errorf("read required PostgreSQL column property row: %w", err)
		}
		defaultText := ""
		if defaultValue != nil {
			defaultText = *defaultValue
		}
		actual[tableName+"."+columnName] = struct {
			nullable bool
			def      string
		}{nullable: strings.EqualFold(nullable, "YES"), def: defaultText}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate required PostgreSQL column property rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close required PostgreSQL column property rows: %w", err)
	}

	for _, property := range required {
		key := property.table + "." + property.column
		actualProperty, exists := actual[key]
		if !exists {
			return fmt.Errorf("required column properties for %s are missing; run the explicit gateway migrate command", key)
		}
		if property.mustBeNullable && !actualProperty.nullable {
			return fmt.Errorf("required column %s must be nullable; run the explicit gateway migrate command", key)
		}
		if property.defaultContains != "" && !strings.Contains(actualProperty.def, property.defaultContains) {
			return fmt.Errorf("required default for %s must contain %q; run the explicit gateway migrate command", key, property.defaultContains)
		}
	}
	return nil
}

func stringArgs(values []string) []interface{} {
	args := make([]interface{}, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}

func databaseModels() []interface{} {
	return []interface{}{
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
	}
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
