package snapshotstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"go-blockchain-api/internal/models"
)

const SnapshotSchemaVersion = 1

// AuditSnapshot contains the fields required to reconstruct and verify an
// AuditLog. Metadata is kept as canonical JSON bytes so recovery does not
// accidentally change the hash input.
type AuditSnapshot struct {
	SchemaVersion        int             `json:"schema_version"`
	SnapshotType         string          `json:"snapshot_type"`
	LogID                string          `json:"log_id"`
	ClientID             string          `json:"client_id"`
	Actor                string          `json:"actor"`
	Action               string          `json:"action"`
	Resource             string          `json:"resource"`
	Timestamp            time.Time       `json:"timestamp"`
	DBTimestamp          *time.Time      `json:"db_timestamp,omitempty"`
	SourceSystem         string          `json:"source_system"`
	AuthorizationContext string          `json:"authorization_context"`
	SourceRecordID       string          `json:"source_record_id,omitempty"`
	Metadata             json.RawMessage `json:"metadata"`
	HashAlgorithm        string          `json:"hash_algorithm"`
	HashValue            string          `json:"hash_value"`
	CapturedAt           time.Time       `json:"captured_at"`
}

// RecoveryEventSnapshot contains the immutable recovery execution evidence.
// Pipeline state is intentionally not included; it is stored in PostgreSQL
// and can advance from HASHED to ANCHORED without changing event_hash.
type RecoveryEventSnapshot struct {
	SchemaVersion            int             `json:"schema_version"`
	SnapshotType             string          `json:"snapshot_type"`
	EventID                  string          `json:"event_id"`
	ClientID                 string          `json:"client_id"`
	RequestID                string          `json:"request_id"`
	IncidentID               string          `json:"incident_id"`
	TargetLogID              string          `json:"target_log_id"`
	SelectedLogID            string          `json:"selected_log_id"`
	EventType                string          `json:"event_type"`
	ResultStatus             string          `json:"result_status"`
	Resource                 string          `json:"resource"`
	TargetActor              string          `json:"target_actor"`
	TargetAction             string          `json:"target_action"`
	TargetTimestamp          *time.Time      `json:"target_timestamp,omitempty"`
	SourceSystem             string          `json:"source_system,omitempty"`
	TargetSourceSystem       string          `json:"target_source_system"`
	TargetAuthorization      string          `json:"target_authorization_context"`
	TargetSourceRecordID     string          `json:"target_source_record_id,omitempty"`
	RecoveredMetadata        json.RawMessage `json:"recovered_metadata"`
	ExecutorSystem           string          `json:"executor_system"`
	ExecutedBy               string          `json:"executed_by"`
	Reason                   string          `json:"reason"`
	BeforeHash               string          `json:"before_hash"`
	AfterHash                string          `json:"after_hash"`
	SourceSnapshotObjectKey  string          `json:"source_snapshot_object_key"`
	SourceSnapshotVersionID  string          `json:"source_snapshot_version_id"`
	SourceSnapshotChecksum   string          `json:"source_snapshot_checksum"`
	SourceSnapshotPlainHash  string          `json:"source_snapshot_plaintext_hash"`
	SourceAnchorID           string          `json:"source_anchor_id"`
	SourceExpectedMerkleRoot string          `json:"source_expected_merkle_root"`
	FailureCode              string          `json:"failure_code,omitempty"`
	FailureReason            string          `json:"failure_reason,omitempty"`
	EventHash                string          `json:"event_hash"`
	ExecutedAt               time.Time       `json:"executed_at"`
	CapturedAt               time.Time       `json:"captured_at"`
}

func NewAuditSnapshot(log models.AuditLog, capturedAt time.Time) ([]byte, error) {
	metadata := []byte(log.Metadata)
	if len(bytes.TrimSpace(metadata)) == 0 {
		metadata = []byte("null")
	}
	if !json.Valid(metadata) {
		return nil, fmt.Errorf("metadata log %s bukan JSON valid", log.LogID)
	}

	snapshot := AuditSnapshot{
		SchemaVersion:        SnapshotSchemaVersion,
		SnapshotType:         "AUDIT_LOG",
		LogID:                log.LogID,
		ClientID:             log.ClientID,
		Actor:                log.Actor,
		Action:               log.Action,
		Resource:             log.Resource,
		Timestamp:            log.Timestamp,
		DBTimestamp:          log.DBTimestamp,
		SourceSystem:         log.SourceSystem,
		AuthorizationContext: log.AuthorizationContext,
		SourceRecordID:       log.SourceRecordID,
		Metadata:             json.RawMessage(metadata),
		HashAlgorithm:        "SHA3-256",
		HashValue:            log.HashValue,
		CapturedAt:           capturedAt.UTC(),
	}
	return json.Marshal(snapshot)
}

func ParseAuditSnapshot(payload []byte) (AuditSnapshot, error) {
	var snapshot AuditSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return AuditSnapshot{}, fmt.Errorf("snapshot audit tidak valid: %w", err)
	}
	if snapshot.SchemaVersion != SnapshotSchemaVersion {
		return AuditSnapshot{}, fmt.Errorf("schema snapshot tidak didukung: %d", snapshot.SchemaVersion)
	}
	if snapshot.SnapshotType != "AUDIT_LOG" {
		return AuditSnapshot{}, fmt.Errorf("jenis snapshot tidak didukung: %s", snapshot.SnapshotType)
	}
	if snapshot.LogID == "" || snapshot.ClientID == "" || snapshot.HashValue == "" {
		return AuditSnapshot{}, fmt.Errorf("snapshot audit tidak memiliki identitas/hash wajib")
	}
	if !json.Valid(snapshot.Metadata) {
		return AuditSnapshot{}, fmt.Errorf("metadata pada snapshot audit bukan JSON valid")
	}
	return snapshot, nil
}

func NewRecoveryEventSnapshot(event models.RecoveryEvent, capturedAt time.Time) ([]byte, error) {
	metadata := []byte(event.RecoveredMetadata)
	if len(bytes.TrimSpace(metadata)) == 0 {
		metadata = []byte("null")
	}
	if !json.Valid(metadata) {
		return nil, fmt.Errorf("metadata recovery event %s bukan JSON valid", event.ID)
	}
	snapshot := RecoveryEventSnapshot{
		SchemaVersion:            SnapshotSchemaVersion,
		SnapshotType:             "RECOVERY_EVENT",
		EventID:                  event.ID,
		ClientID:                 event.ClientID,
		RequestID:                event.RequestID,
		IncidentID:               event.IncidentID,
		TargetLogID:              event.TargetLogID,
		SelectedLogID:            event.SelectedLogID,
		EventType:                event.EventType,
		ResultStatus:             event.ResultStatus,
		Resource:                 event.Resource,
		TargetActor:              event.TargetActor,
		TargetAction:             event.TargetAction,
		TargetTimestamp:          event.TargetTimestamp,
		SourceSystem:             event.SourceSystem,
		TargetSourceSystem:       event.TargetSourceSystem,
		TargetAuthorization:      event.TargetAuthorization,
		TargetSourceRecordID:     event.TargetSourceRecordID,
		RecoveredMetadata:        json.RawMessage(metadata),
		ExecutorSystem:           event.ExecutorSystem,
		ExecutedBy:               event.ExecutedBy,
		Reason:                   event.Reason,
		BeforeHash:               event.BeforeHash,
		AfterHash:                event.AfterHash,
		SourceSnapshotObjectKey:  event.SourceSnapshotObjectKey,
		SourceSnapshotVersionID:  event.SourceSnapshotVersionID,
		SourceSnapshotChecksum:   event.SourceSnapshotChecksum,
		SourceSnapshotPlainHash:  event.SourceSnapshotPlainHash,
		SourceAnchorID:           event.SourceAnchorID,
		SourceExpectedMerkleRoot: event.SourceExpectedMerkleRoot,
		FailureCode:              event.FailureCode,
		FailureReason:            event.FailureReason,
		EventHash:                event.EventHash,
		ExecutedAt:               event.ExecutedAt,
		CapturedAt:               capturedAt.UTC(),
	}
	return json.Marshal(snapshot)
}

func ParseRecoveryEventSnapshot(payload []byte) (RecoveryEventSnapshot, error) {
	var snapshot RecoveryEventSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return RecoveryEventSnapshot{}, fmt.Errorf("snapshot recovery event tidak valid: %w", err)
	}
	if snapshot.SchemaVersion != SnapshotSchemaVersion || snapshot.SnapshotType != "RECOVERY_EVENT" {
		return RecoveryEventSnapshot{}, fmt.Errorf("jenis/schema snapshot recovery event tidak didukung")
	}
	if snapshot.EventID == "" || snapshot.ClientID == "" || snapshot.RequestID == "" || snapshot.EventHash == "" {
		return RecoveryEventSnapshot{}, fmt.Errorf("snapshot recovery event tidak memiliki identitas/hash wajib")
	}
	if !json.Valid(snapshot.RecoveredMetadata) {
		return RecoveryEventSnapshot{}, fmt.Errorf("metadata snapshot recovery event bukan JSON valid")
	}
	return snapshot, nil
}
