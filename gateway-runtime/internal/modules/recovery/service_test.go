package recovery

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"go-blockchain-api/internal/models"
	"go-blockchain-api/internal/storage/snapshotstore"
)

func TestValidateSnapshotIdentityRejectsCrossLogAndHashMismatch(t *testing.T) {
	request := &models.RecoveryRequest{
		ClientID:              "client-1",
		TargetLogID:           "log-1",
		SelectedLogID:         "log-1",
		SnapshotPlaintextHash: "hash-1",
	}
	snapshot := snapshotstore.AuditSnapshot{
		LogID:     "log-1",
		ClientID:  "client-1",
		HashValue: "hash-1",
	}
	if err := validateSnapshotIdentity(snapshot, request); err != nil {
		t.Fatalf("valid snapshot identity rejected: %v", err)
	}

	snapshot.LogID = "log-2"
	if err := validateSnapshotIdentity(snapshot, request); err == nil || err.Error() != "cross_log_recovery_not_allowed" {
		t.Fatalf("cross-log error = %v, want cross_log_recovery_not_allowed", err)
	}

	snapshot.LogID = "log-1"
	snapshot.HashValue = "hash-2"
	if err := validateSnapshotIdentity(snapshot, request); err == nil || err.Error() != "snapshot_plaintext_hash_mismatch" {
		t.Fatalf("hash mismatch error = %v, want snapshot_plaintext_hash_mismatch", err)
	}
}

func TestDecryptTamperedMetadataRestoresOriginalPayload(t *testing.T) {
	cipher, err := snapshotstore.NewCipher("test-key", bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatalf("NewCipher() error = %v", err)
	}
	plaintext, err := json.Marshal(map[string]string{
		"metadata": `{"room":181,"owner":"tampered-before-recovery"}`,
	})
	if err != nil {
		t.Fatalf("marshal tampered payload: %v", err)
	}
	encrypted, err := cipher.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	metadata, err := decryptTamperedMetadata(cipher, encrypted)
	if err != nil {
		t.Fatalf("decryptTamperedMetadata() error = %v", err)
	}
	values, ok := metadata.(map[string]interface{})
	if !ok {
		t.Fatalf("metadata type = %T, want map[string]interface{}", metadata)
	}
	if values["owner"] != "tampered-before-recovery" {
		t.Fatalf("owner = %v, want original tampered value", values["owner"])
	}
}

func TestServiceRecoveryCutoffKeepsLegacyOutOfScope(t *testing.T) {
	cutoff := time.Date(2026, 9, 18, 14, 34, 33, 0, time.UTC)
	service := NewService(nil, nil, nil, nil)
	service.SetRecoveryCutoff(&cutoff)

	legacyTime := cutoff.Add(-time.Second)
	legacy := &models.AuditLog{DBTimestamp: &legacyTime}
	if service.inRecoveryScope(legacy) {
		t.Fatal("legacy audit log unexpectedly belongs to recovery scope")
	}

	newTime := cutoff.Add(time.Second)
	newLog := &models.AuditLog{DBTimestamp: &newTime}
	if !service.inRecoveryScope(newLog) {
		t.Fatal("new audit log unexpectedly excluded from recovery scope")
	}
}

func TestRecoveryEventTrustedLineageAllowsRepeatRecovery(t *testing.T) {
	event := &models.RecoveryEvent{
		ResultStatus:             models.RecoveryResultSucceeded,
		SourceSnapshotObjectKey:  "production/log-1",
		SourceSnapshotVersionID:  "version-1",
		SourceSnapshotChecksum:   "checksum-1",
		SourceSnapshotPlainHash:  "hash-1",
		SourceAnchorID:           "anchor-1",
		SourceExpectedMerkleRoot: "root-1",
	}
	if !recoveryEventHasTrustedLineage(event) {
		t.Fatal("successful recovery event with complete source references must establish trusted lineage")
	}

	cases := []struct {
		name   string
		mutate func(*models.RecoveryEvent)
		want   bool
	}{
		{
			name: "failed event does not establish lineage",
			mutate: func(value *models.RecoveryEvent) {
				value.ResultStatus = models.RecoveryResultExecution
			},
			want: false,
		},
		{
			name: "missing snapshot version does not establish lineage",
			mutate: func(value *models.RecoveryEvent) {
				value.SourceSnapshotVersionID = ""
			},
			want: false,
		},
		{
			name: "missing anchor does not establish lineage",
			mutate: func(value *models.RecoveryEvent) {
				value.SourceAnchorID = ""
			},
			want: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := *event
			testCase.mutate(&candidate)
			if got := recoveryEventHasTrustedLineage(&candidate); got != testCase.want {
				t.Fatalf("trusted lineage = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestClientExecutableStatus(t *testing.T) {
	for _, status := range []string{
		models.RecoveryStatusPendingExecution,
		models.RecoveryStatusPendingApproval,
		models.RecoveryStatusApproved,
	} {
		if !isClientExecutableStatus(status) {
			t.Fatalf("status %q should be executable by an authenticated client user", status)
		}
	}

	for _, status := range []string{
		models.RecoveryStatusRejected,
		models.RecoveryStatusExecuting,
		models.RecoveryStatusSucceeded,
		models.RecoveryStatusFailedVerification,
	} {
		if isClientExecutableStatus(status) {
			t.Fatalf("terminal/in-flight status %q must not be executable", status)
		}
	}
}
