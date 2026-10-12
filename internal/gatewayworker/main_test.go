package main

import (
	"strings"
	"testing"
)

func TestRunRequiresExplicitEnable(t *testing.T) {
	t.Setenv("GATEWAY_BACKGROUND_WORKERS_ENABLED", "false")
	if err := run(); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("run() error = %v; expected disabled gate", err)
	}
}

func TestRunRequiresCutoverConfirmation(t *testing.T) {
	t.Setenv("GATEWAY_BACKGROUND_WORKERS_ENABLED", "true")
	t.Setenv("GATEWAY_V2_CUTOVER_CONFIRMED", "false")
	if err := run(); err == nil || !strings.Contains(err.Error(), "GATEWAY_V2_CUTOVER_CONFIRMED") {
		t.Fatalf("run() error = %v; expected cutover gate", err)
	}
}

func TestRunRejectsMissingOffsetBeforeConnecting(t *testing.T) {
	t.Setenv("GATEWAY_BACKGROUND_WORKERS_ENABLED", "true")
	t.Setenv("GATEWAY_V2_CUTOVER_CONFIRMED", "true")
	t.Setenv("KAFKA_CONSUMER_START_OFFSET", "")
	if err := run(); err == nil || !strings.Contains(err.Error(), "KAFKA_CONSUMER_START_OFFSET") {
		t.Fatalf("run() error = %v; expected offset validation", err)
	}
}

func TestRunRejectsMissingCutoffBeforeConnecting(t *testing.T) {
	t.Setenv("GATEWAY_BACKGROUND_WORKERS_ENABLED", "true")
	t.Setenv("GATEWAY_V2_CUTOVER_CONFIRMED", "true")
	t.Setenv("KAFKA_CONSUMER_START_OFFSET", "earliest")
	t.Setenv("FABRIC_CHANNEL", "auditchannel")
	t.Setenv("FABRIC_CHAINCODE", "audit-contract")
	t.Setenv("GATEWAY_PIPELINE_CUTOFF_UTC", "")
	if err := run(); err == nil || !strings.Contains(err.Error(), "GATEWAY_PIPELINE_CUTOFF_UTC") {
		t.Fatalf("run() error = %v; expected cutoff validation", err)
	}
}
