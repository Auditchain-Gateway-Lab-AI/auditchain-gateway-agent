package recovery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"auditchain-agent/internal/canonicalstate"
	"auditchain-agent/internal/config"
	"auditchain-agent/internal/verify"
)

var commandUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

type RecoveryCommandRequest struct {
	RequestID          string                 `json:"request_id"`
	IdempotencyKey     string                 `json:"idempotency_key"`
	Operation          string                 `json:"operation"`
	ExpectedBeforeHash string                 `json:"expected_before_hash"`
	DesiredStateHash   string                 `json:"desired_state_hash"`
	DesiredState       map[string]interface{} `json:"desired_state,omitempty"`
	Reference          struct {
		LogID         string `json:"log_id"`
		AuditLeafHash string `json:"audit_leaf_hash"`
		MerkleRoot    string `json:"merkle_root"`
		AnchorID      string `json:"anchor_id"`
	} `json:"reference"`
	IssuedAt time.Time `json:"issued_at"`
}

type RecoveryCommandResult struct {
	RequestID        string    `json:"request_id"`
	Operation        string    `json:"operation"`
	Applied          bool      `json:"applied"`
	IdempotentReplay bool      `json:"idempotent_replay"`
	BeforeHash       string    `json:"before_hash"`
	AfterHash        string    `json:"after_hash"`
	ReadbackMatch    bool      `json:"readback_match"`
	FoundAfter       bool      `json:"found_after"`
	CheckedAt        time.Time `json:"checked_at"`
}

type CommandFailure struct {
	Status  int
	Code    string
	Message string
}

func (e *CommandFailure) Error() string { return e.Code }

type commandStoreRecord struct {
	Fingerprint string                 `json:"fingerprint"`
	Status      string                 `json:"status"`
	Result      *RecoveryCommandResult `json:"result,omitempty"`
	Failure     *CommandFailure        `json:"failure,omitempty"`
	UpdatedAt   time.Time              `json:"updated_at"`
}

type CommandService struct {
	db         *sql.DB
	clientID   string
	enabled    bool
	statePath  string
	maxSkew    time.Duration
	maxColumns int
	timeout    time.Duration
	retention  time.Duration
	policy     *CommandPolicySet
	mu         sync.Mutex
	store      map[string]commandStoreRecord
}

func NewCommandService(db *sql.DB, recovery config.RecoveryConfig, readMapping func(string) (verify.ReadProjection, bool)) (*CommandService, error) {
	service := &CommandService{
		db: db, clientID: recovery.ClientID, enabled: recovery.Enabled,
		statePath: recovery.StatePath, maxSkew: time.Duration(recovery.MaxClockSkewSeconds) * time.Second,
		maxColumns: recovery.MaxColumns, timeout: time.Duration(recovery.RequestTimeoutSeconds) * time.Second,
		retention: time.Duration(recovery.RetentionDays) * 24 * time.Hour,
		store:     make(map[string]commandStoreRecord),
	}
	if service.maxSkew <= 0 {
		service.maxSkew = 5 * time.Minute
	}
	if service.maxColumns <= 0 {
		service.maxColumns = 64
	}
	if service.timeout <= 0 {
		service.timeout = 15 * time.Second
	}
	if service.retention <= 0 {
		service.retention = 90 * 24 * time.Hour
	}
	if !service.enabled {
		return service, nil
	}
	policy, err := LoadCommandPolicy(recovery.PolicyPath, readMapping)
	if err != nil {
		return nil, err
	}
	service.policy = policy
	if err := service.loadStore(); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *CommandService) Enabled() bool { return s != nil && s.enabled }

func (s *CommandService) Policy() *CommandPolicySet {
	if s == nil {
		return nil
	}
	return s.policy
}

func (s *CommandService) Execute(ctx context.Context, tableName, recordID string, request RecoveryCommandRequest) (*RecoveryCommandResult, *CommandFailure) {
	if s == nil || !s.enabled {
		return nil, commandFailure(403, "recovery_disabled", "Recovery write operations are disabled")
	}
	tableName = strings.ToUpper(strings.TrimSpace(tableName))
	recordID = strings.TrimSpace(recordID)
	if !verify.IsValidSQLIdentifier(tableName) || recordID == "" || len(recordID) > 128 || strings.ContainsAny(recordID, `/\\?#&`) {
		return nil, commandFailure(400, "invalid_payload", "table or record identifier is invalid")
	}
	if err := s.validateCommand(tableName, recordID, request); err != nil {
		return nil, err
	}
	fingerprint, err := commandFingerprint(request)
	if err != nil {
		return nil, commandFailure(400, "invalid_payload", "command payload is invalid")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.store[request.IdempotencyKey]; ok {
		if existing.Fingerprint != fingerprint {
			return nil, commandFailure(409, "idempotency_conflict", "idempotency key was already used for a different command")
		}
		switch existing.Status {
		case "COMPLETED":
			if existing.Result == nil {
				return nil, commandFailure(503, "execution_outcome_unknown", "recovery outcome could not be confirmed")
			}
			result := *existing.Result
			result.IdempotentReplay = true
			return &result, nil
		case "FAILED_SAFE":
			if existing.Failure != nil {
				return nil, existing.Failure
			}
			return nil, commandFailure(503, "execution_outcome_unknown", "recovery outcome could not be confirmed")
		default:
			return nil, commandFailure(503, "execution_outcome_unknown", "recovery outcome could not be confirmed")
		}
	}

	s.store[request.IdempotencyKey] = commandStoreRecord{Fingerprint: fingerprint, Status: "IN_PROGRESS", UpdatedAt: time.Now().UTC()}
	if err := s.persistStoreLocked(); err != nil {
		delete(s.store, request.IdempotencyKey)
		return nil, commandFailure(500, "idempotency_store_failed", "recovery command state could not be saved")
	}

	result, failure, outcomeUnknown := s.apply(ctx, tableName, recordID, request)
	if failure != nil {
		record := s.store[request.IdempotencyKey]
		if outcomeUnknown {
			record.Status = "OUTCOME_UNKNOWN"
		} else {
			record.Status = "FAILED_SAFE"
			record.Failure = failure
		}
		record.UpdatedAt = time.Now().UTC()
		s.store[request.IdempotencyKey] = record
		if err := s.persistStoreLocked(); err != nil {
			return nil, commandFailure(500, "idempotency_store_failed", "recovery command state could not be saved")
		}
		return nil, failure
	}

	record := s.store[request.IdempotencyKey]
	record.Status = "COMPLETED"
	record.Result = result
	record.UpdatedAt = time.Now().UTC()
	s.store[request.IdempotencyKey] = record
	if err := s.persistStoreLocked(); err != nil {
		record.Status = "OUTCOME_UNKNOWN"
		record.Result = nil
		record.UpdatedAt = time.Now().UTC()
		s.store[request.IdempotencyKey] = record
		_ = s.persistStoreLocked()
		return nil, commandFailure(500, "idempotency_store_failed", "recovery result could not be durably recorded")
	}
	return result, nil
}

func (s *CommandService) validateCommand(tableName, recordID string, request RecoveryCommandRequest) *CommandFailure {
	if !commandUUIDPattern.MatchString(strings.TrimSpace(request.RequestID)) || strings.TrimSpace(request.IdempotencyKey) == "" || len(request.IdempotencyKey) > 128 {
		return commandFailure(400, "invalid_payload", "request id or idempotency key is invalid")
	}
	if !isSHA3Hash(request.ExpectedBeforeHash) || !isSHA3Hash(request.DesiredStateHash) ||
		!isSHA3Hash(request.Reference.AuditLeafHash) || !isSHA3Hash(request.Reference.MerkleRoot) ||
		strings.TrimSpace(request.Reference.LogID) == "" || strings.TrimSpace(request.Reference.AnchorID) == "" {
		return commandFailure(400, "invalid_payload", "hash or trusted reference is invalid")
	}
	now := time.Now().UTC()
	if request.IssuedAt.IsZero() || request.IssuedAt.Before(now.Add(-s.maxSkew)) || request.IssuedAt.After(now.Add(s.maxSkew)) {
		return commandFailure(400, "invalid_payload", "issued_at is missing or outside the allowed clock skew")
	}
	table, ok := s.policy.table(tableName)
	if !ok {
		return commandFailure(403, "table_not_allowed", "table is not enabled by the Agent recovery policy")
	}
	operation := strings.ToUpper(strings.TrimSpace(request.Operation))
	if operation != "UPSERT" && operation != "DELETE" && operation != "NOOP" {
		return commandFailure(400, "invalid_payload", "operation is invalid")
	}
	if operation != "NOOP" && !table.allows(operation) {
		return commandFailure(403, "table_not_allowed", "operation is not enabled by the Agent recovery policy")
	}
	if operation == "DELETE" && len(request.DesiredState) != 0 {
		return commandFailure(400, "desired_state_invalid", "DELETE must not include desired_state")
	}
	if operation == "UPSERT" {
		if len(request.DesiredState) == 0 || len(request.DesiredState) > s.maxColumns {
			return commandFailure(400, "desired_state_invalid", "UPSERT desired_state is empty or too large")
		}
		if failure := validateDesiredState(table, recordID, request.DesiredState); failure != nil {
			return failure
		}
		desiredHash, _, err := canonicalstate.HashState(s.clientID, tableName+":"+recordID, request.DesiredState)
		if err != nil || !strings.EqualFold(desiredHash, request.DesiredStateHash) {
			return commandFailure(400, "desired_state_invalid", "desired_state_hash does not match desired_state")
		}
	} else {
		emptyHash, _, err := canonicalstate.HashState(s.clientID, tableName+":"+recordID, map[string]any{})
		if err != nil || !strings.EqualFold(emptyHash, request.DesiredStateHash) {
			return commandFailure(400, "desired_state_invalid", "empty desired state hash does not match the operation")
		}
	}
	return nil
}

func validateDesiredState(table CommandTablePolicy, recordID string, desired map[string]interface{}) *CommandFailure {
	for field, value := range desired {
		column, ok := table.Columns[field]
		if !ok || !column.State {
			return commandFailure(403, "column_not_allowed", "desired_state contains a field outside the Agent recovery policy")
		}
		if _, ok := value.(map[string]interface{}); ok {
			return commandFailure(400, "desired_state_invalid", "nested objects are not accepted as database field values")
		}
		if _, ok := value.([]interface{}); ok {
			return commandFailure(400, "desired_state_invalid", "arrays are not accepted as database field values")
		}
	}
	for field, column := range table.Columns {
		if column.State {
			if _, exists := desired[field]; !exists {
				return commandFailure(400, "desired_state_invalid", "desired_state must include every audited state field")
			}
		}
	}
	primaryKey, ok := desired[table.PrimaryKeyField]
	if !ok || strings.TrimSpace(fmt.Sprint(primaryKey)) != recordID {
		return commandFailure(400, "desired_state_invalid", "desired_state primary key does not match the path identifier")
	}
	return nil
}

func (s *CommandService) apply(ctx context.Context, tableName, recordID string, request RecoveryCommandRequest) (*RecoveryCommandResult, *CommandFailure, bool) {
	if s.db == nil {
		return nil, commandFailure(503, "database_unreachable", "client database is unavailable"), false
	}
	table, _ := s.policy.table(tableName)
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, commandFailure(503, "database_unreachable", "client database is unavailable"), false
	}
	defer tx.Rollback()

	beforeState, beforeFound, err := readCommandState(ctx, tx, table, recordID, true)
	if err != nil {
		return nil, commandFailure(503, "database_unreachable", "client database is unavailable"), false
	}
	resource := tableName + ":" + recordID
	beforeHash, _, err := canonicalstate.HashState(s.clientID, resource, beforeState)
	if err != nil {
		return nil, commandFailure(503, "database_unreachable", "client state could not be canonicalized"), false
	}
	if !strings.EqualFold(beforeHash, request.ExpectedBeforeHash) {
		return nil, commandFailure(409, "source_state_changed", "current client state no longer matches the recovery precondition"), false
	}

	operation := strings.ToUpper(strings.TrimSpace(request.Operation))
	if operation == "UPSERT" {
		if err := applyUpsert(ctx, tx, table, recordID, beforeState, beforeFound, request.DesiredState); err != nil {
			return nil, commandFailure(422, "write_rejected", "client database rejected the recovery write"), false
		}
	} else if operation == "DELETE" && beforeFound {
		query := fmt.Sprintf("DELETE FROM %s WHERE %s = :1", qualifiedTable(table), quoteSQLIdentifier(table.PrimaryKeyColumn))
		result, err := tx.ExecContext(ctx, query, recordID)
		if err != nil {
			return nil, commandFailure(422, "write_rejected", "client database rejected the recovery write"), false
		}
		if count, err := result.RowsAffected(); err == nil && count != 1 {
			return nil, commandFailure(409, "source_state_changed", "client row changed while recovery was being applied"), false
		}
	}

	afterState, afterFound, err := readCommandState(ctx, tx, table, recordID, false)
	if err != nil {
		return nil, commandFailure(500, "readback_mismatch", "recovery readback did not match the requested state"), false
	}
	afterHash, _, err := canonicalstate.HashState(s.clientID, resource, afterState)
	if err != nil || !strings.EqualFold(afterHash, request.DesiredStateHash) {
		return nil, commandFailure(500, "readback_mismatch", "recovery readback did not match the requested state"), false
	}
	if operation == "UPSERT" && !afterFound || operation == "DELETE" && afterFound {
		return nil, commandFailure(500, "readback_mismatch", "recovery readback did not match the requested state"), false
	}
	if err := tx.Commit(); err != nil {
		return nil, commandFailure(503, "execution_outcome_unknown", "client database commit outcome could not be confirmed"), true
	}
	return &RecoveryCommandResult{
		RequestID: request.RequestID, Operation: operation,
		Applied:    operation != "NOOP" && (operation == "DELETE" && beforeFound || operation == "UPSERT" && !strings.EqualFold(beforeHash, request.DesiredStateHash)),
		BeforeHash: beforeHash, AfterHash: afterHash, ReadbackMatch: true,
		FoundAfter: afterFound, CheckedAt: time.Now().UTC(),
	}, nil, false
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

func readCommandState(ctx context.Context, queryer queryRower, table CommandTablePolicy, recordID string, lock bool) (map[string]any, bool, error) {
	fields := make([]string, 0, len(table.Columns))
	for field, column := range table.Columns {
		if column.State {
			fields = append(fields, field)
		}
	}
	sort.Strings(fields)
	columns := make([]string, 0, len(fields))
	for _, field := range fields {
		columns = append(columns, quoteSQLIdentifier(table.Columns[field].DatabaseColumn))
	}
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s = :1", strings.Join(columns, ", "), qualifiedTable(table), quoteSQLIdentifier(table.PrimaryKeyColumn))
	if lock {
		query += " FOR UPDATE"
	}
	row := queryer.QueryRowContext(ctx, query, recordID)
	values := make([]interface{}, len(fields))
	valuePointers := make([]interface{}, len(fields))
	for index := range valuePointers {
		valuePointers[index] = &values[index]
	}
	if err := row.Scan(valuePointers...); errors.Is(err, sql.ErrNoRows) {
		return map[string]any{}, false, nil
	} else if err != nil {
		return nil, false, err
	}
	state := make(map[string]any, len(fields))
	for index, field := range fields {
		value := values[index]
		if bytesValue, ok := value.([]byte); ok {
			value = string(bytesValue)
		}
		state[field] = value
	}
	return state, true, nil
}

func applyUpsert(ctx context.Context, tx *sql.Tx, table CommandTablePolicy, recordID string, current map[string]any, exists bool, desired map[string]interface{}) error {
	fields := make([]string, 0, len(desired))
	for field := range desired {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	if exists {
		setColumns := make([]string, 0)
		args := make([]interface{}, 0)
		for _, field := range fields {
			column := table.Columns[field]
			if field == table.PrimaryKeyField || !column.Update {
				if !sameCanonicalValue(current[field], desired[field]) {
					return errors.New("immutable state differs")
				}
				continue
			}
			args = append(args, oracleValue(desired[field]))
			setColumns = append(setColumns, fmt.Sprintf("%s = :%d", quoteSQLIdentifier(column.DatabaseColumn), len(args)))
		}
		if len(setColumns) == 0 {
			return nil
		}
		args = append(args, recordID)
		query := fmt.Sprintf("UPDATE %s SET %s WHERE %s = :%d", qualifiedTable(table), strings.Join(setColumns, ", "), quoteSQLIdentifier(table.PrimaryKeyColumn), len(args))
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		if count, err := result.RowsAffected(); err == nil && count != 1 {
			return errors.New("update row count mismatch")
		}
		return nil
	}
	insertColumns := make([]string, 0, len(fields))
	placeholders := make([]string, 0, len(fields))
	args := make([]interface{}, 0, len(fields))
	for _, field := range fields {
		column := table.Columns[field]
		if !column.Insert {
			return errors.New("column is not insertable")
		}
		insertColumns = append(insertColumns, quoteSQLIdentifier(column.DatabaseColumn))
		args = append(args, oracleValue(desired[field]))
		placeholders = append(placeholders, fmt.Sprintf(":%d", len(args)))
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", qualifiedTable(table), strings.Join(insertColumns, ", "), strings.Join(placeholders, ", "))
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

func sameCanonicalValue(left, right any) bool {
	leftJSON, leftErr := canonicalstate.CanonicalizeObject(map[string]any{"value": left})
	rightJSON, rightErr := canonicalstate.CanonicalizeObject(map[string]any{"value": right})
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func qualifiedTable(table CommandTablePolicy) string {
	return quoteSQLIdentifier(table.Schema) + "." + quoteSQLIdentifier(table.Table)
}

func quoteSQLIdentifier(value string) string { return `"` + value + `"` }

func (s *CommandService) loadStore() error {
	if strings.TrimSpace(s.statePath) == "" {
		return fmt.Errorf("recovery state path is empty")
	}
	file, err := os.Open(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return s.persistStoreLocked()
	}
	if err != nil {
		return fmt.Errorf("open recovery state store: %w", err)
	}
	decoder := json.NewDecoder(io.LimitReader(file, 32<<20))
	if err := decoder.Decode(&s.store); err != nil {
		_ = file.Close()
		return fmt.Errorf("decode recovery state store: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close recovery state store: %w", err)
	}
	if s.store == nil {
		s.store = make(map[string]commandStoreRecord)
	}
	cutoff := time.Now().UTC().Add(-s.retention)
	for key, record := range s.store {
		if record.Status == "IN_PROGRESS" {
			record.Status = "OUTCOME_UNKNOWN"
			record.UpdatedAt = time.Now().UTC()
			s.store[key] = record
		}
		if record.Status != "OUTCOME_UNKNOWN" && !record.UpdatedAt.IsZero() && record.UpdatedAt.Before(cutoff) {
			delete(s.store, key)
		}
	}
	return s.persistStoreLocked()
}

func (s *CommandService) persistStoreLocked() error {
	if s.store == nil {
		s.store = make(map[string]commandStoreRecord)
	}
	if err := os.MkdirAll(filepath.Dir(s.statePath), 0o700); err != nil {
		return err
	}
	temporary := s.statePath + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(file).Encode(s.store)
	if encodeErr == nil {
		encodeErr = file.Sync()
	}
	closeErr := file.Close()
	if encodeErr != nil {
		_ = os.Remove(temporary)
		return encodeErr
	}
	if closeErr != nil {
		_ = os.Remove(temporary)
		return closeErr
	}
	if err := os.Rename(temporary, s.statePath); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func commandFingerprint(request RecoveryCommandRequest) (string, error) {
	canonicalDesired, err := canonicalstate.CanonicalizeObject(request.DesiredState)
	if err != nil {
		return "", err
	}
	semantic := struct {
		RequestID          string `json:"request_id"`
		IdempotencyKey     string `json:"idempotency_key"`
		Operation          string `json:"operation"`
		ExpectedBeforeHash string `json:"expected_before_hash"`
		DesiredStateHash   string `json:"desired_state_hash"`
		DesiredState       string `json:"desired_state"`
		LogID              string `json:"log_id"`
		AuditLeafHash      string `json:"audit_leaf_hash"`
		MerkleRoot         string `json:"merkle_root"`
		AnchorID           string `json:"anchor_id"`
	}{request.RequestID, request.IdempotencyKey, strings.ToUpper(strings.TrimSpace(request.Operation)), strings.ToLower(request.ExpectedBeforeHash), strings.ToLower(request.DesiredStateHash), string(canonicalDesired), request.Reference.LogID, strings.ToLower(request.Reference.AuditLeafHash), strings.ToLower(request.Reference.MerkleRoot), request.Reference.AnchorID}
	encoded, err := json.Marshal(semantic)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func isSHA3Hash(value string) bool {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	return err == nil && len(decoded) == 32
}

func commandFailure(status int, code, message string) *CommandFailure {
	return &CommandFailure{Status: status, Code: code, Message: message}
}
