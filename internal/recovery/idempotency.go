package recovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"auditchain-agent/internal/canonicalstate"
)

type CommandStatus string

const (
	StatusInProgress     CommandStatus = "IN_PROGRESS"
	StatusCompleted      CommandStatus = "COMPLETED"
	StatusFailedSafe     CommandStatus = "FAILED_SAFE"
	StatusOutcomeUnknown CommandStatus = "OUTCOME_UNKNOWN"
)

type CommandRecord struct {
	IdempotencyKey      string            `json:"idempotency_key"`
	RequestID           string            `json:"request_id"`
	SemanticFingerprint string            `json:"semantic_fingerprint"`
	Resource            string            `json:"resource"`
	Operation           Operation         `json:"operation"`
	RecordID            string            `json:"record_id"`
	Request             RecoveryRequest   `json:"request"`
	Status              CommandStatus     `json:"status"`
	BeforeHash          string            `json:"before_hash,omitempty"`
	AfterHash           string            `json:"after_hash,omitempty"`
	StoredResponse      *RecoveryResponse `json:"stored_response,omitempty"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
	ExpiresAt           time.Time         `json:"expires_at"`
}

type persistedStore struct {
	Version int                      `json:"version"`
	Records map[string]CommandRecord `json:"records"`
}

type IdempotencyStore struct {
	path    string
	mu      sync.Mutex
	records map[string]CommandRecord
}

type BeginDecision struct {
	Record  CommandRecord
	Created bool
}

var ErrIdempotencyStore = errors.New("idempotency store failure")

func OpenIdempotencyStore(path string) (*IdempotencyStore, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: empty state path", ErrIdempotencyStore)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create state directory: %v", ErrIdempotencyStore, err)
	}
	store := &IdempotencyStore{path: path, records: make(map[string]CommandRecord)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := ensureStatePermissions(path); err != nil {
			return nil, err
		}
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read state: %v", ErrIdempotencyStore, err)
	}
	var persisted persistedStore
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, fmt.Errorf("%w: corrupt state: %v", ErrIdempotencyStore, err)
	}
	if persisted.Version != 1 || persisted.Records == nil {
		return nil, fmt.Errorf("%w: unsupported state format", ErrIdempotencyStore)
	}
	store.records = persisted.Records
	if err := ensureStatePermissions(path); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *IdempotencyStore) Begin(key, fingerprint string, request RecoveryRequest, resource, recordID string, retention time.Duration, now time.Time) (BeginDecision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if record, ok := s.records[key]; ok {
		if record.SemanticFingerprint != fingerprint {
			return BeginDecision{Record: record}, newAPIError("idempotency_conflict", "idempotency key is already bound to another command")
		}
		return BeginDecision{Record: record}, nil
	}

	record := CommandRecord{
		IdempotencyKey:      key,
		RequestID:           request.RequestID,
		SemanticFingerprint: fingerprint,
		Resource:            resource,
		Operation:           request.Operation,
		RecordID:            recordID,
		Request:             request,
		Status:              StatusInProgress,
		CreatedAt:           now,
		UpdatedAt:           now,
		ExpiresAt:           now.Add(retention),
	}
	s.records[key] = record
	if err := s.persistLocked(); err != nil {
		delete(s.records, key)
		return BeginDecision{}, err
	}
	return BeginDecision{Record: record, Created: true}, nil
}

func (s *IdempotencyStore) Complete(key string, response RecoveryResponse, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key]
	if !ok {
		return fmt.Errorf("%w: command not found", ErrIdempotencyStore)
	}
	previous := record
	responseCopy := response
	record.Status = StatusCompleted
	record.BeforeHash = response.BeforeHash
	record.AfterHash = response.AfterHash
	record.StoredResponse = &responseCopy
	record.UpdatedAt = now
	s.records[key] = record
	if err := s.persistLocked(); err != nil {
		s.records[key] = previous
		return err
	}
	return nil
}

func (s *IdempotencyStore) MarkFailedSafe(key string, now time.Time) error {
	return s.mark(key, StatusFailedSafe, now)
}

func (s *IdempotencyStore) MarkOutcomeUnknown(key string, now time.Time) error {
	return s.mark(key, StatusOutcomeUnknown, now)
}

func (s *IdempotencyStore) MarkReconciled(key string, response RecoveryResponse, now time.Time) error {
	return s.Complete(key, response, now)
}

func (s *IdempotencyStore) Cleanup(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	removed := make(map[string]CommandRecord)
	for key, record := range s.records {
		if record.ExpiresAt.Before(now) && record.Status != StatusInProgress {
			removed[key] = record
			delete(s.records, key)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := s.persistLocked(); err != nil {
		for key, record := range removed {
			s.records[key] = record
		}
		return err
	}
	return nil
}

func (s *IdempotencyStore) Snapshot(key string) (CommandRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key]
	return record, ok
}

func (s *IdempotencyStore) mark(key string, status CommandStatus, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key]
	if !ok {
		return fmt.Errorf("%w: command not found", ErrIdempotencyStore)
	}
	previous := record
	record.Status = status
	record.UpdatedAt = now
	s.records[key] = record
	if err := s.persistLocked(); err != nil {
		s.records[key] = previous
		return err
	}
	return nil
}

func (s *IdempotencyStore) persistLocked() error {
	payload, err := json.Marshal(persistedStore{Version: 1, Records: s.records})
	if err != nil {
		return fmt.Errorf("%w: marshal state: %v", ErrIdempotencyStore, err)
	}
	directory := filepath.Dir(s.path)
	temporary, err := os.CreateTemp(directory, ".recovery-state-*")
	if err != nil {
		return fmt.Errorf("%w: create state temp: %v", ErrIdempotencyStore, err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%w: chmod state temp: %v", ErrIdempotencyStore, err)
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%w: write state: %v", ErrIdempotencyStore, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%w: sync state: %v", ErrIdempotencyStore, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("%w: close state: %v", ErrIdempotencyStore, err)
	}
	if err := os.Rename(temporaryName, s.path); err != nil {
		// Windows does not replace an existing file with Rename. The state file
		// belongs exclusively to this Agent, so replace it only after the new
		// contents have been fully written and synced.
		if removeErr := os.Remove(s.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("%w: replace state: %v", ErrIdempotencyStore, err)
		}
		if retryErr := os.Rename(temporaryName, s.path); retryErr != nil {
			return fmt.Errorf("%w: rename state: %v", ErrIdempotencyStore, retryErr)
		}
	}
	return ensureStatePermissions(s.path)
}

func ensureStatePermissions(path string) error {
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: state permissions: %v", ErrIdempotencyStore, err)
	}
	return nil
}

func SemanticFingerprint(request RecoveryRequest, resource string) (string, error) {
	canonical, err := canonicalSemantic(request, resource)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalSemantic(request RecoveryRequest, resource string) ([]byte, error) {
	// JSON marshaling a map is deterministic in supported Go versions, while
	// canonicalstate also normalizes all nested state keys and numbers.
	state, err := canonicalizeStateForFingerprint(request.DesiredState)
	if err != nil {
		return nil, err
	}
	value := map[string]any{
		"request_id":           request.RequestID,
		"idempotency_key":      request.IdempotencyKey,
		"operation":            string(request.Operation),
		"expected_before_hash": request.ExpectedBeforeHash,
		"desired_state_hash":   request.DesiredStateHash,
		"desired_state":        state,
		"reference":            request.Reference,
		"resource":             resource,
	}
	return json.Marshal(value)
}

func canonicalizeStateForFingerprint(state map[string]any) (any, error) {
	if state == nil {
		return map[string]any{}, nil
	}
	encoded, err := canonicalstate.CanonicalizeObject(state)
	if err != nil {
		return nil, err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}
