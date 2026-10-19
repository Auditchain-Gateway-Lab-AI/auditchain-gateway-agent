package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadRecoveryCutoff(t *testing.T) {
	t.Setenv("RECOVERY_CUTOFF_AT", "2026-09-18T14:34:33+00:00")
	cutoff, err := LoadRecoveryCutoff()
	if err != nil {
		t.Fatalf("LoadRecoveryCutoff() unexpected error: %v", err)
	}
	if cutoff == nil || cutoff.Format(time.RFC3339) != "2026-09-18T14:34:33Z" {
		t.Fatalf("cutoff = %v, want UTC 2026-09-18T14:34:33Z", cutoff)
	}
}

func TestLoadRecoveryCutoffRejectsInvalidValue(t *testing.T) {
	t.Setenv("RECOVERY_CUTOFF_AT", "not-a-timestamp")
	if _, err := LoadRecoveryCutoff(); err == nil {
		t.Fatal("expected invalid RECOVERY_CUTOFF_AT to be rejected")
	}
}

func TestLoadRecoveryCutoffAllowsUnsetValue(t *testing.T) {
	_ = os.Unsetenv("RECOVERY_CUTOFF_AT")
	cutoff, err := LoadRecoveryCutoff()
	if err != nil {
		t.Fatalf("LoadRecoveryCutoff() unexpected error: %v", err)
	}
	if cutoff != nil {
		t.Fatalf("cutoff = %v, want nil", cutoff)
	}
}

func TestInRecoveryScope(t *testing.T) {
	cutoff := time.Date(2026, 9, 18, 14, 34, 33, 0, time.UTC)
	before := cutoff.Add(-time.Second)
	after := cutoff.Add(time.Second)

	if InRecoveryScope(&before, &cutoff) {
		t.Fatal("row before cutoff must be legacy")
	}
	if !InRecoveryScope(&cutoff, &cutoff) {
		t.Fatal("row at cutoff must be in recovery scope")
	}
	if !InRecoveryScope(&after, &cutoff) {
		t.Fatal("row after cutoff must be in recovery scope")
	}
	if InRecoveryScope(nil, &cutoff) {
		t.Fatal("row without creation time must not be in scoped recovery")
	}
}
