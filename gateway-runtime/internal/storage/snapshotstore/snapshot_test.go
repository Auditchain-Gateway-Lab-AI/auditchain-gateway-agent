package snapshotstore

import (
	"testing"
	"time"

	"go-blockchain-api/internal/models"
)

func TestAuditSnapshotRoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	log := models.AuditLog{
		LogID:                "L2",
		ClientID:             "client-1",
		Actor:                "Indah",
		Action:               "UPDATE",
		Resource:             "Data_RM:123",
		Timestamp:            now,
		SourceSystem:         "hospital-system",
		AuthorizationContext: "",
		SourceRecordID:       "123",
		Metadata:             `{"diagnosis":"sakit demam"}`,
		HashValue:            "abc123",
	}

	payload, err := NewAuditSnapshot(log, now.Add(time.Second))
	if err != nil {
		t.Fatalf("NewAuditSnapshot() error = %v", err)
	}
	snapshot, err := ParseAuditSnapshot(payload)
	if err != nil {
		t.Fatalf("ParseAuditSnapshot() error = %v", err)
	}
	if snapshot.LogID != log.LogID || snapshot.ClientID != log.ClientID {
		t.Fatalf("identitas snapshot tidak sesuai: %+v", snapshot)
	}
	if string(snapshot.Metadata) != log.Metadata {
		t.Fatalf("metadata snapshot berubah: %s", snapshot.Metadata)
	}
	if snapshot.HashValue != log.HashValue {
		t.Fatalf("hash snapshot berubah: %s", snapshot.HashValue)
	}
}

func TestNewAuditSnapshotRejectsInvalidMetadata(t *testing.T) {
	log := models.AuditLog{LogID: "L-invalid", ClientID: "client-1", Metadata: "not-json"}
	if _, err := NewAuditSnapshot(log, time.Now()); err == nil {
		t.Fatal("NewAuditSnapshot() menerima metadata yang bukan JSON")
	}
}
