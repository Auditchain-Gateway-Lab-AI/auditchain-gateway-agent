package snapshotstore

import (
	"testing"
	"time"

	"go-blockchain-api/internal/models"
)

func TestRecoveryEventSnapshotRoundTrip(t *testing.T) {
	timestamp := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	event := models.RecoveryEvent{
		ID: "event-1", ClientID: "client-1", RequestID: "request-1", IncidentID: "incident-1",
		TargetLogID: "log-1", SelectedLogID: "log-1", EventType: models.RecoveryEventTypeExecution,
		ResultStatus: models.RecoveryResultSucceeded, Resource: "RUANGAN:1", TargetActor: "POLINEMA",
		TargetAction: "UPDATE", TargetTimestamp: &timestamp, TargetSourceSystem: "SIMRS Morbis 1",
		RecoveredMetadata: `{"id":1}`, ExecutorSystem: "AuditChain Gateway", ExecutedBy: "user-1",
		Reason: "tamper recovery", BeforeHash: "before", AfterHash: "after", EventHash: "event-hash",
		ExecutedAt: timestamp,
	}
	payload, err := NewRecoveryEventSnapshot(event, timestamp)
	if err != nil {
		t.Fatalf("NewRecoveryEventSnapshot() error = %v", err)
	}
	decoded, err := ParseRecoveryEventSnapshot(payload)
	if err != nil {
		t.Fatalf("ParseRecoveryEventSnapshot() error = %v", err)
	}
	if decoded.EventID != event.ID || decoded.ClientID != event.ClientID || decoded.EventHash != event.EventHash {
		t.Fatalf("snapshot identity changed: %#v", decoded)
	}
	if string(decoded.RecoveredMetadata) != event.RecoveredMetadata {
		t.Fatalf("metadata = %s, want %s", decoded.RecoveredMetadata, event.RecoveredMetadata)
	}
}

func TestParseRecoveryEventSnapshotRejectsWrongType(t *testing.T) {
	if _, err := ParseRecoveryEventSnapshot([]byte(`{"schema_version":1,"snapshot_type":"AUDIT_LOG"}`)); err == nil {
		t.Fatal("expected wrong snapshot type to be rejected")
	}
}
