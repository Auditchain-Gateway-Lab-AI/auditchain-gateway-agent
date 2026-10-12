package models

import "time"

const (
	SnapshotStatusPending       = "PENDING"
	SnapshotStatusUploading     = "UPLOADING"
	SnapshotStatusRetry         = "RETRY"
	SnapshotStatusVerified      = "VERIFIED"
	SnapshotStatusFailed        = "FAILED"
	SnapshotStatusLegacyMissing = "LEGACY_MISSING"

	OutboxStatusPending    = "PENDING"
	OutboxStatusProcessing = "PROCESSING"
	OutboxStatusRetry      = "RETRY"
	OutboxStatusCompleted  = "COMPLETED"
	OutboxStatusDeadLetter = "DEAD_LETTER"

	IncidentStatusOpen     = "OPEN"
	IncidentStatusResolved = "RESOLVED"

	// Recovery is client-operated. A request is ready to be executed by the
	// authenticated user that belongs to the owning client; no platform-admin
	// approval is required. The approval-related values remain for backwards
	// compatibility with requests created by older deployments.
	RecoveryStatusPendingExecution   = "PENDING_EXECUTION"
	RecoveryStatusPendingApproval    = "PENDING_APPROVAL"
	RecoveryStatusApproved           = "APPROVED"
	RecoveryStatusRejected           = "REJECTED"
	RecoveryStatusExecuting          = "EXECUTING"
	RecoveryStatusSucceeded          = "SUCCEEDED"
	RecoveryStatusFailedVerification = "FAILED_VERIFICATION"
	RecoveryStatusFailedExecution    = "FAILED_EXECUTION"
	RecoveryStatusAppliedAwaitingCDC = "APPLIED_AWAITING_CDC"
	RecoveryStatusAppliedCDCTimeout  = "APPLIED_CDC_TIMEOUT"

	RecoveryScopeGatewayIntegrity = "GATEWAY_INTEGRITY"
	RecoveryScopeClientSource     = "CLIENT_SOURCE"

	SourceStatusMatched           = "MATCHED"
	SourceStatusMismatch          = "MISMATCH"
	SourceStatusMissing           = "MISSING"
	SourceStatusUnexpectedPresent = "UNEXPECTED_PRESENT"
	SourceStatusUnreachable       = "UNREACHABLE"
	SourceStatusNotComparable     = "NOT_COMPARABLE"

	RecoveryOperationUpsert = "UPSERT"
	RecoveryOperationDelete = "DELETE"
	RecoveryOperationNoop   = "NOOP"

	CDCStatusPending     = "PENDING"
	CDCStatusConfirmed   = "CONFIRMED"
	CDCStatusTimeout     = "TIMEOUT"
	CDCStatusConflict    = "CONFLICT"
	CDCStatusNotRequired = "NOT_REQUIRED"

	RecoveryEventTypeExecution = "RECOVERY_EXECUTION"
	RecoveryResultSucceeded    = "SUCCEEDED"
	RecoveryResultAwaitingCDC  = "APPLIED_AWAITING_CDC"
	RecoveryResultCDCTimeout   = "APPLIED_CDC_TIMEOUT"
	RecoveryResultVerification = "FAILED_VERIFICATION"
	RecoveryResultExecution    = "FAILED_EXECUTION"
	RecoveryPipelineHashed     = "HASHED"
	RecoveryPipelineAggregated = "AGGREGATED"
	RecoveryPipelineAnchored   = "ANCHORED"
)

type SnapshotOutbox struct {
	ID       string `gorm:"primaryKey;type:varchar(36)" json:"id"`
	LogID    string `gorm:"type:varchar(100);not null;uniqueIndex" json:"log_id"`
	ClientID string `gorm:"type:varchar(36);not null;index" json:"client_id"`
	// Default keeps AutoMigrate safe for existing outbox rows created before
	// event types were introduced. Empty/legacy rows are treated as audit
	// snapshots by the worker during a rolling deployment.
	EventType     string     `gorm:"type:varchar(50);not null;default:'STORE_AUDIT_SNAPSHOT'" json:"event_type"`
	Payload       []byte     `gorm:"type:bytea;not null" json:"-"`
	PayloadHash   string     `gorm:"type:varchar(64);not null" json:"payload_hash"`
	Status        string     `gorm:"type:varchar(20);not null;index;default:'PENDING'" json:"status"`
	AttemptCount  int        `gorm:"not null;default:0" json:"attempt_count"`
	NextAttemptAt *time.Time `gorm:"index" json:"next_attempt_at,omitempty"`
	LockedAt      *time.Time `json:"locked_at,omitempty"`
	LockedBy      string     `gorm:"type:varchar(100)" json:"locked_by,omitempty"`
	LastError     string     `gorm:"type:text" json:"last_error,omitempty"`
	CreatedAt     time.Time  `gorm:"autoCreateTime" json:"created_at"`
	ProcessedAt   *time.Time `json:"processed_at,omitempty"`
}

type TamperIncident struct {
	ID                 string     `gorm:"primaryKey;type:varchar(36)" json:"id"`
	ClientID           string     `gorm:"type:varchar(36);not null;index" json:"client_id"`
	LogID              string     `gorm:"type:varchar(100);not null;index" json:"log_id"`
	Resource           string     `gorm:"type:varchar(255);not null;index" json:"resource"`
	IncidentType       string     `gorm:"type:varchar(80);not null;index" json:"incident_type"`
	ExpectedHash       string     `gorm:"type:varchar(64)" json:"expected_hash"`
	DetectedHash       string     `gorm:"type:varchar(64)" json:"detected_hash"`
	TamperedPayload    []byte     `gorm:"type:bytea" json:"-"`
	Status             string     `gorm:"type:varchar(30);not null;index;default:'OPEN'" json:"status"`
	IncidentScope      string     `gorm:"type:varchar(30);not null;index;default:'GATEWAY_INTEGRITY'" json:"incident_scope"`
	SourceStatus       string     `gorm:"type:varchar(30);index" json:"source_status,omitempty"`
	ReferenceLogID     string     `gorm:"type:varchar(100);index" json:"reference_log_id,omitempty"`
	DetectedStateHash  string     `gorm:"type:varchar(64)" json:"detected_state_hash,omitempty"`
	DiscrepancySummary string     `gorm:"type:jsonb" json:"discrepancy_summary,omitempty"`
	LastConfirmedAt    *time.Time `gorm:"index" json:"last_confirmed_at,omitempty"`
	DetectedAt         time.Time  `gorm:"autoCreateTime;index" json:"detected_at"`
	ResolvedAt         *time.Time `json:"resolved_at,omitempty"`
}

type RecoveryRequest struct {
	ID                string `gorm:"primaryKey;type:varchar(36)" json:"id"`
	ClientID          string `gorm:"type:varchar(36);not null;index;uniqueIndex:idx_recovery_idempotency" json:"client_id"`
	IncidentID        string `gorm:"type:varchar(36);not null;index" json:"incident_id"`
	TargetLogID       string `gorm:"type:varchar(100);not null" json:"target_log_id"`
	SelectedLogID     string `gorm:"type:varchar(100);not null" json:"selected_log_id"`
	SnapshotObjectKey string `gorm:"type:text" json:"snapshot_object_key,omitempty"`
	SnapshotVersionID string `gorm:"type:varchar(255)" json:"snapshot_version_id,omitempty"`
	// These frozen references are nullable at the database level so AutoMigrate
	// can be applied safely to installations that already contain legacy
	// recovery requests. New requests are rejected unless all values are set.
	SnapshotChecksum      string `gorm:"type:varchar(64)" json:"snapshot_checksum"`
	SnapshotPlaintextHash string `gorm:"type:varchar(64)" json:"snapshot_plaintext_hash"`
	AnchorID              string `gorm:"type:varchar(100)" json:"anchor_id"`
	ExpectedMerkleRoot    string `gorm:"type:varchar(64)" json:"expected_merkle_root"`
	Resource              string `gorm:"type:varchar(255);index" json:"resource"`
	Operation             string `gorm:"type:varchar(20)" json:"operation,omitempty"`
	ReferenceLogHash      string `gorm:"type:varchar(64)" json:"reference_log_hash,omitempty"`
	ReferenceMerkleRoot   string `gorm:"type:varchar(64)" json:"reference_merkle_root,omitempty"`
	ReferenceAnchorID     string `gorm:"type:varchar(100)" json:"reference_anchor_id,omitempty"`
	DesiredStateHash      string `gorm:"type:varchar(64)" json:"desired_state_hash,omitempty"`
	// DesiredState is the exact state sent to the Agent. It is intentionally
	// not serialized in API responses; previews/evidence use the redaction
	// policy instead of returning credentials or bearer material.
	DesiredState       string     `gorm:"type:jsonb" json:"-"`
	ClientBeforeHash   string     `gorm:"type:varchar(64)" json:"client_before_hash,omitempty"`
	AgentCommandID     string     `gorm:"type:varchar(100);index" json:"agent_command_id,omitempty"`
	AgentAppliedAt     *time.Time `json:"agent_applied_at,omitempty"`
	CDCStatus          string     `gorm:"type:varchar(20);index;default:'PENDING'" json:"cdc_status"`
	CDCDeadlineAt      *time.Time `gorm:"index" json:"cdc_deadline_at,omitempty"`
	ResultAuditLogID   string     `gorm:"type:varchar(100);index" json:"result_audit_log_id,omitempty"`
	RequestedBy        string     `gorm:"type:varchar(36);not null" json:"requested_by"`
	ApprovedBy         string     `gorm:"type:varchar(36)" json:"approved_by,omitempty"`
	ExecutedBy         string     `gorm:"type:varchar(36)" json:"executed_by,omitempty"`
	Reason             string     `gorm:"type:text;not null" json:"reason"`
	Status             string     `gorm:"type:varchar(35);not null;index;default:'PENDING_EXECUTION'" json:"status"`
	IdempotencyKey     string     `gorm:"type:varchar(100);not null;uniqueIndex:idx_recovery_idempotency" json:"idempotency_key"`
	BeforeHash         string     `gorm:"type:varchar(64)" json:"before_hash,omitempty"`
	AfterHash          string     `gorm:"type:varchar(64)" json:"after_hash,omitempty"`
	FailureReason      string     `gorm:"type:text" json:"failure_reason,omitempty"`
	RequestedAt        time.Time  `gorm:"autoCreateTime;index" json:"requested_at"`
	ApprovedAt         *time.Time `json:"approved_at,omitempty"`
	ExecutedAt         *time.Time `json:"executed_at,omitempty"`
	ExecutionStartedAt *time.Time `json:"execution_started_at,omitempty"`
	RecoveryEventID    string     `gorm:"type:varchar(36);uniqueIndex" json:"recovery_event_id,omitempty"`
}

// RecoveryEvent is the immutable, tenant-scoped evidence of one execution
// attempt. It is deliberately separate from AuditLog: AuditLog contains
// client-originated CDC events, while this model describes a Gateway recovery
// operation and its independently anchored evidence.
type RecoveryEvent struct {
	ID            string `gorm:"primaryKey;type:varchar(36)" json:"id"`
	ClientID      string `gorm:"type:varchar(36);not null;index" json:"client_id"`
	RequestID     string `gorm:"type:varchar(36);not null;uniqueIndex" json:"request_id"`
	IncidentID    string `gorm:"type:varchar(36);not null;index" json:"incident_id"`
	TargetLogID   string `gorm:"type:varchar(100);not null;index" json:"target_log_id"`
	SelectedLogID string `gorm:"type:varchar(100);not null" json:"selected_log_id"`
	EventType     string `gorm:"type:varchar(50);not null" json:"event_type"`
	ResultStatus  string `gorm:"type:varchar(35);not null;index" json:"result_status"`
	Operation     string `gorm:"type:varchar(20)" json:"operation,omitempty"`

	Resource             string     `gorm:"type:varchar(255);not null;index" json:"resource"`
	TargetActor          string     `gorm:"type:varchar(100)" json:"target_actor"`
	TargetAction         string     `gorm:"type:varchar(100)" json:"target_action"`
	TargetTimestamp      *time.Time `json:"target_timestamp,omitempty"`
	SourceSystem         string     `gorm:"column:source_system;type:varchar(100)" json:"source_system,omitempty"`
	TargetSourceSystem   string     `gorm:"type:varchar(100)" json:"target_source_system"`
	TargetAuthorization  string     `gorm:"type:text" json:"target_authorization_context"`
	TargetSourceRecordID string     `gorm:"type:varchar(100)" json:"target_source_record_id"`
	RecoveredMetadata    string     `gorm:"type:jsonb" json:"recovered_metadata"`
	ExecutorSystem       string     `gorm:"type:varchar(100);not null" json:"executor_system"`
	ExecutedBy           string     `gorm:"type:varchar(36);not null;index" json:"executed_by"`
	Reason               string     `gorm:"type:text;not null" json:"reason"`
	BeforeHash           string     `gorm:"type:varchar(64)" json:"before_hash"`
	AfterHash            string     `gorm:"type:varchar(64)" json:"after_hash"`
	ReferenceLogHash     string     `gorm:"type:varchar(64)" json:"reference_log_hash,omitempty"`
	ReferenceMerkleRoot  string     `gorm:"type:varchar(64)" json:"reference_merkle_root,omitempty"`
	ReferenceAnchorID    string     `gorm:"type:varchar(100)" json:"reference_anchor_id,omitempty"`
	ClientBeforeHash     string     `gorm:"type:varchar(64)" json:"client_before_hash,omitempty"`
	ClientAfterHash      string     `gorm:"type:varchar(64)" json:"client_after_hash,omitempty"`
	ReadbackStatus       string     `gorm:"type:varchar(30)" json:"readback_status,omitempty"`
	ResultAuditLogID     string     `gorm:"type:varchar(100);index" json:"result_audit_log_id,omitempty"`
	CDCStatus            string     `gorm:"type:varchar(20);index;default:'PENDING'" json:"cdc_status"`

	SourceSnapshotObjectKey  string `gorm:"type:text" json:"source_snapshot_object_key"`
	SourceSnapshotVersionID  string `gorm:"type:varchar(255)" json:"source_snapshot_version_id"`
	SourceSnapshotChecksum   string `gorm:"type:varchar(64)" json:"source_snapshot_checksum"`
	SourceSnapshotPlainHash  string `gorm:"type:varchar(64)" json:"source_snapshot_plaintext_hash"`
	SourceAnchorID           string `gorm:"type:varchar(100)" json:"source_anchor_id"`
	SourceExpectedMerkleRoot string `gorm:"type:varchar(64)" json:"source_expected_merkle_root"`

	FailureCode   string    `gorm:"type:varchar(100)" json:"failure_code,omitempty"`
	FailureReason string    `gorm:"type:text" json:"failure_reason,omitempty"`
	EventHash     string    `gorm:"type:varchar(64);uniqueIndex" json:"event_hash"`
	ExecutedAt    time.Time `gorm:"not null;index" json:"executed_at"`

	PipelineStatus      string     `gorm:"type:varchar(20);not null;index;default:'HASHED'" json:"pipeline_status"`
	MerkleRoot          string     `gorm:"type:varchar(64);index" json:"merkle_root"`
	BlockchainTxID      *string    `gorm:"type:varchar(100)" json:"blockchain_tx_id"`
	BlockchainTimestamp *time.Time `gorm:"index" json:"blockchain_timestamp,omitempty"`

	SnapshotStatus        string     `gorm:"type:varchar(30);index;default:'PENDING'" json:"snapshot_status"`
	SnapshotObjectKey     string     `gorm:"type:text" json:"snapshot_object_key"`
	SnapshotVersionID     string     `gorm:"type:varchar(255)" json:"snapshot_version_id"`
	SnapshotChecksum      string     `gorm:"type:varchar(64)" json:"snapshot_checksum"`
	SnapshotPlaintextHash string     `gorm:"type:varchar(64)" json:"snapshot_plaintext_hash"`
	SnapshotStoredAt      *time.Time `json:"snapshot_stored_at,omitempty"`
	SnapshotVerifiedAt    *time.Time `json:"snapshot_verified_at,omitempty"`
	SnapshotLastError     string     `gorm:"type:text" json:"snapshot_last_error,omitempty"`

	IntegrityStatus    string     `gorm:"type:varchar(20);index;default:'NOT_CHECKED'" json:"integrity_status"`
	IntegrityCheckedAt *time.Time `gorm:"index" json:"integrity_checked_at,omitempty"`
	IntegrityError     string     `gorm:"type:text" json:"integrity_error,omitempty"`
}
