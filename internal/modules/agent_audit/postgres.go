package agent_audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) LatestByResource(ctx context.Context, clientID, resource string) ([]json.RawMessage, error) {
	return s.queryReadOnly(ctx, `
		SELECT to_jsonb(a)
		FROM audit_logs AS a
		WHERE a.client_id = $1
		  AND a.resource = $2
		  AND COALESCE(UPPER(TRIM(a.action)), '') <> 'RECOVERY'
		ORDER BY a."timestamp" DESC, a.log_id DESC
		LIMIT 1`, clientID, resource)
}

func (s *PostgresStore) ByTimeRange(ctx context.Context, clientID string, from, to time.Time, limit int) ([]json.RawMessage, error) {
	if limit <= 0 {
		return nil, errors.New("limit must be positive")
	}
	return s.queryReadOnly(ctx, `
		SELECT to_jsonb(a)
		FROM audit_logs AS a
		WHERE a.client_id = $1
		  AND a."timestamp" >= $2
		  AND a."timestamp" <= $3
		  AND COALESCE(UPPER(TRIM(a.action)), '') <> 'RECOVERY'
		ORDER BY a."timestamp" ASC, a.log_id ASC
		LIMIT $4`, clientID, from, to, limit)
}

func (s *PostgresStore) queryReadOnly(ctx context.Context, query string, args ...any) ([]json.RawMessage, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("offchain database is not configured")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin read-only transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query audit logs: %w", err)
	}
	result := make([]json.RawMessage, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan audit log JSON: %w", err)
		}
		if !json.Valid(raw) {
			return nil, errors.New("database returned invalid audit log JSON")
		}
		result = append(result, append(json.RawMessage(nil), raw...))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit logs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close audit log rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit read-only transaction: %w", err)
	}
	return result, nil
}
