package recovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"auditchain-agent/internal/canonicalstate"
	"auditchain-agent/internal/sqlident"
)

type OracleRepository struct {
	db       *sql.DB
	clientID string
}

func NewOracleRepository(db *sql.DB, clientID string) *OracleRepository {
	return &OracleRepository{db: db, clientID: clientID}
}

func (r *OracleRepository) Read(ctx context.Context, table TablePolicy, recordID string) (StateSnapshot, error) {
	if r == nil || r.db == nil {
		return StateSnapshot{}, ErrDatabaseUnreachable
	}
	return r.readWith(ctx, r.db, table, recordID, false)
}

func (r *OracleRepository) Apply(ctx context.Context, table TablePolicy, recordID string, request RecoveryRequest) (ApplyResult, error) {
	if r == nil || r.db == nil {
		return ApplyResult{}, ErrDatabaseUnreachable
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("%w: begin transaction", ErrDatabaseUnreachable)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	current, err := r.readWith(ctx, tx, table, recordID, true)
	if err != nil {
		return ApplyResult{}, err
	}
	if current.Hash != request.ExpectedBeforeHash {
		return ApplyResult{}, ErrSourceStateChanged
	}
	if request.Operation == OperationNoop {
		if current.Hash != request.DesiredStateHash {
			return ApplyResult{}, ErrSourceStateChanged
		}
		if err := tx.Commit(); err != nil {
			return ApplyResult{}, fmt.Errorf("%w: commit noop", ErrOutcomeUnknown)
		}
		committed = true
		return ApplyResult{BeforeHash: current.Hash, AfterHash: current.Hash, FoundAfter: current.Found, ReadbackMatch: true}, nil
	}

	if request.Operation == OperationDelete {
		if !current.Found {
			return ApplyResult{}, ErrSourceStateChanged
		}
		query := fmt.Sprintf("DELETE FROM %s WHERE %s = :1", qualifiedTable(table), sqlident.Quote(table.PrimaryKeyColumn))
		result, err := tx.ExecContext(ctx, query, recordID)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("%w: delete rejected", ErrWriteRejected)
		}
		if err := requireOneRow(result); err != nil {
			return ApplyResult{}, err
		}
	} else if request.Operation == OperationUpsert {
		if current.Found {
			if err := r.updateExisting(ctx, tx, table, recordID, request.DesiredState); err != nil {
				return ApplyResult{}, err
			}
		} else if err := r.insertMissing(ctx, tx, table, recordID, request.DesiredState); err != nil {
			return ApplyResult{}, err
		}
	} else {
		return ApplyResult{}, ErrWriteRejected
	}

	readback, err := r.readWith(ctx, tx, table, recordID, false)
	if err != nil {
		return ApplyResult{}, err
	}
	if readback.Hash != request.DesiredStateHash {
		return ApplyResult{}, ErrReadbackMismatch
	}
	if err := tx.Commit(); err != nil {
		return ApplyResult{}, fmt.Errorf("%w: commit outcome unknown", ErrOutcomeUnknown)
	}
	committed = true
	return ApplyResult{
		BeforeHash:    current.Hash,
		AfterHash:     readback.Hash,
		FoundAfter:    readback.Found,
		ReadbackMatch: true,
	}, nil
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (r *OracleRepository) readWith(ctx context.Context, db queryer, table TablePolicy, recordID string, forUpdate bool) (StateSnapshot, error) {
	columns := stateColumns(table)
	if len(columns) == 0 {
		return StateSnapshot{}, ErrWriteRejected
	}
	selectColumns := make([]string, 0, len(columns))
	for _, column := range columns {
		selectColumns = append(selectColumns, sqlident.Quote(column.DatabaseColumn))
	}
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s = :1", strings.Join(selectColumns, ", "), qualifiedTable(table), sqlident.Quote(table.PrimaryKeyColumn))
	if forUpdate {
		query += " FOR UPDATE"
	}
	values := make([]any, len(columns))
	valuePointers := make([]any, len(columns))
	for i := range values {
		valuePointers[i] = &values[i]
	}
	if err := db.QueryRowContext(ctx, query, recordID).Scan(valuePointers...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			hash, _, hashErr := canonicalstate.HashState(r.clientID, resourceName(table, recordID), map[string]any{})
			if hashErr != nil {
				return StateSnapshot{}, hashErr
			}
			return StateSnapshot{Found: false, State: map[string]any{}, Hash: hash}, nil
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return StateSnapshot{}, fmt.Errorf("%w: query timeout", ErrDatabaseUnreachable)
		}
		return StateSnapshot{}, fmt.Errorf("%w: query state", ErrDatabaseUnreachable)
	}

	state := make(map[string]any, len(columns))
	for i, column := range columns {
		value, err := normalizeScanValue(values[i])
		if err != nil {
			return StateSnapshot{}, err
		}
		state[column.CanonicalField] = value
	}
	hash, _, err := canonicalstate.HashState(r.clientID, resourceName(table, recordID), state)
	if err != nil {
		return StateSnapshot{}, err
	}
	return StateSnapshot{Found: true, State: state, Hash: hash}, nil
}

func (r *OracleRepository) updateExisting(ctx context.Context, tx *sql.Tx, table TablePolicy, recordID string, desired map[string]any) error {
	columns := stateColumns(table)
	sets := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns)+1)
	for _, column := range columns {
		if !column.Update || strings.EqualFold(column.DatabaseColumn, table.PrimaryKeyColumn) {
			continue
		}
		value, ok := desired[column.CanonicalField]
		if !ok {
			return fmt.Errorf("%w: missing update column", ErrWriteRejected)
		}
		sets = append(sets, fmt.Sprintf("%s = :%d", sqlident.Quote(column.DatabaseColumn), len(args)+1))
		args = append(args, oracleValue(value))
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, recordID)
	query := fmt.Sprintf("UPDATE %s SET %s WHERE %s = :%d", qualifiedTable(table), strings.Join(sets, ", "), sqlident.Quote(table.PrimaryKeyColumn), len(args))
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%w: update rejected", ErrWriteRejected)
	}
	return requireOneRow(result)
}

func (r *OracleRepository) insertMissing(ctx context.Context, tx *sql.Tx, table TablePolicy, recordID string, desired map[string]any) error {
	columns := stateColumns(table)
	databaseColumns := make([]string, 0, len(columns))
	placeholders := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for _, column := range columns {
		if !column.Insert {
			continue
		}
		value, ok := desired[column.CanonicalField]
		if !ok {
			return fmt.Errorf("%w: missing insert column", ErrWriteRejected)
		}
		databaseColumns = append(databaseColumns, sqlident.Quote(column.DatabaseColumn))
		placeholders = append(placeholders, fmt.Sprintf(":%d", len(args)+1))
		args = append(args, oracleValue(value))
	}
	if len(databaseColumns) == 0 {
		return fmt.Errorf("%w: no insert columns", ErrWriteRejected)
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", qualifiedTable(table), strings.Join(databaseColumns, ", "), strings.Join(placeholders, ", "))
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%w: insert rejected", ErrWriteRejected)
	}
	return requireOneRow(result)
}

func stateColumns(table TablePolicy) []ColumnMapping {
	columns := make([]ColumnMapping, 0, len(table.Columns))
	for field, column := range table.Columns {
		if column.State {
			columns = append(columns, ColumnMapping{CanonicalField: field, ColumnPolicy: column})
		}
	}
	sort.Slice(columns, func(i, j int) bool { return columns[i].CanonicalField < columns[j].CanonicalField })
	return columns
}

type ColumnMapping struct {
	CanonicalField string
	ColumnPolicy
}

func qualifiedTable(table TablePolicy) string {
	return sqlident.Quote(table.Schema) + "." + sqlident.Quote(table.Table)
}

func resourceName(table TablePolicy, recordID string) string {
	return table.ResourceTable + ":" + recordID
}

func normalizeScanValue(value any) (any, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case []byte:
		return string(typed), nil
	case time.Time:
		return typed.UTC(), nil
	case string, bool, int64, float64:
		return typed, nil
	default:
		return nil, fmt.Errorf("unsupported Oracle state value %T", value)
	}
}

func oracleValue(value any) any {
	// json.Number is deliberately bound as text. This preserves large numbers
	// instead of routing them through float64; Oracle performs the column-type
	// conversion under the database's normal bind semantics.
	return value
}

func requireOneRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return fmt.Errorf("%w: expected one affected row", ErrWriteRejected)
	}
	return nil
}
