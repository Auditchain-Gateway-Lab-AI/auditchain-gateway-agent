package hasher

import (
	"testing"
	"time"

	"go-blockchain-api/internal/models"
)

func TestGenerateRecoveryEventHashExcludesPipelineState(t *testing.T) {
	timestamp := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	event := models.RecoveryEvent{
		ID: "event-1", ClientID: "client-1", RequestID: "request-1", IncidentID: "incident-1",
		TargetLogID: "log-1", SelectedLogID: "log-1", EventType: models.RecoveryEventTypeExecution,
		ResultStatus: models.RecoveryResultSucceeded, Resource: "RUANGAN:1", TargetActor: "POLINEMA",
		TargetAction: "UPDATE", TargetTimestamp: &timestamp, TargetSourceSystem: "SIMRS Morbis 1",
		RecoveredMetadata: `{"id":1,"nama":"ruang"}`, ExecutorSystem: "AuditChain Gateway",
		ExecutedBy: "user-1", Reason: "tamper recovery", BeforeHash: "before", AfterHash: "after",
		SourceSnapshotObjectKey: "production/client/recovery-events/event.snapshot", SourceSnapshotVersionID: "version-1",
		SourceSnapshotChecksum: "checksum", SourceSnapshotPlainHash: "plain-hash", SourceAnchorID: "anchor-1",
		SourceExpectedMerkleRoot: "root-1", ExecutedAt: timestamp,
	}
	first := GenerateRecoveryEventHash(&event)
	event.PipelineStatus = models.RecoveryPipelineAnchored
	event.MerkleRoot = "new-root"
	txID := "tx-1"
	event.BlockchainTxID = &txID
	second := GenerateRecoveryEventHash(&event)
	if first == "" || first != second {
		t.Fatalf("pipeline state changed immutable event hash: first=%q second=%q", first, second)
	}
	event.Reason = "different reason"
	if GenerateRecoveryEventHash(&event) == first {
		t.Fatal("immutable recovery event field did not change event hash")
	}
	event.Reason = "tamper recovery"
	event.SourceSystem = "other-client"
	if GenerateRecoveryEventHash(&event) == first {
		t.Fatal("source_system change did not change event hash")
	}
}
