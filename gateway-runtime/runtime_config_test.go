package main

import (
	"testing"
	"time"
)

func TestValidateRecoveryScopeFlags(t *testing.T) {
	cutoff := time.Date(2026, 9, 18, 14, 34, 33, 0, time.UTC)
	tests := []struct {
		name             string
		recovery         bool
		snapshotRequired bool
		cutoff           *time.Time
		mode             string
		wantErr          bool
	}{
		{name: "disabled local", wantErr: false},
		{name: "legacy recovery requires cutoff", recovery: true, snapshotRequired: true, mode: "snapshot_legacy", wantErr: true},
		{name: "legacy recovery requires gate", recovery: true, cutoff: &cutoff, mode: "snapshot_legacy", wantErr: true},
		{name: "legacy gate requires cutoff", snapshotRequired: true, mode: "snapshot_legacy", wantErr: true},
		{name: "legacy production scope valid", recovery: true, snapshotRequired: true, cutoff: &cutoff, mode: "snapshot_legacy", wantErr: false},
		{name: "direct recovery does not require legacy cutoff", recovery: true, snapshotRequired: false, mode: "agent_direct", wantErr: false},
		{name: "default recovery mode is direct", recovery: true, snapshotRequired: false, wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateRecoveryScopeFlags(tt.recovery, tt.snapshotRequired, tt.cutoff, tt.mode); (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateRecoveryScopeFlagsRejectsUnknownMode(t *testing.T) {
	if err := validateRecoveryScopeFlags(false, false, nil, "unknown"); err == nil {
		t.Fatal("unknown recovery mode accepted")
	}
}

func TestDirectRecoveryIgnoresLegacySnapshotWriterFlag(t *testing.T) {
	t.Setenv("SNAPSHOT_WRITER_ENABLED", "true")
	builder, store, cipher, err := buildSnapshotRuntime("agent_direct")
	if err != nil {
		t.Fatalf("buildSnapshotRuntime() error = %v", err)
	}
	if builder != nil || store != nil || cipher != nil {
		t.Fatal("agent_direct initialized legacy snapshot runtime")
	}
}
