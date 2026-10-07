package recovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"go-blockchain-api/internal/blockchain/agentverifier"
	"go-blockchain-api/internal/engine/hasher"
	"go-blockchain-api/internal/models"
	"go-blockchain-api/pkg/canonicalstate"
	"go-blockchain-api/pkg/redaction"
)

const recoveryModeAgentDirect = "agent_direct"

func recoveryModeFromEnv() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("RECOVERY_MODE")))
	if mode == "" {
		return recoveryModeAgentDirect
	}
	return mode
}

// SetRecoveryMode is intentionally explicit so tests and a rolling deploy can
// choose the direct client-DB path without changing the legacy snapshot API.
func (s *Service) SetRecoveryMode(mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = recoveryModeAgentDirect
	}
	s.mode = mode
}

func (s *Service) SetAgentVerifier(agent *agentverifier.Service) {
	s.agent = agent
}

func (s *Service) directRecoveryEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(s.mode), recoveryModeAgentDirect)
}

type directRecoveryData struct {
	Incident     models.TamperIncident
	Log          models.AuditLog
	Reference    *TrustedReference
	Operation    string
	Desired      map[string]interface{}
	DesiredRaw   []byte
	DesiredHash  string
	Client       *agentverifier.ResourceRecord
	ClientHash   string
	SourceStatus string
	AgentStatus  string
}

func (s *Service) loadDirectRecoveryData(ctx context.Context, clientID, incidentID string) (*directRecoveryData, error) {
	if s.agent == nil {
		return nil, errors.New("agent_recovery_client_unavailable")
	}
	incident, logRow, reference, operation, err := s.loadDirectReference(ctx, clientID, incidentID)
	if err != nil {
		return nil, err
	}
	desired, desiredRaw, err := directDesiredState(&logRow, operation)
	if err != nil {
		return nil, err
	}
	desiredHash, err := agentverifier.HashResourceState(clientID, logRow.Resource, desired)
	if err != nil {
		return nil, fmt.Errorf("desired_state_hash_failed: %w", err)
	}
	clientRecord, clientHash, _, err := s.readDirectClient(ctx, clientID, logRow.Resource)
	if err != nil {
		return nil, err
	}
	sourceStatus := directSourceStatus(operation, clientRecord, clientHash, desiredHash)
	return &directRecoveryData{
		Incident: incident, Log: logRow, Reference: reference, Operation: operation,
		Desired: desired, DesiredRaw: desiredRaw, DesiredHash: desiredHash,
		Client: clientRecord, ClientHash: clientHash, SourceStatus: sourceStatus,
		AgentStatus: directAgentStatus(sourceStatus),
	}, nil
}

func (s *Service) loadDirectReference(ctx context.Context, clientID, incidentID string) (models.TamperIncident, models.AuditLog, *TrustedReference, string, error) {
	var incident models.TamperIncident
	if err := s.db.WithContext(ctx).Where("id = ? AND client_id = ?", incidentID, clientID).First(&incident).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.TamperIncident{}, models.AuditLog{}, nil, "", errors.New("incident_not_found")
		}
		return models.TamperIncident{}, models.AuditLog{}, nil, "", err
	}
	if !strings.EqualFold(strings.TrimSpace(incident.IncidentScope), models.RecoveryScopeClientSource) {
		// A direct client write is authorized only by an incident that proves a
		// mismatch in the operational client row. Gateway/Fabric integrity
		// incidents must remain read-only: their payload may itself be
		// untrusted, and Fabric stores a root rather than a recoverable row.
		return models.TamperIncident{}, models.AuditLog{}, nil, "", errors.New("client_source_incident_required")
	}
	if incident.Status == models.IncidentStatusResolved {
		return models.TamperIncident{}, models.AuditLog{}, nil, "", errors.New("incident_closed")
	}
	targetLogID := strings.TrimSpace(incident.ReferenceLogID)
	if targetLogID == "" {
		targetLogID = strings.TrimSpace(incident.LogID)
	}
	var logRow models.AuditLog
	if err := s.db.WithContext(ctx).Where("log_id = ? AND client_id = ?", targetLogID, clientID).First(&logRow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.TamperIncident{}, models.AuditLog{}, nil, "", errors.New("target_log_not_found")
		}
		return models.TamperIncident{}, models.AuditLog{}, nil, "", err
	}
	if strings.TrimSpace(incident.Resource) != "" && incident.Resource != logRow.Resource {
		return models.TamperIncident{}, models.AuditLog{}, nil, "", errors.New("reference_resource_mismatch")
	}
	if strings.EqualFold(strings.TrimSpace(logRow.Action), "RECOVERY") {
		return models.TamperIncident{}, models.AuditLog{}, nil, "", errors.New("recovery_reference_must_be_client_event")
	}
	var latest models.AuditLog
	if err := s.db.WithContext(ctx).
		Where("client_id = ? AND resource = ? AND COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY'", clientID, logRow.Resource).
		Order("COALESCE(db_timestamp, timestamp) DESC, timestamp DESC, log_id DESC").First(&latest).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.TamperIncident{}, models.AuditLog{}, nil, "", errors.New("reference_latest_client_event_missing")
		}
		return models.TamperIncident{}, models.AuditLog{}, nil, "", err
	}
	if latest.LogID != logRow.LogID {
		return models.TamperIncident{}, models.AuditLog{}, nil, "", errors.New("reference_not_latest_client_event")
	}
	reference, err := s.loadTrustedReference(ctx, &logRow)
	if err != nil {
		return models.TamperIncident{}, models.AuditLog{}, nil, "", err
	}
	operation, err := directOperation(logRow.Action)
	if err != nil {
		return models.TamperIncident{}, models.AuditLog{}, nil, "", err
	}
	return incident, logRow, reference, operation, nil
}

func (s *Service) loadTrustedReference(ctx context.Context, logRow *models.AuditLog) (*TrustedReference, error) {
	if logRow == nil {
		return nil, errors.New("reference_log_missing")
	}
	query := s.db.WithContext(ctx).Where("transaction_hash = ?", logRow.HashValue)
	if strings.TrimSpace(logRow.MerkleRoot) != "" {
		query = query.Where("merkle_root = ?", logRow.MerkleRoot)
	}
	var proofs []models.MerkleProof
	if err := query.Order("tree_level ASC").Find(&proofs).Error; err != nil {
		return nil, fmt.Errorf("reference_proof_read_failed: %w", err)
	}
	return ValidateTrustedReference(logRow, proofs, s.fabric)
}

func (s *Service) readDirectClient(ctx context.Context, clientID, resource string) (*agentverifier.ResourceRecord, string, string, error) {
	record, err := s.agent.ReadResource(ctx, clientID, resource)
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "unreachable") ||
			strings.Contains(message, "request ke agent") ||
			strings.Contains(message, "token verifikasi") ||
			strings.Contains(message, "agent mengembalikan status") ||
			strings.Contains(message, "gagal membaca response agent") ||
			strings.Contains(message, "gagal parse response agent") {
			return nil, "", models.SourceStatusUnreachable, fmt.Errorf("client_source_unreachable: %w", err)
		}
		return nil, "", models.SourceStatusNotComparable, err
	}
	state := record.Data
	if state == nil {
		state = map[string]interface{}{}
	}
	hash, err := agentverifier.HashResourceState(clientID, resource, state)
	if err != nil {
		return nil, "", models.SourceStatusNotComparable, fmt.Errorf("client_state_hash_failed: %w", err)
	}
	return record, hash, sourceStatusFor(record), nil
}

func sourceStatusFor(record *agentverifier.ResourceRecord) string {
	if record == nil || !record.Found {
		return models.SourceStatusMissing
	}
	return models.SourceStatusMatched
}

func directSourceStatus(operation string, record *agentverifier.ResourceRecord, currentHash, desiredHash string) string {
	switch operation {
	case models.RecoveryOperationDelete:
		if record == nil || !record.Found {
			return models.SourceStatusMatched
		}
		return models.SourceStatusUnexpectedPresent
	case models.RecoveryOperationUpsert:
		if record == nil || !record.Found {
			return models.SourceStatusMissing
		}
		if currentHash != desiredHash {
			return models.SourceStatusMismatch
		}
		return models.SourceStatusMatched
	default:
		return models.SourceStatusNotComparable
	}
}

func directAgentStatus(sourceStatus string) string {
	if sourceStatus == models.SourceStatusUnreachable {
		return "unreachable"
	}
	return strings.ToLower(sourceStatus)
}

func directOperation(action string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(action)) {
	case "INSERT", "UPDATE", "UPSERT":
		return models.RecoveryOperationUpsert, nil
	case "DELETE":
		return models.RecoveryOperationDelete, nil
	default:
		return "", fmt.Errorf("recovery_operation_unsupported: %s", action)
	}
}

func directDesiredState(logRow *models.AuditLog, operation string) (map[string]interface{}, []byte, error) {
	if operation == models.RecoveryOperationDelete {
		return map[string]interface{}{}, []byte(`{}`), nil
	}
	if strings.TrimSpace(logRow.Metadata) == "" {
		return nil, nil, errors.New("reference_metadata_missing")
	}
	object, canonical, err := canonicalstate.CanonicalizeObject([]byte(logRow.Metadata))
	if err != nil {
		return nil, nil, fmt.Errorf("reference_metadata_invalid: %w", err)
	}
	return object, canonical, nil
}

func (s *Service) directSourceMatches(data *directRecoveryData) bool {
	if data == nil {
		return false
	}
	switch data.Operation {
	case models.RecoveryOperationDelete:
		return data.Client == nil || !data.Client.Found
	case models.RecoveryOperationUpsert:
		return data.Client != nil && data.Client.Found && data.ClientHash == data.DesiredHash
	default:
		return false
	}
}

func (s *Service) listDirectCandidate(ctx context.Context, clientID, incidentID string) ([]CandidateView, error) {
	_, logRow, reference, operation, err := s.loadDirectReference(ctx, clientID, incidentID)
	if err != nil {
		return nil, err
	}
	return []CandidateView{{
		LogID: logRow.LogID, Resource: logRow.Resource, Operation: operation,
		ReferenceLogHash: reference.LeafHash, ReferenceMerkleRoot: reference.MerkleRoot,
		ReferenceAnchorID: reference.AnchorID,
		Eligible:          true,
	}}, nil
}

func (s *Service) listDirectVersions(ctx context.Context, clientID, resource string) ([]VersionView, error) {
	var logs []models.AuditLog
	if err := s.db.WithContext(ctx).
		Where("client_id = ? AND resource = ? AND COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY' AND status = ?", clientID, resource, "ANCHORED").
		Order("timestamp ASC").Find(&logs).Error; err != nil {
		return nil, err
	}
	versions := make([]VersionView, 0, len(logs))
	for _, logRow := range logs {
		versions = append(versions, VersionView{
			LogID: logRow.LogID, Action: logRow.Action, Actor: logRow.Actor,
			Resource: logRow.Resource, Timestamp: logRow.Timestamp, HashValue: logRow.HashValue,
			MerkleRoot: logRow.MerkleRoot, Status: logRow.Status,
			SnapshotStatus:  models.SnapshotStatusLegacyMissing,
			IntegrityStatus: logRow.IntegrityStatus,
		})
	}
	return versions, nil
}

func (s *Service) preflightDirect(ctx context.Context, clientID, incidentID string) (*PreflightResult, error) {
	data, err := s.loadDirectRecoveryData(ctx, clientID, incidentID)
	if err != nil {
		return nil, err
	}
	if data.SourceStatus == models.SourceStatusUnreachable {
		return nil, errors.New("client_source_unreachable")
	}
	recoverable := !s.directSourceMatches(data)
	status := "VALID"
	if !recoverable {
		status = "NO_RECOVERY_REQUIRED"
	}
	localHash := hasher.GenerateLogHash(&data.Log)
	currentIntegrity := data.Log.IntegrityStatus
	if localHash != data.Log.HashValue {
		currentIntegrity = models.IntegrityStatusTampered
	}
	metadata, err := redaction.Value(data.DesiredRaw)
	if err != nil {
		return nil, fmt.Errorf("reference_metadata_invalid: %w", err)
	}
	return &PreflightResult{
		Status: status, Recoverable: recoverable, LogID: data.Log.LogID,
		CurrentHash: localHash, CurrentIntegrity: currentIntegrity,
		SnapshotHash: data.Reference.LeafHash, MerkleRoot: data.Reference.MerkleRoot,
		AnchorID: data.Reference.AnchorID, ObjectVersionID: "",
		Operation: data.Operation, ReferenceLogHash: data.Reference.LeafHash,
		FabricRoot: data.Reference.FabricRoot, ClientStateHash: data.ClientHash,
		DesiredStateHash: data.DesiredHash, SourceStatus: data.SourceStatus,
		AgentStatus: data.AgentStatus, SourceFound: data.Client != nil && data.Client.Found,
		SnapshotPreview: SnapshotPreview{
			Actor: data.Log.Actor, Action: data.Log.Action, Resource: data.Log.Resource,
			Timestamp: data.Log.Timestamp, DBTimestamp: data.Log.DBTimestamp,
			SourceSystem: data.Log.SourceSystem, AuthorizationContext: data.Log.AuthorizationContext,
			SourceRecordID: data.Log.SourceRecordID, Metadata: metadata,
		},
	}, nil
}

func (s *Service) createDirectRequest(ctx context.Context, clientID, userID string, input CreateRequestInput) (*models.RecoveryRequest, error) {
	data, err := s.loadDirectRecoveryData(ctx, clientID, input.IncidentID)
	if err != nil {
		return nil, err
	}
	var active models.RecoveryRequest
	if err := s.db.WithContext(ctx).
		Where("client_id = ? AND resource = ? AND status IN ?", clientID, data.Log.Resource, []string{
			models.RecoveryStatusPendingExecution,
			models.RecoveryStatusPendingApproval,
			models.RecoveryStatusApproved,
			models.RecoveryStatusExecuting,
			models.RecoveryStatusAppliedAwaitingCDC,
		}).First(&active).Error; err == nil {
		return nil, errors.New("recovery_active_conflict")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if data.Log.LogID != input.SelectedLogID {
		return nil, errors.New("cross_log_recovery_not_allowed")
	}
	if data.SourceStatus == models.SourceStatusUnreachable {
		return nil, errors.New("client_source_unreachable")
	}
	if s.directSourceMatches(data) {
		return nil, errors.New("recovery_not_required")
	}
	now := time.Now().UTC()
	request := &models.RecoveryRequest{
		ID: uuid.NewString(), ClientID: clientID, IncidentID: data.Incident.ID,
		TargetLogID: data.Log.LogID, SelectedLogID: data.Log.LogID,
		Resource: data.Log.Resource, Operation: data.Operation,
		ReferenceLogHash: data.Reference.LeafHash, ReferenceMerkleRoot: data.Reference.MerkleRoot,
		ReferenceAnchorID: data.Reference.AnchorID, DesiredStateHash: data.DesiredHash,
		DesiredState: string(data.DesiredRaw), ClientBeforeHash: data.ClientHash,
		AgentCommandID: uuid.NewString(), CDCStatus: models.CDCStatusPending,
		CDCDeadlineAt: ptrTime(now.Add(recoveryCDCTimeout())), RequestedBy: userID,
		Reason: strings.TrimSpace(input.Reason), Status: models.RecoveryStatusPendingExecution,
		IdempotencyKey: strings.TrimSpace(input.IdempotencyKey), BeforeHash: data.ClientHash,
		AnchorID: data.Reference.AnchorID, ExpectedMerkleRoot: data.Reference.MerkleRoot,
	}
	if err := s.db.WithContext(ctx).Create(request).Error; err != nil {
		if strings.Contains(err.Error(), "idx_recovery_active_resource") {
			return nil, errors.New("recovery_active_conflict")
		}
		return nil, err
	}
	return request, nil
}

func (s *Service) executeDirect(ctx context.Context, clientID, requestID, executorID string) (*models.RecoveryRequest, error) {
	if s.agent == nil {
		return nil, errors.New("agent_recovery_client_unavailable")
	}
	var request models.RecoveryRequest
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND client_id = ?", requestID, clientID).First(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("request_not_found")
			}
			return err
		}
		if request.Status == models.RecoveryStatusAppliedAwaitingCDC {
			return nil
		}
		if request.Status != models.RecoveryStatusPendingExecution {
			return errors.New("invalid_request_state")
		}
		return tx.Model(&request).Updates(map[string]interface{}{
			"status": models.RecoveryStatusExecuting, "executed_by": executorID,
			"execution_started_at": time.Now().UTC(),
		}).Error
	}); err != nil {
		return nil, err
	}
	if request.Status == models.RecoveryStatusAppliedAwaitingCDC {
		return &request, nil
	}
	var incident models.TamperIncident
	if err := s.db.WithContext(ctx).Where("id = ? AND client_id = ?", request.IncidentID, clientID).First(&incident).Error; err != nil {
		return s.failDirectRequest(ctx, &request, executorID, err)
	}
	if incident.Status == models.IncidentStatusResolved {
		return s.failDirectRequest(ctx, &request, executorID, errors.New("incident_closed"))
	}
	var logRow models.AuditLog
	if err := s.db.WithContext(ctx).Where("log_id = ? AND client_id = ?", request.TargetLogID, clientID).First(&logRow).Error; err != nil {
		return s.failDirectRequest(ctx, &request, executorID, err)
	}
	reference, err := s.loadTrustedReference(ctx, &logRow)
	if err != nil {
		return s.failDirectRequest(ctx, &request, executorID, err)
	}
	if request.ReferenceLogHash != reference.LeafHash || request.ReferenceMerkleRoot != reference.MerkleRoot || request.ReferenceAnchorID != reference.AnchorID {
		return s.failDirectRequest(ctx, &request, executorID, errors.New("reference_changed"))
	}
	desired, desiredRaw, err := parseStoredDesiredState(request.DesiredState, request.Operation)
	if err != nil {
		return s.failDirectRequest(ctx, &request, executorID, err)
	}
	desiredHash, err := agentverifier.HashResourceState(clientID, request.Resource, desired)
	if err != nil || desiredHash != request.DesiredStateHash {
		return s.failDirectRequest(ctx, &request, executorID, errors.New("desired_state_hash_mismatch"))
	}
	_, clientHash, sourceStatus, err := s.readDirectClient(ctx, clientID, request.Resource)
	if err != nil {
		return s.failDirectRequest(ctx, &request, executorID, err)
	}
	if clientHash != request.ClientBeforeHash {
		return s.failDirectRequest(ctx, &request, executorID, errors.New("source_state_changed"))
	}
	command := agentverifier.RecoveryCommand{
		ClientID: clientID, RequestID: request.ID, IdempotencyKey: request.AgentCommandID,
		Operation: request.Operation, ExpectedBeforeHash: request.ClientBeforeHash,
		DesiredStateHash: request.DesiredStateHash, DesiredState: desired,
		Reference: agentverifier.RecoveryReference{LogID: request.TargetLogID, AuditLeafHash: request.ReferenceLogHash, MerkleRoot: request.ReferenceMerkleRoot, AnchorID: request.ReferenceAnchorID},
		IssuedAt:  time.Now().UTC(),
	}
	result, err := s.agent.RecoverResource(ctx, command, request.Resource)
	if err != nil {
		return s.failDirectRequest(ctx, &request, executorID, err)
	}
	if result == nil || !result.ReadbackMatch || result.AfterHash != request.DesiredStateHash {
		return s.failDirectRequest(ctx, &request, executorID, errors.New("readback_mismatch"))
	}
	now := time.Now().UTC()
	if err := s.persistDirectApplied(ctx, &request, &logRow, result, desiredRaw, sourceStatus, executorID, now); err != nil {
		return s.failDirectRequest(ctx, &request, executorID, err)
	}
	_ = s.db.WithContext(ctx).First(&request, "id = ? AND client_id = ?", request.ID, clientID).Error
	return &request, nil
}

// ReconcilePendingCDC closes the second half of a direct recovery. The Agent
// write/readback is not enough to declare the operation complete: Debezium
// must publish the resulting client event and the normal hashing pipeline must
// be able to anchor that event. Until then the request remains explicitly
// APPLIED_AWAITING_CDC.
func (s *Service) ReconcilePendingCDC(ctx context.Context, limit int) error {
	if !s.directRecoveryEnabled() || s.db == nil {
		return nil
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	// A gateway process can stop after moving a request to EXECUTING but before
	// receiving the Agent response. Do not leave that request invisible forever:
	// mark the outcome explicitly unknown/failed and keep the Agent command
	// evidence in recovery_events. A retry can be initiated as a new request
	// after the operator confirms the client state; execute itself remains
	// idempotent at the Agent boundary via agent_command_id.
	if err := s.reconcileStuckDirectExecutions(ctx, limit, time.Now().UTC()); err != nil {
		return err
	}

	var requests []models.RecoveryRequest
	if err := s.db.WithContext(ctx).Where("status = ? AND cdc_status = ?", models.RecoveryStatusAppliedAwaitingCDC, models.CDCStatusPending).
		Order("agent_applied_at ASC").Limit(limit).Find(&requests).Error; err != nil {
		return err
	}
	var firstErr error
	for i := range requests {
		if err := s.reconcileDirectRequest(ctx, &requests[i]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Service) reconcileStuckDirectExecutions(ctx context.Context, limit int, now time.Time) error {
	cutoff := now.Add(-recoveryCDCTimeout())
	var requests []models.RecoveryRequest
	if err := s.db.WithContext(ctx).
		Where("status = ? AND execution_started_at IS NOT NULL AND execution_started_at < ?", models.RecoveryStatusExecuting, cutoff).
		Order("execution_started_at ASC").Limit(limit).Find(&requests).Error; err != nil {
		return err
	}
	for i := range requests {
		_, _ = s.failDirectRequest(ctx, &requests[i], requests[i].ExecutedBy, errors.New("execution_timeout_unknown"))
	}
	return nil
}

func (s *Service) reconcileDirectRequest(ctx context.Context, request *models.RecoveryRequest) error {
	if request == nil {
		return errors.New("invalid_request")
	}
	now := time.Now().UTC()
	var target models.AuditLog
	if err := s.db.WithContext(ctx).Where("log_id = ? AND client_id = ?", request.TargetLogID, request.ClientID).First(&target).Error; err != nil {
		return err
	}
	if request.AgentAppliedAt == nil {
		return errors.New("cdc_start_time_missing")
	}
	var candidate models.AuditLog
	query := s.db.WithContext(ctx).
		Where("client_id = ? AND resource = ? AND COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY'", request.ClientID, request.Resource).
		Where("db_timestamp IS NOT NULL AND db_timestamp >= ?", request.AgentAppliedAt)
	// The upper bound prevents a delayed reconciliation run from claiming a
	// later, unrelated client mutation as proof of this recovery. If the
	// deadline has passed, include events through the deadline before marking
	// the request timed out.
	windowEnd := now
	if request.CDCDeadlineAt != nil && request.CDCDeadlineAt.Before(windowEnd) {
		windowEnd = request.CDCDeadlineAt.UTC()
	}
	query = query.Where("db_timestamp <= ?", windowEnd)
	query = query.Where("log_id <> ?", request.TargetLogID)
	if request.Operation == models.RecoveryOperationDelete {
		query = query.Where("UPPER(TRIM(action)) = 'DELETE'")
	} else {
		query = query.Where("UPPER(TRIM(action)) IN ?", []string{"INSERT", "UPDATE"})
	}
	var logs []models.AuditLog
	if err := query.Order("db_timestamp ASC").Limit(20).Find(&logs).Error; err != nil {
		return err
	}
	for i := range logs {
		if directCDCMatches(request, &logs[i]) {
			candidate = logs[i]
			break
		}
	}
	if candidate.LogID == "" {
		if request.CDCDeadlineAt != nil && now.After(request.CDCDeadlineAt.UTC()) {
			return s.markDirectCDCTimeout(ctx, request, now)
		}
		return nil
	}
	return s.markDirectCDCConfirmed(ctx, request, &candidate)
}

func directCDCMatches(request *models.RecoveryRequest, logRow *models.AuditLog) bool {
	if request == nil || logRow == nil {
		return false
	}
	if request.Operation == models.RecoveryOperationDelete {
		return strings.EqualFold(strings.TrimSpace(logRow.Action), "DELETE")
	}
	if strings.TrimSpace(request.DesiredStateHash) == "" {
		return false
	}
	object, _, err := canonicalstate.CanonicalizeObject([]byte(logRow.Metadata))
	if err != nil {
		return false
	}
	hash, err := agentverifier.HashResourceState(request.ClientID, request.Resource, object)
	return err == nil && hash == request.DesiredStateHash
}

func (s *Service) markDirectCDCConfirmed(ctx context.Context, request *models.RecoveryRequest, candidate *models.AuditLog) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current models.RecoveryRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ? AND cdc_status = ?", request.ID, models.RecoveryStatusAppliedAwaitingCDC, models.CDCStatusPending).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		var event models.RecoveryEvent
		if err := tx.Where("id = ? AND request_id = ?", current.RecoveryEventID, current.ID).First(&event).Error; err != nil {
			return err
		}
		event.ResultStatus = models.RecoveryResultSucceeded
		event.CDCStatus = models.CDCStatusConfirmed
		event.ResultAuditLogID = candidate.LogID
		event.EventHash = hasher.GenerateRecoveryEventHash(&event)
		if err := tx.Model(&event).Updates(map[string]interface{}{
			"result_status": event.ResultStatus, "cdc_status": event.CDCStatus,
			"result_audit_log_id": event.ResultAuditLogID, "event_hash": event.EventHash,
		}).Error; err != nil {
			return err
		}
		// CDC confirmation is necessary but not sufficient for completion. The
		// request and incident remain pending until this recovery event itself
		// has passed the normal hash/Merkle/Fabric verification path.
		return tx.Model(&current).Updates(map[string]interface{}{
			"cdc_status":          models.CDCStatusConfirmed,
			"result_audit_log_id": candidate.LogID,
		}).Error
	})
}

// finalizeDirectRecoveryIfVerified is called only after VerifyEvent has
// validated the direct recovery event against its Merkle proof and Fabric
// anchor. Keeping this transition here prevents an Agent HTTP 2xx or a bare
// CDC message from prematurely closing the incident.
func (s *Service) finalizeDirectRecoveryIfVerified(ctx context.Context, event *models.RecoveryEvent, now time.Time) error {
	if event == nil || event.CDCStatus != models.CDCStatusConfirmed || strings.TrimSpace(event.ResultAuditLogID) == "" {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var request models.RecoveryRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND client_id = ?", event.RequestID, event.ClientID).First(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if request.Status == models.RecoveryStatusSucceeded {
			return nil
		}
		if request.Status != models.RecoveryStatusAppliedAwaitingCDC || request.CDCStatus != models.CDCStatusConfirmed {
			return nil
		}
		if err := tx.Model(&request).Updates(map[string]interface{}{
			"status":              models.RecoveryStatusSucceeded,
			"cdc_status":          models.CDCStatusConfirmed,
			"result_audit_log_id": event.ResultAuditLogID,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&models.TamperIncident{}).
			Where("id = ? AND client_id = ? AND status <> ?", request.IncidentID, request.ClientID, models.IncidentStatusResolved).
			Updates(map[string]interface{}{"status": models.IncidentStatusResolved, "resolved_at": now}).Error
	})
}

func (s *Service) markDirectCDCTimeout(ctx context.Context, request *models.RecoveryRequest, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current models.RecoveryRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ? AND cdc_status = ?", request.ID, models.RecoveryStatusAppliedAwaitingCDC, models.CDCStatusPending).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		var event models.RecoveryEvent
		if err := tx.Where("id = ? AND request_id = ?", current.RecoveryEventID, current.ID).First(&event).Error; err != nil {
			return err
		}
		event.ResultStatus = models.RecoveryResultCDCTimeout
		event.CDCStatus = models.CDCStatusTimeout
		event.FailureCode = "cdc_confirmation_timeout"
		event.FailureReason = "event CDC hasil recovery belum diterima sampai batas waktu"
		event.EventHash = hasher.GenerateRecoveryEventHash(&event)
		if err := tx.Model(&event).Updates(map[string]interface{}{
			"result_status": event.ResultStatus, "cdc_status": event.CDCStatus,
			"failure_code": event.FailureCode, "failure_reason": event.FailureReason,
			"event_hash": event.EventHash,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&current).Updates(map[string]interface{}{
			"status": models.RecoveryStatusAppliedCDCTimeout, "cdc_status": models.CDCStatusTimeout,
			"failure_reason": event.FailureReason,
		}).Error
	})
}

func (s *Service) persistDirectApplied(ctx context.Context, request *models.RecoveryRequest, logRow *models.AuditLog, result *agentverifier.RecoveryResult, desiredRaw []byte, sourceStatus, executorID string, now time.Time) error {
	event := models.RecoveryEvent{
		ID: uuid.NewString(), ClientID: request.ClientID, RequestID: request.ID,
		IncidentID: request.IncidentID, TargetLogID: request.TargetLogID, SelectedLogID: request.SelectedLogID,
		EventType: models.RecoveryEventTypeExecution, ResultStatus: models.RecoveryResultAwaitingCDC,
		Operation: request.Operation, Resource: request.Resource, TargetActor: logRow.Actor,
		TargetAction: logRow.Action, TargetTimestamp: &logRow.Timestamp, SourceSystem: logRow.SourceSystem,
		TargetSourceSystem: logRow.SourceSystem, TargetAuthorization: logRow.AuthorizationContext,
		TargetSourceRecordID: logRow.SourceRecordID, RecoveredMetadata: redactedRecoveryMetadata(desiredRaw),
		ExecutorSystem: "AuditChain Gateway", ExecutedBy: executorID, Reason: request.Reason,
		BeforeHash: request.ClientBeforeHash, AfterHash: result.AfterHash,
		ReferenceLogHash: request.ReferenceLogHash, ReferenceMerkleRoot: request.ReferenceMerkleRoot,
		ReferenceAnchorID: request.ReferenceAnchorID, ClientBeforeHash: request.ClientBeforeHash,
		ClientAfterHash: result.AfterHash, ReadbackStatus: models.SourceStatusMatched,
		ExecutedAt: now, PipelineStatus: models.RecoveryPipelineHashed,
		SnapshotStatus: models.SnapshotStatusLegacyMissing, IntegrityStatus: models.IntegrityStatusNotChecked,
		CDCStatus: models.CDCStatusPending,
	}
	event.EventHash = hasher.GenerateRecoveryEventHash(&event)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&event).Error; err != nil {
			return fmt.Errorf("recovery event gagal: %w", err)
		}
		deadline := now.Add(recoveryCDCTimeout())
		updated := tx.Model(&models.RecoveryRequest{}).Where("id = ? AND status = ?", request.ID, models.RecoveryStatusExecuting).Updates(map[string]interface{}{
			"status": models.RecoveryStatusAppliedAwaitingCDC, "after_hash": result.AfterHash,
			"executed_at": now, "executed_by": executorID, "agent_applied_at": now,
			"cdc_status": models.CDCStatusPending, "cdc_deadline_at": deadline,
			"recovery_event_id": event.ID,
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 1 {
			return nil
		}
		// A retry may have completed the same idempotent Agent command and
		// persisted the event first. Treat that outcome as success; otherwise
		// fail closed instead of leaving an orphan event/request transition.
		var current models.RecoveryRequest
		if err := tx.Where("id = ?", request.ID).First(&current).Error; err != nil {
			return err
		}
		if current.Status == models.RecoveryStatusAppliedAwaitingCDC || current.Status == models.RecoveryStatusSucceeded {
			return nil
		}
		return errors.New("request_execution_state_changed")
	})
}

func (s *Service) failDirectRequest(ctx context.Context, request *models.RecoveryRequest, executorID string, failure error) (*models.RecoveryRequest, error) {
	if request == nil {
		return nil, failure
	}
	message := sanitizeFailure(failure)
	code := failureCode(failure)
	requestStatus, resultStatus := directFailureStatuses(code)
	_ = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := map[string]interface{}{
			"status": requestStatus, "failure_reason": message, "executed_by": executorID,
		}
		if strings.TrimSpace(request.RecoveryEventID) == "" {
			var target models.AuditLog
			_ = tx.Where("log_id = ? AND client_id = ?", request.TargetLogID, request.ClientID).First(&target).Error
			now := time.Now().UTC()
			event := models.RecoveryEvent{
				ID: uuid.NewString(), ClientID: request.ClientID, RequestID: request.ID,
				IncidentID: request.IncidentID, TargetLogID: request.TargetLogID, SelectedLogID: request.SelectedLogID,
				EventType: models.RecoveryEventTypeExecution, ResultStatus: resultStatus,
				Operation: request.Operation, Resource: request.Resource, TargetActor: target.Actor,
				TargetAction: target.Action, SourceSystem: target.SourceSystem, TargetSourceSystem: target.SourceSystem,
				TargetAuthorization: target.AuthorizationContext, TargetSourceRecordID: target.SourceRecordID,
				ExecutorSystem: "AuditChain Gateway", ExecutedBy: executorID, Reason: request.Reason,
				BeforeHash: request.ClientBeforeHash, ReferenceLogHash: request.ReferenceLogHash,
				ReferenceMerkleRoot: request.ReferenceMerkleRoot, ReferenceAnchorID: request.ReferenceAnchorID,
				FailureCode: code, FailureReason: message, ExecutedAt: now,
				PipelineStatus: models.RecoveryPipelineHashed, SnapshotStatus: models.SnapshotStatusLegacyMissing,
				IntegrityStatus: models.IntegrityStatusNotChecked, ReadbackStatus: models.SourceStatusNotComparable,
				CDCStatus: models.CDCStatusConflict,
			}
			if !target.Timestamp.IsZero() {
				timestamp := target.Timestamp
				event.TargetTimestamp = &timestamp
			}
			event.EventHash = hasher.GenerateRecoveryEventHash(&event)
			if err := tx.Create(&event).Error; err == nil {
				updates["recovery_event_id"] = event.ID
			}
		}
		return tx.Model(&models.RecoveryRequest{}).Where("id = ? AND client_id = ?", request.ID, request.ClientID).Updates(updates).Error
	})
	_ = s.db.WithContext(ctx).First(request, "id = ? AND client_id = ?", request.ID, request.ClientID).Error
	return request, fmt.Errorf("%s", code)
}

// directFailureStatuses distinguishes a failure to prove/authorize the
// target from a failure while applying the already-authorized Agent command.
// Keeping this distinction in persistence makes the request state useful to
// both the client UI and incident response without exposing raw Agent errors.
func directFailureStatuses(code string) (requestStatus, eventStatus string) {
	requestStatus = models.RecoveryStatusFailedExecution
	eventStatus = models.RecoveryResultExecution
	code = strings.ToLower(strings.TrimSpace(code))
	verification := code == "client_source_unreachable" ||
		code == "source_state_changed" ||
		code == "incident_closed" ||
		code == "record_not_found" ||
		code == "reference_changed" ||
		code == "reference_log_missing" ||
		code == "target_log_not_found" ||
		code == "reference_metadata_missing" ||
		code == "reference_metadata_invalid" ||
		code == "desired_state_missing" ||
		code == "desired_state_invalid" ||
		code == "desired_state_hash_mismatch" ||
		code == "client_state_hash_failed" ||
		code == "recovery_operation_unsupported" ||
		code == "request_execution_state_changed" ||
		strings.HasPrefix(code, "reference_") ||
		strings.HasPrefix(code, "fabric_")
	if verification {
		requestStatus = models.RecoveryStatusFailedVerification
		eventStatus = models.RecoveryResultVerification
	}
	return requestStatus, eventStatus
}

func parseStoredDesiredState(raw, operation string) (map[string]interface{}, []byte, error) {
	if operation == models.RecoveryOperationDelete && strings.TrimSpace(raw) == "" {
		return map[string]interface{}{}, []byte(`{}`), nil
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil, errors.New("desired_state_missing")
	}
	object, canonical, err := canonicalstate.CanonicalizeObject([]byte(raw))
	if err != nil {
		return nil, nil, fmt.Errorf("desired_state_invalid: %w", err)
	}
	return object, canonical, nil
}

// redactedRecoveryMetadata is used only for persisted recovery evidence. The
// unredacted canonical state remains in the request for the Agent command and
// is never exposed through the JSON model/API.
func redactedRecoveryMetadata(raw []byte) string {
	if strings.TrimSpace(string(raw)) == "" {
		return ""
	}
	redacted, err := redaction.JSON(raw)
	if err != nil {
		return "{}"
	}
	return string(redacted)
}

func ptrTime(value time.Time) *time.Time { return &value }

func recoveryCDCTimeout() time.Duration {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("RECOVERY_CDC_TIMEOUT_SECONDS")))
	if err != nil || value <= 0 {
		return 120 * time.Second
	}
	if value > 3600 {
		value = 3600
	}
	return time.Duration(value) * time.Second
}
