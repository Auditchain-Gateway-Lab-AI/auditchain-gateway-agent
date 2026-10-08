package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseRuntimeCommand(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "defaults to serve", want: runtimeCommandServe},
		{name: "explicit serve", args: []string{"serve"}, want: runtimeCommandServe},
		{name: "explicit migrate", args: []string{"migrate"}, want: runtimeCommandMigrate},
		{name: "rejects unknown command", args: []string{"setup"}, wantErr: true},
		{name: "rejects extra arguments", args: []string{"migrate", "production"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRuntimeCommand(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseRuntimeCommand() error = %v, wantErr=%v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("parseRuntimeCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunDatabaseMigrationRequiresDedicatedDSN(t *testing.T) {
	t.Setenv("DB_MIGRATION_DSN", "")
	t.Setenv("DB_DSN", "postgres://must-not-be-used.invalid/db")
	err := runDatabaseMigration()
	if err == nil || !strings.Contains(err.Error(), "database DSN is required") {
		t.Fatalf("runDatabaseMigration() error = %v, want missing dedicated DSN error", err)
	}
}

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

func TestLoadBackgroundWorkersConfigDefaultsToDisabled(t *testing.T) {
	t.Setenv("GATEWAY_BACKGROUND_WORKERS_ENABLED", "")
	t.Setenv("KAFKA_CONSUMER_START_OFFSET", "")
	enabled, offset, err := loadBackgroundWorkersConfig()
	if err != nil {
		t.Fatalf("loadBackgroundWorkersConfig() error = %v", err)
	}
	if enabled || offset != "" {
		t.Fatalf("got enabled=%v offset=%q, want disabled with no offset", enabled, offset)
	}
}

func TestLoadBackgroundWorkersConfigRequiresExplicitKafkaOffset(t *testing.T) {
	t.Setenv("GATEWAY_BACKGROUND_WORKERS_ENABLED", "true")
	t.Setenv("KAFKA_CONSUMER_START_OFFSET", "")
	if _, _, err := loadBackgroundWorkersConfig(); err == nil {
		t.Fatal("enabled background workers accepted an unspecified Kafka start offset")
	}
}

func TestLoadBackgroundWorkersConfigRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"sometimes", "1", "yes"} {
		t.Setenv("GATEWAY_BACKGROUND_WORKERS_ENABLED", value)
		if _, _, err := loadBackgroundWorkersConfig(); err == nil {
			t.Fatalf("invalid worker enable flag %q was accepted", value)
		}
	}

	t.Setenv("GATEWAY_BACKGROUND_WORKERS_ENABLED", "true")
	t.Setenv("KAFKA_CONSUMER_START_OFFSET", "0")
	if _, _, err := loadBackgroundWorkersConfig(); err == nil {
		t.Fatal("numeric Kafka offset was accepted instead of explicit earliest/latest")
	}
}

func TestLoadBackgroundWorkersConfigAcceptsExplicitOffset(t *testing.T) {
	t.Setenv("GATEWAY_BACKGROUND_WORKERS_ENABLED", "true")
	for _, offset := range []string{"earliest", "latest"} {
		t.Run(offset, func(t *testing.T) {
			t.Setenv("KAFKA_CONSUMER_START_OFFSET", offset)
			enabled, gotOffset, err := loadBackgroundWorkersConfig()
			if err != nil {
				t.Fatalf("loadBackgroundWorkersConfig() error = %v", err)
			}
			if !enabled || gotOffset != offset {
				t.Fatalf("got enabled=%v offset=%q, want enabled and %q", enabled, gotOffset, offset)
			}
		})
	}
}
