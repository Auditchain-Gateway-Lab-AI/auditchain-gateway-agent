package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"go-blockchain-api/internal/blockchain/agentverifier"
	"go-blockchain-api/internal/config"
	"go-blockchain-api/internal/engine/hasher"
	"go-blockchain-api/internal/models"
	"go-blockchain-api/internal/storage/snapshotstore"
	"go-blockchain-api/pkg/crypto"
	"go-blockchain-api/pkg/redaction"
)

type FabricReader interface {
	GetAnchorFromLedger(anchorID string) (string, error)
}

type Service struct {
	db              *gorm.DB
	store           snapshotstore.SnapshotStore
	cipher          *snapshotstore.Cipher
	fabric          FabricReader
	snapshotBuilder snapshotstore.RecoveryEventOutboxBuilder
	recoveryCutoff  *time.Time
	agent           *agentverifier.Service
	mode            string
}

type IncidentFilter struct {
	Status string
}

// IncidentDetailView keeps the encrypted evidence private while exposing the
// original metadata needed by the tenant-scoped recovery comparison UI.
type IncidentDetailView struct {
	models.TamperIncident
	TamperedMetadata          interface{} `json:"tampered_metadata,omitempty"`
	TamperedEvidenceAvailable bool        `json:"tampered_evidence_available"`
}

type VersionView struct {
	LogID              string     `json:"log_id"`
	Action             string     `json:"action"`
	Actor              string     `json:"actor"`
	Resource           string     `json:"resource"`
	Timestamp          time.Time  `json:"timestamp"`
	HashValue          string     `json:"hash_value"`
	MerkleRoot         string     `json:"merkle_root"`
	Status             string     `json:"status"`
	SnapshotStatus     string     `json:"snapshot_status"`
	SnapshotObjectKey  string     `json:"snapshot_object_key"`
	SnapshotVersionID  string     `json:"snapshot_version_id"`
	SnapshotVerifiedAt *time.Time `json:"snapshot_verified_at,omitempty"`
	IntegrityStatus    string     `json:"integrity_status"`
}

type CandidateView struct {
	LogID                 string     `json:"log_id"`
	Resource              string     `json:"resource,omitempty"`
	Operation             string     `json:"operation,omitempty"`
	RecoverySource        string     `json:"recovery_source,omitempty"`
	ReferenceLogHash      string     `json:"reference_log_hash,omitempty"`
	ReferenceMerkleRoot   string     `json:"reference_merkle_root,omitempty"`
	ReferenceAnchorID     string     `json:"reference_anchor_id,omitempty"`
	SourceStatus          string     `json:"source_status,omitempty"`
	SnapshotObjectKey     string     `json:"snapshot_object_key"`
	SnapshotVersionID     string     `json:"snapshot_version_id"`
	SnapshotChecksum      string     `json:"snapshot_checksum"`
	SnapshotPlaintextHash string     `json:"snapshot_plaintext_hash"`
	SnapshotVerifiedAt    *time.Time `json:"snapshot_verified_at,omitempty"`
	Eligible              bool       `json:"eligible"`
	Reason                string     `json:"reason,omitempty"`
}

type SnapshotPreview struct {
	Actor                string      `json:"actor"`
	Action               string      `json:"action"`
	Resource             string      `json:"resource"`
	Timestamp            time.Time   `json:"timestamp"`
	DBTimestamp          *time.Time  `json:"db_timestamp,omitempty"`
	SourceSystem         string      `json:"source_system"`
	AuthorizationContext string      `json:"authorization_context"`
	SourceRecordID       string      `json:"source_record_id,omitempty"`
	Metadata             interface{} `json:"metadata"`
}

type PreflightResult struct {
	Status                  string           `json:"status"`
	Recoverable             bool             `json:"recoverable"`
	LogID                   string           `json:"log_id"`
	RecoverySource          string           `json:"recovery_source,omitempty"`
	CurrentHash             string           `json:"current_hash"`
	CurrentIntegrity        string           `json:"current_integrity"`
	SnapshotHash            string           `json:"snapshot_hash"`
	MerkleRoot              string           `json:"merkle_root"`
	AnchorID                string           `json:"anchor_id"`
	ObjectVersionID         string           `json:"object_version_id"`
	SnapshotPreview         SnapshotPreview  `json:"snapshot_preview"`
	TrustedReferencePreview *SnapshotPreview `json:"trusted_reference_preview,omitempty"`
	Operation               string           `json:"operation,omitempty"`
	ReferenceLogHash        string           `json:"reference_log_hash,omitempty"`
	ReferenceMerkleRoot     string           `json:"reference_merkle_root,omitempty"`
	ReferenceAnchorID       string           `json:"reference_anchor_id,omitempty"`
	FabricRoot              string           `json:"fabric_root,omitempty"`
	ClientStateHash         string           `json:"client_state_hash,omitempty"`
	DesiredStateHash        string           `json:"desired_state_hash,omitempty"`
	SourceStatus            string           `json:"source_status,omitempty"`
	AgentStatus             string           `json:"agent_status,omitempty"`
	SourceFound             bool             `json:"source_found,omitempty"`
}

type CreateRequestInput struct {
	IncidentID     string `json:"incident_id" binding:"required"`
	SelectedLogID  string `json:"selected_log_id" binding:"required"`
	Reason         string `json:"reason" binding:"required,min=5"`
	IdempotencyKey string `json:"idempotency_key" binding:"required"`
}

func NewService(db *gorm.DB, store snapshotstore.SnapshotStore, cipher *snapshotstore.Cipher, fabric FabricReader, builders ...snapshotstore.RecoveryEventOutboxBuilder) *Service {
	var builder snapshotstore.RecoveryEventOutboxBuilder
	if len(builders) > 0 {
		builder = builders[0]
	}
	return &Service{db: db, store: store, cipher: cipher, fabric: fabric, snapshotBuilder: builder, mode: recoveryModeFromEnv()}
}

// SetRecoveryCutoff sets the default deployment boundary for recovery. Rows
// before the boundary remain legacy unless a prior successful recovery has
// already established a trusted snapshot lineage for the same log.
func (s *Service) SetRecoveryCutoff(cutoff *time.Time) {
	if cutoff == nil {
		s.recoveryCutoff = nil
		return
	}
	value := cutoff.UTC()
	s.recoveryCutoff = &value
}

func (s *Service) inRecoveryScope(logRow *models.AuditLog) bool {
	if logRow == nil {
		return false
	}
	return config.InRecoveryScope(logRow.DBTimestamp, s.recoveryCutoff)
}

// recoveryScopeAllowed keeps the deployment cutoff as the default boundary,
// but lets a log continue through the recovery workflow after a successful
// recovery has already established a trusted snapshot lineage for it.
//
// A recovery restores the audit row from the original snapshot, including its
// original db_timestamp. If that timestamp is NULL or predates the current
// deployment cutoff, a later tamper incident would otherwise become
// permanently unrecoverable even though the gateway has already validated and
// restored the exact same log once. The lineage exception does not skip any
// cryptographic checks: ListCandidates and Preflight still require the
// current row's verified snapshot references, and validateSnapshot is still
// executed before a request is persisted and during execution.
func (s *Service) recoveryScopeAllowed(ctx context.Context, clientID string, logRow *models.AuditLog) (bool, error) {
	if s.inRecoveryScope(logRow) {
		return true, nil
	}
	if s.db == nil || logRow == nil || strings.TrimSpace(clientID) == "" || strings.TrimSpace(logRow.LogID) == "" {
		return false, nil
	}

	var events []models.RecoveryEvent
	err := s.db.WithContext(ctx).
		Where("client_id = ? AND target_log_id = ? AND result_status = ?", clientID, logRow.LogID, models.RecoveryResultSucceeded).
		Order("executed_at DESC").
		Find(&events).Error
	if err != nil {
		return false, err
	}
	for i := range events {
		if recoveryEventHasTrustedLineage(&events[i]) {
			return true, nil
		}
	}
	return false, nil
}

func recoveryEventHasTrustedLineage(event *models.RecoveryEvent) bool {
	if event == nil || event.ResultStatus != models.RecoveryResultSucceeded {
		return false
	}
	return strings.TrimSpace(event.SourceSnapshotObjectKey) != "" &&
		strings.TrimSpace(event.SourceSnapshotVersionID) != "" &&
		strings.TrimSpace(event.SourceSnapshotChecksum) != "" &&
		strings.TrimSpace(event.SourceSnapshotPlainHash) != "" &&
		strings.TrimSpace(event.SourceAnchorID) != "" &&
		strings.TrimSpace(event.SourceExpectedMerkleRoot) != ""
}

func (s *Service) ListIncidents(ctx context.Context, clientID string, filter IncidentFilter) ([]models.TamperIncident, error) {
	query := s.db.WithContext(ctx).Where("client_id = ?", clientID).Order("detected_at DESC")
	if strings.TrimSpace(filter.Status) != "" {
		query = query.Where("status = ?", strings.ToUpper(strings.TrimSpace(filter.Status)))
	}
	var incidents []models.TamperIncident
	if err := query.Find(&incidents).Error; err != nil {
		return nil, err
	}
	return incidents, nil
}

func (s *Service) GetIncident(ctx context.Context, clientID, incidentID string) (*models.TamperIncident, error) {
	var incident models.TamperIncident
	if err := s.db.WithContext(ctx).Where("id = ? AND client_id = ?", incidentID, clientID).First(&incident).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("incident_not_found")
		}
		return nil, err
	}
	return &incident, nil
}

func (s *Service) GetIncidentDetail(ctx context.Context, clientID, incidentID string) (*IncidentDetailView, error) {
	incident, err := s.GetIncident(ctx, clientID, incidentID)
	if err != nil {
		return nil, err
	}

	detail := &IncidentDetailView{TamperIncident: *incident}
	if len(incident.TamperedPayload) == 0 || s.cipher == nil {
		return detail, nil
	}

	metadata, err := decryptTamperedMetadata(s.cipher, incident.TamperedPayload)
	if err != nil {
		return nil, fmt.Errorf("tampered_evidence_decrypt_failed: %w", err)
	}
	detail.TamperedMetadata = metadata
	detail.TamperedEvidenceAvailable = true
	return detail, nil
}

func decryptTamperedMetadata(cipher *snapshotstore.Cipher, encrypted []byte) (interface{}, error) {
	if cipher == nil {
		return nil, errors.New("tampered_evidence_cipher_unavailable")
	}
	plaintext, err := cipher.Decrypt(encrypted)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, err
	}
	if len(payload.Metadata) == 0 || string(payload.Metadata) == "null" {
		return nil, nil
	}

	// AuditLog.Metadata is stored as a JSON string, but accepting an object as
	// well keeps this reader compatible with older evidence formats. Both paths
	// must pass through the same redaction policy before evidence reaches a UI.
	metadataJSON := payload.Metadata
	var encodedMetadata string
	if err := json.Unmarshal(payload.Metadata, &encodedMetadata); err == nil {
		if strings.TrimSpace(encodedMetadata) == "" {
			return nil, nil
		}
		metadataJSON = []byte(encodedMetadata)
	}

	redacted, err := redaction.JSON(metadataJSON)
	if err != nil {
		return nil, err
	}
	var safe interface{}
	if err := json.Unmarshal(redacted, &safe); err != nil {
		return nil, err
	}
	return safe, nil
}

func (s *Service) ListRequests(ctx context.Context, clientID, status string) ([]models.RecoveryRequest, error) {
	query := s.db.WithContext(ctx).Where("client_id = ?", clientID).Order("requested_at DESC")
	if strings.TrimSpace(status) != "" {
		query = query.Where("status = ?", strings.ToUpper(strings.TrimSpace(status)))
	}
	var requests []models.RecoveryRequest
	if err := query.Find(&requests).Error; err != nil {
		return nil, err
	}
	return requests, nil
}

func (s *Service) GetRequest(ctx context.Context, clientID, requestID string) (*models.RecoveryRequest, error) {
	var request models.RecoveryRequest
	if err := s.db.WithContext(ctx).Where("id = ? AND client_id = ?", requestID, clientID).First(&request).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("request_not_found")
		}
		return nil, err
	}
	return &request, nil
}

func (s *Service) ListVersions(ctx context.Context, clientID, resource string) ([]VersionView, error) {
	if s.directRecoveryEnabled() {
		return s.listDirectVersions(ctx, clientID, resource)
	}
	var logs []models.AuditLog
	if err := s.db.WithContext(ctx).
		Where("client_id = ? AND resource = ? AND snapshot_status = ? AND COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY'", clientID, resource, models.SnapshotStatusVerified).
		Scopes(func(db *gorm.DB) *gorm.DB {
			if s.recoveryCutoff == nil {
				return db
			}
			return db.Where("db_timestamp IS NOT NULL AND db_timestamp >= ?", *s.recoveryCutoff)
		}).
		Order("timestamp ASC").
		Find(&logs).Error; err != nil {
		return nil, err
	}
	versions := make([]VersionView, 0, len(logs))
	for _, log := range logs {
		versions = append(versions, VersionView{
			LogID:              log.LogID,
			Action:             log.Action,
			Actor:              log.Actor,
			Resource:           log.Resource,
			Timestamp:          log.Timestamp,
			HashValue:          log.HashValue,
			MerkleRoot:         log.MerkleRoot,
			Status:             log.Status,
			SnapshotStatus:     log.SnapshotStatus,
			SnapshotObjectKey:  log.SnapshotObjectKey,
			SnapshotVersionID:  log.SnapshotVersionID,
			SnapshotVerifiedAt: log.SnapshotVerifiedAt,
			IntegrityStatus:    log.IntegrityStatus,
		})
	}
	return versions, nil
}

func (s *Service) ListCandidates(ctx context.Context, clientID, incidentID string) ([]CandidateView, error) {
	if s.directRecoveryEnabled() {
		var directIncident models.TamperIncident
		if err := s.db.WithContext(ctx).Where("id = ? AND client_id = ?", incidentID, clientID).First(&directIncident).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errors.New("incident_not_found")
			}
			return nil, err
		}
		if strings.EqualFold(strings.TrimSpace(directIncident.IncidentScope), models.RecoveryScopeGatewayIntegrity) {
			return s.listGatewayAuditTrailCandidate(ctx, clientID, incidentID)
		}
		return s.listDirectCandidate(ctx, clientID, incidentID)
	}
	var incident models.TamperIncident
	if err := s.db.WithContext(ctx).Where("id = ? AND client_id = ?", incidentID, clientID).First(&incident).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("incident_not_found")
		}
		return nil, err
	}
	var logRow models.AuditLog
	if err := s.db.WithContext(ctx).Where("log_id = ? AND client_id = ?", incident.LogID, clientID).First(&logRow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("snapshot_not_found")
		}
		return nil, err
	}
	candidate := CandidateView{
		LogID:                 logRow.LogID,
		SnapshotObjectKey:     logRow.SnapshotObjectKey,
		SnapshotVersionID:     logRow.SnapshotVersionID,
		SnapshotChecksum:      logRow.SnapshotChecksum,
		SnapshotPlaintextHash: logRow.SnapshotPlaintextHash,
		SnapshotVerifiedAt:    logRow.SnapshotVerifiedAt,
		Eligible:              logRow.SnapshotStatus == models.SnapshotStatusVerified && logRow.SnapshotObjectKey != "" && logRow.SnapshotVersionID != "" && logRow.SnapshotChecksum != "" && logRow.SnapshotPlaintextHash != "" && logRow.BlockchainTxID != nil && strings.TrimSpace(*logRow.BlockchainTxID) != "" && logRow.MerkleRoot != "",
	}
	scopeAllowed, err := s.recoveryScopeAllowed(ctx, clientID, &logRow)
	if err != nil {
		return nil, err
	}
	if !scopeAllowed {
		candidate.Eligible = false
		candidate.Reason = "legacy_recovery_out_of_scope"
		return []CandidateView{candidate}, nil
	}
	if !candidate.Eligible {
		switch {
		case logRow.SnapshotStatus != models.SnapshotStatusVerified:
			candidate.Reason = "snapshot_belum_verified"
		case logRow.SnapshotObjectKey == "" || logRow.SnapshotVersionID == "":
			candidate.Reason = "snapshot_reference_missing"
		case logRow.SnapshotChecksum == "":
			candidate.Reason = "snapshot_checksum_missing"
		case logRow.SnapshotPlaintextHash == "":
			candidate.Reason = "snapshot_plaintext_hash_missing"
		case logRow.BlockchainTxID == nil || strings.TrimSpace(*logRow.BlockchainTxID) == "":
			candidate.Reason = "anchor_missing"
		case logRow.MerkleRoot == "":
			candidate.Reason = "merkle_root_missing"
		}
	}
	return []CandidateView{candidate}, nil
}

func (s *Service) Preflight(ctx context.Context, clientID, incidentID string) (*PreflightResult, error) {
	if s.directRecoveryEnabled() {
		var directIncident models.TamperIncident
		if err := s.db.WithContext(ctx).Where("id = ? AND client_id = ?", incidentID, clientID).First(&directIncident).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errors.New("incident_not_found")
			}
			return nil, err
		}
		if strings.EqualFold(strings.TrimSpace(directIncident.IncidentScope), models.RecoveryScopeGatewayIntegrity) {
			return s.preflightGatewayAuditTrail(ctx, clientID, incidentID)
		}
		return s.preflightDirect(ctx, clientID, incidentID)
	}
	var incident models.TamperIncident
	if err := s.db.WithContext(ctx).Where("id = ? AND client_id = ?", incidentID, clientID).First(&incident).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("incident_not_found")
		}
		return nil, err
	}
	if incident.Status == models.IncidentStatusResolved {
		return nil, errors.New("incident_closed")
	}
	var logRow models.AuditLog
	if err := s.db.WithContext(ctx).Where("log_id = ? AND client_id = ?", incident.LogID, clientID).First(&logRow).Error; err != nil {
		return nil, errors.New("snapshot_not_found")
	}
	scopeAllowed, err := s.recoveryScopeAllowed(ctx, clientID, &logRow)
	if err != nil {
		return nil, err
	}
	if !scopeAllowed {
		return nil, errors.New("legacy_recovery_out_of_scope")
	}
	if logRow.SnapshotStatus != models.SnapshotStatusVerified {
		return nil, errors.New("snapshot_belum_verified")
	}
	request := &models.RecoveryRequest{
		ClientID:              clientID,
		TargetLogID:           incident.LogID,
		SelectedLogID:         incident.LogID,
		SnapshotObjectKey:     logRow.SnapshotObjectKey,
		SnapshotVersionID:     logRow.SnapshotVersionID,
		SnapshotChecksum:      logRow.SnapshotChecksum,
		SnapshotPlaintextHash: logRow.SnapshotPlaintextHash,
		ExpectedMerkleRoot:    logRow.MerkleRoot,
	}
	if logRow.BlockchainTxID != nil {
		request.AnchorID = *logRow.BlockchainTxID
	}
	snapshot, anchorRoot, _, err := s.validateSnapshot(ctx, request)
	if err != nil {
		return nil, err
	}
	currentHash := hasher.GenerateLogHash(&logRow)
	needsRecovery := currentHash != snapshot.HashValue || strings.TrimSpace(logRow.HashValue) != snapshot.HashValue
	preflightStatus := "VALID"
	currentIntegrity := models.IntegrityStatusTampered
	if !needsRecovery {
		preflightStatus = "NO_RECOVERY_REQUIRED"
		currentIntegrity = models.IntegrityStatusValid
	}
	metadata, err := redaction.Value(snapshot.Metadata)
	if err != nil {
		return nil, fmt.Errorf("snapshot_metadata_invalid: %w", err)
	}
	return &PreflightResult{
		Status:           preflightStatus,
		Recoverable:      needsRecovery,
		LogID:            snapshot.LogID,
		CurrentHash:      currentHash,
		CurrentIntegrity: currentIntegrity,
		SnapshotHash:     snapshot.HashValue,
		MerkleRoot:       anchorRoot,
		AnchorID:         request.AnchorID,
		ObjectVersionID:  request.SnapshotVersionID,
		SnapshotPreview: SnapshotPreview{
			Actor:                snapshot.Actor,
			Action:               snapshot.Action,
			Resource:             snapshot.Resource,
			Timestamp:            snapshot.Timestamp,
			DBTimestamp:          snapshot.DBTimestamp,
			SourceSystem:         snapshot.SourceSystem,
			AuthorizationContext: snapshot.AuthorizationContext,
			SourceRecordID:       snapshot.SourceRecordID,
			Metadata:             metadata,
		},
	}, nil
}

func (s *Service) CreateRequest(ctx context.Context, clientID, userID string, input CreateRequestInput) (*models.RecoveryRequest, error) {
	if strings.TrimSpace(input.Reason) == "" || strings.TrimSpace(input.IdempotencyKey) == "" {
		return nil, errors.New("invalid_request")
	}
	var existing models.RecoveryRequest
	if err := s.db.WithContext(ctx).Where("idempotency_key = ? AND client_id = ?", input.IdempotencyKey, clientID).First(&existing).Error; err == nil {
		if existing.IncidentID != input.IncidentID || existing.SelectedLogID != input.SelectedLogID || existing.Reason != strings.TrimSpace(input.Reason) {
			return nil, errors.New("idempotency_conflict")
		}
		return &existing, nil
	}
	if s.directRecoveryEnabled() {
		var directIncident models.TamperIncident
		if err := s.db.WithContext(ctx).Where("id = ? AND client_id = ?", input.IncidentID, clientID).First(&directIncident).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errors.New("incident_not_found")
			}
			return nil, err
		}
		if strings.EqualFold(strings.TrimSpace(directIncident.IncidentScope), models.RecoveryScopeGatewayIntegrity) {
			return s.createGatewayAuditTrailRequest(ctx, clientID, userID, input)
		}
		return s.createDirectRequest(ctx, clientID, userID, input)
	}

	var incident models.TamperIncident
	if err := s.db.WithContext(ctx).Where("id = ? AND client_id = ?", input.IncidentID, clientID).First(&incident).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("incident_not_found")
		}
		return nil, err
	}
	if incident.Status == models.IncidentStatusResolved {
		return nil, errors.New("incident_closed")
	}

	var selected models.AuditLog
	if err := s.db.WithContext(ctx).Where(
		"log_id = ? AND client_id = ? AND resource = ? AND snapshot_status = ? AND COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY'",
		input.SelectedLogID, clientID, incident.Resource, models.SnapshotStatusVerified,
	).First(&selected).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("snapshot_not_found")
		}
		return nil, err
	}
	scopeAllowed, err := s.recoveryScopeAllowed(ctx, clientID, &selected)
	if err != nil {
		return nil, err
	}
	if !scopeAllowed {
		return nil, errors.New("legacy_recovery_out_of_scope")
	}
	if selected.SnapshotObjectKey == "" || selected.SnapshotVersionID == "" {
		return nil, errors.New("snapshot_reference_missing")
	}
	if selected.SnapshotChecksum == "" {
		return nil, errors.New("snapshot_checksum_missing")
	}
	if selected.SnapshotPlaintextHash == "" {
		return nil, errors.New("snapshot_plaintext_hash_missing")
	}
	if selected.LogID != incident.LogID {
		return nil, errors.New("cross_log_recovery_not_allowed")
	}
	anchorID := ""
	if selected.BlockchainTxID != nil {
		anchorID = strings.TrimSpace(*selected.BlockchainTxID)
	}
	if anchorID == "" {
		return nil, errors.New("anchor_missing")
	}
	if strings.TrimSpace(selected.MerkleRoot) == "" {
		return nil, errors.New("merkle_root_missing")
	}
	beforeHash := strings.TrimSpace(incident.DetectedHash)
	if beforeHash == "" {
		// Incident lama mungkin tidak memiliki detected_hash. Tetap bekukan
		// hash expected sebagai fallback agar request legacy tidak rusak.
		beforeHash = selected.HashValue
	}

	request := &models.RecoveryRequest{
		ID:                uuid.NewString(),
		ClientID:          clientID,
		IncidentID:        incident.ID,
		TargetLogID:       incident.LogID,
		SelectedLogID:     selected.LogID,
		SnapshotObjectKey: selected.SnapshotObjectKey,
		SnapshotVersionID: selected.SnapshotVersionID,
		SnapshotChecksum:  selected.SnapshotChecksum,
		SnapshotPlaintextHash: func() string {
			if selected.SnapshotPlaintextHash != "" {
				return selected.SnapshotPlaintextHash
			}
			return selected.HashValue
		}(),
		AnchorID:           anchorID,
		ExpectedMerkleRoot: selected.MerkleRoot,
		RequestedBy:        userID,
		Reason:             strings.TrimSpace(input.Reason),
		Status:             models.RecoveryStatusPendingExecution,
		IdempotencyKey:     strings.TrimSpace(input.IdempotencyKey),
		BeforeHash:         beforeHash,
	}
	// The client-facing button must never create a request based only on a
	// cached preflight response. Verify the exact MinIO version and Fabric
	// anchor again before persisting the request, then ensure the current
	// PostgreSQL row still differs from the trusted snapshot.
	snapshot, _, _, err := s.validateSnapshot(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := s.ensureTargetNeedsRecovery(ctx, request, snapshot.HashValue); err != nil {
		return nil, err
	}
	// Incident lifecycle stays OPEN until the trusted snapshot is actually
	// restored. Request status carries the pending/executing outcome.
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Create(request).Error
	}); err != nil {
		return nil, err
	}
	return request, nil
}

func (s *Service) Approve(ctx context.Context, clientID, requestID, approverID string) (*models.RecoveryRequest, error) {
	// Deprecated: approval is no longer exposed by the client-operated API.
	// Keep the service method for backwards compatibility with legacy callers.
	return s.transitionRequest(ctx, clientID, requestID, approverID, models.RecoveryStatusApproved, models.RecoveryStatusPendingApproval)
}

func (s *Service) Reject(ctx context.Context, clientID, requestID, approverID string) (*models.RecoveryRequest, error) {
	// Deprecated: rejection is no longer exposed by the client-operated API.
	// Keep the service method for backwards compatibility with legacy callers.
	return s.transitionRequest(ctx, clientID, requestID, approverID, models.RecoveryStatusRejected, models.RecoveryStatusPendingApproval)
}

func (s *Service) transitionRequest(ctx context.Context, clientID, requestID, actorID, next, expected string) (*models.RecoveryRequest, error) {
	var request models.RecoveryRequest
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND client_id = ?", requestID, clientID).First(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("request_not_found")
			}
			return err
		}
		if request.Status != expected {
			return errors.New("invalid_request_state")
		}
		now := time.Now().UTC()
		updates := map[string]interface{}{"status": next}
		if next == models.RecoveryStatusApproved {
			updates["approved_by"] = actorID
			updates["approved_at"] = now
		} else {
			updates["failure_reason"] = "ditolak oleh approver"
		}
		if err := tx.Model(&request).Updates(updates).Error; err != nil {
			return err
		}
		request.Status = next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &request, nil
}

func (s *Service) Execute(ctx context.Context, clientID, requestID, executorID string) (*models.RecoveryRequest, error) {
	if s.directRecoveryEnabled() {
		var directRequest models.RecoveryRequest
		if err := s.db.WithContext(ctx).Where("id = ? AND client_id = ?", requestID, clientID).First(&directRequest).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errors.New("request_not_found")
			}
			return nil, err
		}
		var directIncident models.TamperIncident
		if err := s.db.WithContext(ctx).Where("id = ? AND client_id = ?", directRequest.IncidentID, clientID).First(&directIncident).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errors.New("incident_not_found")
			}
			return nil, err
		}
		if strings.EqualFold(strings.TrimSpace(directIncident.IncidentScope), models.RecoveryScopeGatewayIntegrity) {
			return s.executeGatewayAuditTrail(ctx, clientID, requestID, executorID)
		}
		return s.executeDirect(ctx, clientID, requestID, executorID)
	}
	if s.store == nil || s.cipher == nil || s.snapshotBuilder == nil {
		return nil, errors.New("recovery_storage_unavailable")
	}
	var request models.RecoveryRequest
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND client_id = ?", requestID, clientID).First(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("request_not_found")
			}
			return err
		}
		if !isClientExecutableStatus(request.Status) {
			return errors.New("invalid_request_state")
		}
		startedAt := time.Now().UTC()
		if err := tx.Model(&request).Updates(map[string]interface{}{
			"status":               models.RecoveryStatusExecuting,
			"execution_started_at": startedAt,
			"executed_by":          executorID,
		}).Error; err != nil {
			return err
		}
		// The incident remains OPEN while the request is executing; a
		// successful recovery changes it to RESOLVED in executeSnapshot.
		return nil
	}); err != nil {
		return nil, err
	}

	if err := s.executeSnapshot(ctx, &request, executorID); err != nil {
		if recordErr := s.recordFailedExecution(ctx, &request, executorID, err); recordErr != nil {
			return nil, recordErr
		}
		_ = s.db.WithContext(ctx).First(&request, "id = ? AND client_id = ?", request.ID, clientID).Error
		return &request, err
	}
	_ = s.db.WithContext(ctx).First(&request, "id = ? AND client_id = ?", request.ID, clientID).Error
	return &request, nil
}

// isClientExecutableStatus accepts the new client-operated state and the two
// approval-era states so requests created before this deployment are not
// stranded. New requests always use PENDING_EXECUTION.
func isClientExecutableStatus(status string) bool {
	switch status {
	case models.RecoveryStatusPendingExecution,
		models.RecoveryStatusPendingApproval,
		models.RecoveryStatusApproved:
		return true
	default:
		return false
	}
}

func (s *Service) ensureTargetNeedsRecovery(ctx context.Context, request *models.RecoveryRequest, expectedHash string) error {
	if request == nil {
		return errors.New("invalid_request")
	}
	var target models.AuditLog
	if err := s.db.WithContext(ctx).Where("log_id = ? AND client_id = ?", request.TargetLogID, request.ClientID).First(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("target_log_not_found")
		}
		return err
	}
	actualHash := hasher.GenerateLogHash(&target)
	if actualHash == expectedHash && strings.TrimSpace(target.HashValue) == expectedHash {
		return errors.New("recovery_not_required")
	}
	return nil
}

func (s *Service) executeSnapshot(ctx context.Context, request *models.RecoveryRequest, executorID string) error {
	snapshot, anchorRoot, _, err := s.validateSnapshot(ctx, request)
	if err != nil {
		return err
	}

	reconstructed := models.AuditLog{
		LogID:                snapshot.LogID,
		ClientID:             snapshot.ClientID,
		Actor:                snapshot.Actor,
		Action:               snapshot.Action,
		Resource:             snapshot.Resource,
		Timestamp:            snapshot.Timestamp,
		DBTimestamp:          snapshot.DBTimestamp,
		SourceSystem:         snapshot.SourceSystem,
		AuthorizationContext: snapshot.AuthorizationContext,
		Metadata:             string(snapshot.Metadata),
		SourceRecordID:       snapshot.SourceRecordID,
	}
	if hasher.GenerateLogHash(&reconstructed) != snapshot.HashValue {
		return errors.New("snapshot_hash_mismatch")
	}
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var target models.AuditLog
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("log_id = ? AND client_id = ?", request.TargetLogID, request.ClientID).First(&target).Error; err != nil {
			return errors.New("target_log_not_found")
		}
		currentHash := hasher.GenerateLogHash(&target)
		if currentHash == snapshot.HashValue && strings.TrimSpace(target.HashValue) == snapshot.HashValue {
			return errors.New("recovery_not_required")
		}
		tamperedPayload, err := json.Marshal(target)
		if err != nil {
			return fmt.Errorf("serialisasi nilai tampered gagal: %w", err)
		}
		tamperedEvidence, err := s.cipher.Encrypt(tamperedPayload)
		if err != nil {
			return fmt.Errorf("enkripsi bukti tampered gagal: %w", err)
		}
		updates := map[string]interface{}{
			"actor":                   reconstructed.Actor,
			"action":                  reconstructed.Action,
			"resource":                reconstructed.Resource,
			"timestamp":               reconstructed.Timestamp,
			"db_timestamp":            reconstructed.DBTimestamp,
			"source_system":           reconstructed.SourceSystem,
			"authorization_context":   reconstructed.AuthorizationContext,
			"source_record_id":        reconstructed.SourceRecordID,
			"metadata":                reconstructed.Metadata,
			"hash_value":              snapshot.HashValue,
			"merkle_root":             anchorRoot,
			"blockchain_tx_id":        request.AnchorID,
			"snapshot_status":         models.SnapshotStatusVerified,
			"snapshot_object_key":     request.SnapshotObjectKey,
			"snapshot_version_id":     request.SnapshotVersionID,
			"snapshot_checksum":       request.SnapshotChecksum,
			"snapshot_plaintext_hash": request.SnapshotPlaintextHash,
			"snapshot_last_error":     "",
			"integrity_status":        models.IntegrityStatusValid,
			"integrity_checked_at":    now,
			"integrity_error":         "",
			"integrity_source":        models.IntegritySourceRecovery,
			"integrity_run_id":        "",
			"status":                  "ANCHORED",
		}
		if err := tx.Model(&target).Updates(updates).Error; err != nil {
			return fmt.Errorf("restore audit log gagal: %w", err)
		}
		var restored models.AuditLog
		if err := tx.Where("log_id = ? AND client_id = ?", request.TargetLogID, request.ClientID).First(&restored).Error; err != nil {
			return fmt.Errorf("post-recovery readback gagal: %w", err)
		}
		if actualHash := hasher.GenerateLogHash(&restored); actualHash != snapshot.HashValue {
			return errors.New("post_recovery_hash_mismatch")
		}
		if err := tx.Model(&models.ClientDashboardStats{}).Where("client_id = ?", request.ClientID).Updates(map[string]interface{}{
			"last_integrity_check_at":     now,
			"last_integrity_check_source": models.IntegritySourceRecovery,
			"last_integrity_check_run_id": "",
			"last_integrity_check_logs":   1,
		}).Error; err != nil {
			return fmt.Errorf("ringkasan provenance recovery gagal diperbarui: %w", err)
		}
		if err := tx.Model(&models.TamperIncident{}).Where("id = ?", request.IncidentID).Updates(map[string]interface{}{
			"tampered_payload": tamperedEvidence,
			"status":           models.IncidentStatusResolved,
			"resolved_at":      now,
		}).Error; err != nil {
			return err
		}

		targetTimestamp := reconstructed.Timestamp
		recoveryEvent := models.RecoveryEvent{
			ID:                       uuid.NewString(),
			ClientID:                 request.ClientID,
			RequestID:                request.ID,
			IncidentID:               request.IncidentID,
			TargetLogID:              request.TargetLogID,
			SelectedLogID:            request.SelectedLogID,
			EventType:                models.RecoveryEventTypeExecution,
			ResultStatus:             models.RecoveryResultSucceeded,
			Resource:                 restored.Resource,
			TargetActor:              reconstructed.Actor,
			TargetAction:             reconstructed.Action,
			TargetTimestamp:          &targetTimestamp,
			SourceSystem:             reconstructed.SourceSystem,
			TargetSourceSystem:       reconstructed.SourceSystem,
			TargetAuthorization:      reconstructed.AuthorizationContext,
			TargetSourceRecordID:     reconstructed.SourceRecordID,
			RecoveredMetadata:        redactedRecoveryMetadata([]byte(reconstructed.Metadata)),
			ExecutorSystem:           "AuditChain Gateway",
			ExecutedBy:               executorID,
			Reason:                   request.Reason,
			BeforeHash:               request.BeforeHash,
			AfterHash:                snapshot.HashValue,
			SourceSnapshotObjectKey:  request.SnapshotObjectKey,
			SourceSnapshotVersionID:  request.SnapshotVersionID,
			SourceSnapshotChecksum:   request.SnapshotChecksum,
			SourceSnapshotPlainHash:  request.SnapshotPlaintextHash,
			SourceAnchorID:           request.AnchorID,
			SourceExpectedMerkleRoot: request.ExpectedMerkleRoot,
			ExecutedAt:               now,
			PipelineStatus:           models.RecoveryPipelineHashed,
			SnapshotStatus:           models.SnapshotStatusPending,
			IntegrityStatus:          models.IntegrityStatusNotChecked,
		}
		recoveryEvent.EventHash = hasher.GenerateRecoveryEventHash(&recoveryEvent)
		if err := tx.Create(&recoveryEvent).Error; err != nil {
			return fmt.Errorf("recovery event gagal: %w", err)
		}
		if s.snapshotBuilder == nil {
			return errors.New("recovery_snapshot_builder_unavailable")
		}
		recoveryOutbox, err := s.snapshotBuilder.BuildRecoveryEvent(recoveryEvent)
		if err != nil {
			return fmt.Errorf("snapshot recovery event gagal: %w", err)
		}
		if err := tx.Create(recoveryOutbox).Error; err != nil {
			return fmt.Errorf("snapshot outbox recovery event gagal: %w", err)
		}
		if err := tx.Model(&models.RecoveryRequest{}).Where("id = ?", request.ID).Updates(map[string]interface{}{
			"status":            models.RecoveryStatusSucceeded,
			"after_hash":        snapshot.HashValue,
			"executed_at":       now,
			"executed_by":       executorID,
			"recovery_event_id": recoveryEvent.ID,
		}).Error; err != nil {
			return err
		}
		return nil
	})
}

func (s *Service) recordFailedExecution(ctx context.Context, request *models.RecoveryRequest, executorID string, failure error) error {
	if request == nil {
		return errors.New("invalid_request")
	}
	now := time.Now().UTC()
	var target models.AuditLog
	if err := s.db.WithContext(ctx).Where("log_id = ? AND client_id = ?", request.TargetLogID, request.ClientID).First(&target).Error; err != nil {
		return err
	}
	resultStatus := models.RecoveryResultVerification
	if strings.Contains(strings.ToLower(failure.Error()), "restore audit log") || strings.Contains(strings.ToLower(failure.Error()), "post_recovery") {
		resultStatus = models.RecoveryResultExecution
	}
	event := models.RecoveryEvent{
		ID:                       uuid.NewString(),
		ClientID:                 request.ClientID,
		RequestID:                request.ID,
		IncidentID:               request.IncidentID,
		TargetLogID:              request.TargetLogID,
		SelectedLogID:            request.SelectedLogID,
		EventType:                models.RecoveryEventTypeExecution,
		ResultStatus:             resultStatus,
		Resource:                 target.Resource,
		TargetActor:              target.Actor,
		TargetAction:             target.Action,
		TargetTimestamp:          &target.Timestamp,
		SourceSystem:             target.SourceSystem,
		TargetSourceSystem:       target.SourceSystem,
		TargetAuthorization:      target.AuthorizationContext,
		TargetSourceRecordID:     target.SourceRecordID,
		ExecutorSystem:           "AuditChain Gateway",
		ExecutedBy:               executorID,
		Reason:                   request.Reason,
		BeforeHash:               request.BeforeHash,
		SourceSnapshotObjectKey:  request.SnapshotObjectKey,
		SourceSnapshotVersionID:  request.SnapshotVersionID,
		SourceSnapshotChecksum:   request.SnapshotChecksum,
		SourceSnapshotPlainHash:  request.SnapshotPlaintextHash,
		SourceAnchorID:           request.AnchorID,
		SourceExpectedMerkleRoot: request.ExpectedMerkleRoot,
		FailureCode:              failureCode(failure),
		FailureReason:            sanitizeFailure(failure),
		ExecutedAt:               now,
		PipelineStatus:           models.RecoveryPipelineHashed,
		SnapshotStatus:           models.SnapshotStatusPending,
		IntegrityStatus:          models.IntegrityStatusNotChecked,
	}
	event.EventHash = hasher.GenerateRecoveryEventHash(&event)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing models.RecoveryEvent
		if err := tx.Where("request_id = ?", request.ID).First(&existing).Error; err == nil {
			return tx.Model(&models.RecoveryRequest{}).Where("id = ?", request.ID).Updates(map[string]interface{}{
				"status": models.RecoveryStatusFailedVerification, "failure_reason": sanitizeFailure(failure), "executed_by": executorID,
			}).Error
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Create(&event).Error; err != nil {
			return err
		}
		if s.snapshotBuilder == nil {
			return errors.New("recovery_snapshot_builder_unavailable")
		}
		outbox, err := s.snapshotBuilder.BuildRecoveryEvent(event)
		if err != nil {
			return err
		}
		if err := tx.Create(outbox).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.RecoveryRequest{}).Where("id = ?", request.ID).Updates(map[string]interface{}{
			"status": models.RecoveryStatusFailedVerification, "failure_reason": sanitizeFailure(failure), "executed_by": executorID,
			"executed_at": now, "recovery_event_id": event.ID,
		}).Error; err != nil {
			return err
		}
		return nil
	})
}

func failureCode(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if index := strings.IndexByte(message, ':'); index > 0 {
		message = message[:index]
	}
	message = strings.ToLower(strings.TrimSpace(message))
	message = strings.ReplaceAll(message, " ", "_")
	if len(message) > 100 {
		message = message[:100]
	}
	return message
}

func (s *Service) validateSnapshot(ctx context.Context, request *models.RecoveryRequest) (snapshotstore.AuditSnapshot, string, snapshotstore.ObjectInfo, error) {
	if s.store == nil || s.cipher == nil {
		return snapshotstore.AuditSnapshot{}, "", snapshotstore.ObjectInfo{}, errors.New("recovery_storage_unavailable")
	}
	if request == nil {
		return snapshotstore.AuditSnapshot{}, "", snapshotstore.ObjectInfo{}, errors.New("invalid_request")
	}
	if request.SnapshotObjectKey == "" || request.SnapshotVersionID == "" {
		return snapshotstore.AuditSnapshot{}, "", snapshotstore.ObjectInfo{}, errors.New("snapshot_reference_missing")
	}
	payload, info, err := s.store.GetVersion(ctx, request.SnapshotObjectKey, request.SnapshotVersionID)
	if err != nil {
		return snapshotstore.AuditSnapshot{}, "", snapshotstore.ObjectInfo{}, fmt.Errorf("snapshot_read_failed: %w", err)
	}
	if strings.TrimSpace(request.SnapshotPlaintextHash) == "" {
		return snapshotstore.AuditSnapshot{}, "", snapshotstore.ObjectInfo{}, errors.New("snapshot_plaintext_hash_missing")
	}
	if strings.TrimSpace(request.AnchorID) == "" {
		return snapshotstore.AuditSnapshot{}, "", snapshotstore.ObjectInfo{}, errors.New("anchor_missing")
	}
	if strings.TrimSpace(request.ExpectedMerkleRoot) == "" {
		return snapshotstore.AuditSnapshot{}, "", snapshotstore.ObjectInfo{}, errors.New("merkle_root_missing")
	}
	if err := snapshotstore.ValidateObjectInfo(info, request.SnapshotObjectKey, request.SnapshotVersionID, request.SnapshotChecksum); err != nil {
		return snapshotstore.AuditSnapshot{}, "", snapshotstore.ObjectInfo{}, err
	}
	plaintext, err := s.cipher.Decrypt(payload)
	if err != nil {
		return snapshotstore.AuditSnapshot{}, "", snapshotstore.ObjectInfo{}, fmt.Errorf("snapshot_decrypt_failed: %w", err)
	}
	snapshot, err := snapshotstore.ParseAuditSnapshot(plaintext)
	if err != nil {
		return snapshotstore.AuditSnapshot{}, "", snapshotstore.ObjectInfo{}, fmt.Errorf("snapshot_invalid: %w", err)
	}
	if err := validateSnapshotIdentity(snapshot, request); err != nil {
		return snapshotstore.AuditSnapshot{}, "", snapshotstore.ObjectInfo{}, err
	}
	reconstructed := models.AuditLog{
		LogID:                snapshot.LogID,
		ClientID:             snapshot.ClientID,
		Actor:                snapshot.Actor,
		Action:               snapshot.Action,
		Resource:             snapshot.Resource,
		Timestamp:            snapshot.Timestamp,
		DBTimestamp:          snapshot.DBTimestamp,
		SourceSystem:         snapshot.SourceSystem,
		AuthorizationContext: snapshot.AuthorizationContext,
		Metadata:             string(snapshot.Metadata),
		SourceRecordID:       snapshot.SourceRecordID,
	}
	if hasher.GenerateLogHash(&reconstructed) != snapshot.HashValue {
		return snapshotstore.AuditSnapshot{}, "", snapshotstore.ObjectInfo{}, errors.New("snapshot_hash_mismatch")
	}
	anchorRoot, err := s.verifyAnchor(ctx, snapshot.HashValue, request.TargetLogID, request.ClientID, request.AnchorID, request.ExpectedMerkleRoot)
	if err != nil {
		return snapshotstore.AuditSnapshot{}, "", snapshotstore.ObjectInfo{}, err
	}
	return snapshot, anchorRoot, info, nil
}

func validateSnapshotIdentity(snapshot snapshotstore.AuditSnapshot, request *models.RecoveryRequest) error {
	if request == nil {
		return errors.New("invalid_request")
	}
	if snapshot.LogID != request.TargetLogID || snapshot.LogID != request.SelectedLogID || snapshot.ClientID != request.ClientID {
		return errors.New("cross_log_recovery_not_allowed")
	}
	if request.SnapshotPlaintextHash == "" {
		return errors.New("snapshot_plaintext_hash_missing")
	}
	if snapshot.HashValue != request.SnapshotPlaintextHash {
		return errors.New("snapshot_plaintext_hash_mismatch")
	}
	return nil
}

func (s *Service) verifyAnchor(ctx context.Context, hash, logID, clientID, requestedAnchorID, expectedRoot string) (string, error) {
	var logRow models.AuditLog
	if err := s.db.WithContext(ctx).Where("log_id = ? AND client_id = ?", logID, clientID).First(&logRow).Error; err != nil {
		return "", errors.New("target_log_not_found")
	}
	anchorID := strings.TrimSpace(requestedAnchorID)
	if anchorID == "" && logRow.BlockchainTxID != nil {
		anchorID = strings.TrimSpace(*logRow.BlockchainTxID)
	}
	if anchorID == "" {
		return "", errors.New("anchor_missing")
	}
	var proofs []models.MerkleProof
	if err := s.db.WithContext(ctx).Where("transaction_hash = ?", hash).Order("tree_level ASC").Find(&proofs).Error; err != nil {
		return "", err
	}
	proofData := make([]crypto.MerkleProofData, 0, len(proofs))
	for _, proof := range proofs {
		proofData = append(proofData, crypto.MerkleProofData{SiblingHash: proof.SiblingHash, IsLeft: proof.IsLeft, TreeLevel: proof.TreeLevel})
	}
	if s.fabric == nil {
		return "", errors.New("fabric_unavailable")
	}
	raw, err := s.fabric.GetAnchorFromLedger(anchorID)
	if err != nil {
		return "", fmt.Errorf("fabric_read_failed: %w", err)
	}
	var anchor struct {
		MerkleRoot string `json:"merkle_root"`
	}
	if err := json.Unmarshal([]byte(raw), &anchor); err != nil {
		return "", fmt.Errorf("fabric_anchor_invalid: %w", err)
	}
	if strings.TrimSpace(expectedRoot) != "" && anchor.MerkleRoot != expectedRoot {
		return "", errors.New("merkle_root_mismatch")
	}
	reconstructedRoot := crypto.ReconstructMerkleRoot(hash, proofData)
	if reconstructedRoot != anchor.MerkleRoot {
		return "", errors.New("merkle_proof_invalid")
	}
	return anchor.MerkleRoot, nil
}

func sanitizeFailure(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 1000 {
		message = message[:1000]
	}
	return message
}
