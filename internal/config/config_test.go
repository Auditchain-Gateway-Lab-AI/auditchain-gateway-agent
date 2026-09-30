package config

import (
	"strings"
	"testing"
)

func TestRecoveryConfigDisabledByDefault(t *testing.T) {
	if err := (RecoveryConfig{}).Validate(""); err != nil {
		t.Fatalf("disabled recovery should not require secrets: %v", err)
	}
}

func TestRecoveryConfigRequiresSeparateLongToken(t *testing.T) {
	cfg := RecoveryConfig{
		Enabled:               true,
		ClientID:              "client-1",
		Token:                 strings.Repeat("r", 32),
		StatePath:             "state.db",
		MaxBodyBytes:          1024,
		MaxColumns:            8,
		MaxClockSkewSeconds:   300,
		RequestTimeoutSeconds: 15,
		RetentionDays:         90,
		PolicyPath:            "policy.yml",
	}
	if err := cfg.Validate(strings.Repeat("v", 32)); err != nil {
		t.Fatalf("valid recovery config rejected: %v", err)
	}
	if err := cfg.Validate(cfg.Token); err == nil {
		t.Fatal("verify and recovery token reuse should be rejected")
	}
}
