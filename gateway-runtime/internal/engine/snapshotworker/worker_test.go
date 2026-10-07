package snapshotworker

import (
	"strings"
	"testing"
	"time"
)

func TestBuildObjectKeyDoesNotExposeRecordData(t *testing.T) {
	createdAt := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	key := BuildObjectKey("local", "client-1", createdAt, "L2", "abc123")
	want := "local/client-1/2026/09/17/L2/abc123.snapshot"
	if key != want {
		t.Fatalf("BuildObjectKey() = %q, want %q", key, want)
	}
	if strings.Contains(key, "sakit") {
		t.Fatal("object key tidak boleh memuat payload medis")
	}
}

func TestBuildObjectKeySanitizesPathSegments(t *testing.T) {
	createdAt := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	key := BuildObjectKey("prod/../", "client/123", createdAt, "L2?", "hash value")
	if key == "" || key == "prod/..//client/123/2026/09/17/L2?/hash value.snapshot" {
		t.Fatalf("BuildObjectKey() tidak melakukan sanitasi: %q", key)
	}
}

func TestBuildRecoveryEventObjectKeyUsesSeparateNamespace(t *testing.T) {
	createdAt := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	key := BuildRecoveryEventObjectKey("production", "client-1", createdAt, "event-1", "hash-1")
	want := "production/client-1/recovery-events/2026/09/21/event-1/hash-1.snapshot"
	if key != want {
		t.Fatalf("BuildRecoveryEventObjectKey() = %q, want %q", key, want)
	}
	if strings.Contains(key, "/audit-logs/") {
		t.Fatal("recovery event snapshot tidak boleh memakai namespace audit log")
	}
}

func TestRetryDelayCapsAtThirtyMinutes(t *testing.T) {
	if got := retryDelay(5*time.Second, 1); got != 5*time.Second {
		t.Fatalf("retryDelay attempt 1 = %s", got)
	}
	if got := retryDelay(5*time.Second, 4); got != 40*time.Second {
		t.Fatalf("retryDelay attempt 4 = %s", got)
	}
	if got := retryDelay(5*time.Second, 20); got != 30*time.Minute {
		t.Fatalf("retryDelay cap = %s", got)
	}
}
