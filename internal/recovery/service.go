package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"auditchain-agent/internal/canonicalstate"
)

type ServiceConfig struct {
	Enabled      bool
	ClientID     string
	MaxColumns   int
	MaxClockSkew time.Duration
	Retention    time.Duration
}

type Service struct {
	cfg     ServiceConfig
	policy  *PolicySet
	repo    Repository
	store   *IdempotencyStore
	locks   *ResourceLocker
	now     func() time.Time
	metrics recoveryMetrics
}

type recoveryMetrics struct {
	inProgress atomic.Int64
	requests   atomic.Uint64
	replays    atomic.Uint64
	conflicts  atomic.Uint64
	mismatches atomic.Uint64
	database   atomic.Uint64
}

func NewService(cfg ServiceConfig, policy *PolicySet, repo Repository, store *IdempotencyStore) *Service {
	if cfg.MaxColumns <= 0 {
		cfg.MaxColumns = 64
	}
	if cfg.MaxClockSkew <= 0 {
		cfg.MaxClockSkew = 5 * time.Minute
	}
	if cfg.Retention <= 0 {
		cfg.Retention = 90 * 24 * time.Hour
	}
	return &Service{
		cfg:    cfg,
		policy: policy,
		repo:   repo,
		store:  store,
		locks:  NewResourceLocker(),
		now:    time.Now,
	}
}

func (s *Service) Execute(ctx context.Context, tableName, recordID string, request RecoveryRequest) (RecoveryResponse, error) {
	started := s.now()
	s.metrics.requests.Add(1)
	if !s.cfg.Enabled {
		return RecoveryResponse{}, newAPIError("recovery_disabled", "recovery is disabled")
	}
	if s.policy == nil || s.repo == nil || s.store == nil {
		return RecoveryResponse{}, newAPIError("resource_mapping_not_found", "recovery is not configured")
	}

	table, ok := s.policy.Lookup(tableName)
	if !ok {
		return RecoveryResponse{}, newAPIError("table_not_allowed", "table is not allowed by local recovery policy")
	}
	if err := request.Validate(recordID, s.cfg.MaxColumns, started, s.cfg.MaxClockSkew); err != nil {
		return RecoveryResponse{}, err
	}
	if err := validateRequestAgainstPolicy(request, table, recordID, s.cfg.ClientID); err != nil {
		return RecoveryResponse{}, err
	}

	resource := table.ResourceTable + ":" + recordID
	fingerprint, err := SemanticFingerprint(request, resource)
	if err != nil {
		return RecoveryResponse{}, newAPIError("desired_state_invalid", "desired_state cannot be canonicalized")
	}

	release := s.locks.Lock(resource)
	defer release()
	if err := s.store.Cleanup(started); err != nil {
		return RecoveryResponse{}, newAPIError("idempotency_store_failed", "idempotency state could not be maintained")
	}
	decision, err := s.store.Begin(request.IdempotencyKey, fingerprint, request, resource, recordID, s.cfg.Retention, started)
	if err != nil {
		if apiErr := asAPIError(err); apiErr != nil {
			if apiErr.Code == "idempotency_conflict" {
				s.metrics.conflicts.Add(1)
			}
			return RecoveryResponse{}, apiErr
		}
		return RecoveryResponse{}, newAPIError("idempotency_store_failed", "idempotency state could not be saved")
	}

	record := decision.Record
	if record.Status == StatusCompleted {
		if record.StoredResponse == nil {
			return RecoveryResponse{}, newAPIError("idempotency_store_failed", "stored idempotency response is incomplete")
		}
		response := *record.StoredResponse
		response.IdempotentReplay = true
		s.metrics.replays.Add(1)
		s.logResult(request, resource, response, started)
		return response, nil
	}
	if record.Status == StatusOutcomeUnknown {
		return RecoveryResponse{}, newAPIError("execution_outcome_unknown", "previous execution outcome requires reconciliation")
	}

	s.metrics.inProgress.Add(1)
	defer s.metrics.inProgress.Add(-1)
	if record.Status == StatusInProgress && !decision.Created {
		resolved, continueApply, err := s.reconcile(ctx, table, record, request, resource)
		if err != nil {
			return RecoveryResponse{}, err
		}
		if !continueApply {
			return resolved, nil
		}
	}
	if record.Status == StatusFailedSafe {
		resolved, continueApply, err := s.reconcile(ctx, table, record, request, resource)
		if err != nil {
			return RecoveryResponse{}, err
		}
		if !continueApply {
			return resolved, nil
		}
	}

	result, err := s.repo.Apply(ctx, table, recordID, request)
	if err != nil {
		if errors.Is(err, ErrOutcomeUnknown) {
			if markErr := s.store.MarkOutcomeUnknown(request.IdempotencyKey, s.now()); markErr != nil {
				return RecoveryResponse{}, newAPIError("idempotency_store_failed", "outcome could not be durably recorded")
			}
			return RecoveryResponse{}, newAPIError("execution_outcome_unknown", "database commit outcome could not be determined")
		}
		if markErr := s.store.MarkFailedSafe(request.IdempotencyKey, s.now()); markErr != nil {
			return RecoveryResponse{}, newAPIError("idempotency_store_failed", "idempotency state could not be updated")
		}
		apiErr := mapRepositoryError(err)
		if apiErr.Code == "source_state_changed" {
			s.metrics.conflicts.Add(1)
		}
		if apiErr.Code == "readback_mismatch" {
			s.metrics.mismatches.Add(1)
		}
		if apiErr.Code == "database_unreachable" {
			s.metrics.database.Add(1)
		}
		return RecoveryResponse{}, apiErr
	}

	response := RecoveryResponse{
		RequestID:        request.RequestID,
		Operation:        request.Operation,
		Applied:          request.Operation != OperationNoop,
		IdempotentReplay: false,
		BeforeHash:       result.BeforeHash,
		AfterHash:        result.AfterHash,
		ReadbackMatch:    result.ReadbackMatch,
		FoundAfter:       result.FoundAfter,
		CheckedAt:        s.now().UTC(),
	}
	if !response.ReadbackMatch {
		if err := s.store.MarkFailedSafe(request.IdempotencyKey, s.now()); err != nil {
			return RecoveryResponse{}, newAPIError("idempotency_store_failed", "idempotency state could not be updated")
		}
		return RecoveryResponse{}, newAPIError("readback_mismatch", "database readback did not match desired state")
	}
	if err := s.store.Complete(request.IdempotencyKey, response, s.now()); err != nil {
		return RecoveryResponse{}, newAPIError("idempotency_store_failed", "database apply completed but durable completion could not be saved")
	}
	s.logResult(request, resource, response, started)
	return response, nil
}

func (s *Service) reconcile(ctx context.Context, table TablePolicy, record CommandRecord, request RecoveryRequest, resource string) (RecoveryResponse, bool, error) {
	current, err := s.repo.Read(ctx, table, record.RecordID)
	if err != nil {
		return RecoveryResponse{}, false, mapRepositoryError(err)
	}
	if current.Hash == request.DesiredStateHash {
		response := RecoveryResponse{
			RequestID:        request.RequestID,
			Operation:        request.Operation,
			Applied:          request.Operation != OperationNoop,
			IdempotentReplay: true,
			BeforeHash:       firstNonEmpty(record.BeforeHash, request.ExpectedBeforeHash),
			AfterHash:        current.Hash,
			ReadbackMatch:    true,
			FoundAfter:       current.Found,
			CheckedAt:        s.now().UTC(),
		}
		if err := s.store.MarkReconciled(request.IdempotencyKey, response, s.now()); err != nil {
			return RecoveryResponse{}, false, newAPIError("idempotency_store_failed", "reconciled completion could not be saved")
		}
		s.metrics.replays.Add(1)
		s.logResult(request, resource, response, s.now())
		return response, false, nil
	}
	if current.Hash == request.ExpectedBeforeHash {
		return RecoveryResponse{}, true, nil
	}
	if err := s.store.MarkOutcomeUnknown(request.IdempotencyKey, s.now()); err != nil {
		return RecoveryResponse{}, false, newAPIError("idempotency_store_failed", "outcome could not be reconciled")
	}
	return RecoveryResponse{}, false, newAPIError("execution_outcome_unknown", "current state matches neither the before nor desired state")
}

func validateRequestAgainstPolicy(request RecoveryRequest, table TablePolicy, recordID, clientID string) error {
	if !tableAllows(table, request.Operation) {
		return newAPIError("desired_state_invalid", "operation is not allowed for this table")
	}
	if request.Operation == OperationDelete {
		desiredHash, _, err := canonicalstate.HashState(clientID, table.ResourceTable+":"+recordID, map[string]any{})
		if err != nil || !strings.EqualFold(desiredHash, request.DesiredStateHash) {
			return newAPIError("desired_state_invalid", "DELETE desired_state_hash must represent an empty object")
		}
		return nil
	}

	seen := make(map[string]struct{}, len(request.DesiredState))
	for field, value := range request.DesiredState {
		normalized := strings.ToLower(strings.TrimSpace(field))
		if normalized != field {
			return newAPIError("column_not_allowed", "desired_state column name is not canonical")
		}
		column, ok := table.Columns[field]
		if !ok || !column.State {
			return newAPIError("column_not_allowed", "desired_state contains a column not allowed by policy")
		}
		switch value.(type) {
		case map[string]any, []any:
			return newAPIError("desired_state_invalid", "desired_state values must be scalar database values")
		}
		if _, exists := seen[field]; exists {
			return newAPIError("column_not_allowed", "desired_state contains duplicate columns")
		}
		seen[field] = struct{}{}
		if field == table.PrimaryKeyField && fmt.Sprint(value) != recordID {
			return newAPIError("desired_state_invalid", "primary key does not match record_id")
		}
	}
	for field, column := range table.Columns {
		if !column.State {
			continue
		}
		if _, ok := request.DesiredState[field]; !ok {
			return newAPIError("desired_state_invalid", "desired_state must contain the complete state projection")
		}
	}
	desiredHash, _, err := canonicalstate.HashState(clientID, table.ResourceTable+":"+recordID, request.DesiredState)
	if err != nil || !strings.EqualFold(desiredHash, request.DesiredStateHash) {
		return newAPIError("desired_state_invalid", "desired_state_hash does not match desired_state")
	}
	return nil
}

func tableAllows(table TablePolicy, operation Operation) bool {
	for _, allowed := range table.Operations {
		if allowed == operation {
			return true
		}
	}
	// NOOP is defensive and never writes. A policy that permits either real
	// recovery operation implicitly permits the read-only consistency check.
	if operation == OperationNoop {
		for _, allowed := range table.Operations {
			if allowed == OperationUpsert || allowed == OperationDelete {
				return true
			}
		}
	}
	return false
}

func mapRepositoryError(err error) *APIError {
	switch {
	case errors.Is(err, ErrSourceStateChanged):
		return newAPIError("source_state_changed", "current state no longer matches recovery precondition")
	case errors.Is(err, ErrReadbackMismatch):
		return newAPIError("readback_mismatch", "database readback did not match desired state")
	case errors.Is(err, ErrOutcomeUnknown):
		return newAPIError("execution_outcome_unknown", "database commit outcome could not be determined")
	case errors.Is(err, ErrDatabaseUnreachable):
		return newAPIError("database_unreachable", "client database is unavailable")
	case errors.Is(err, ErrWriteRejected):
		return newAPIError("write_rejected", "client database rejected the recovery write")
	default:
		return newAPIError("write_rejected", "recovery write could not be applied")
	}
}

func asAPIError(err error) *APIError {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return nil
}

func (s *Service) logResult(request RecoveryRequest, resource string, response RecoveryResponse, started time.Time) {
	keyHash := sha256.Sum256([]byte(request.IdempotencyKey))
	log.Printf("[Recovery] request_id=%s idempotency_key_hash=%s resource=%s operation=%s result_code=%s idempotent_replay=%t duration_ms=%d before_hash_prefix=%s after_hash_prefix=%s",
		request.RequestID,
		hex.EncodeToString(keyHash[:])[:16],
		sanitizeLogValue(resource),
		request.Operation,
		"success",
		response.IdempotentReplay,
		time.Since(started).Milliseconds(),
		prefix(response.BeforeHash),
		prefix(response.AfterHash),
	)
}

// MetricsHandler exposes only counters and gauges; no request payload, token,
// row, or idempotency key is included.
func (s *Service) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "# TYPE agent_recovery_requests_total counter\nagent_recovery_requests_total %d\n", s.metrics.requests.Load())
		_, _ = fmt.Fprintf(w, "# TYPE agent_recovery_idempotent_replay_total counter\nagent_recovery_idempotent_replay_total %d\n", s.metrics.replays.Load())
		_, _ = fmt.Fprintf(w, "# TYPE agent_recovery_precondition_conflict_total counter\nagent_recovery_precondition_conflict_total %d\n", s.metrics.conflicts.Load())
		_, _ = fmt.Fprintf(w, "# TYPE agent_recovery_readback_mismatch_total counter\nagent_recovery_readback_mismatch_total %d\n", s.metrics.mismatches.Load())
		_, _ = fmt.Fprintf(w, "# TYPE agent_recovery_database_error_total counter\nagent_recovery_database_error_total %d\n", s.metrics.database.Load())
		_, _ = fmt.Fprintf(w, "# TYPE agent_recovery_in_progress gauge\nagent_recovery_in_progress %d\n", s.metrics.inProgress.Load())
	})
}

func sanitizeLogValue(value string) string {
	value = strings.ReplaceAll(value, "\r", "?")
	value = strings.ReplaceAll(value, "\n", "?")
	if len(value) > 300 {
		return value[:300]
	}
	return value
}

func prefix(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
