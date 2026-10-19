package config

import (
	"testing"
)

func TestRecoveryConfigDefaults(t *testing.T) {
	cfg := RecoveryConfig{
		Enabled:  true,
		ClientID: "client-1",
		Token:    "secret",
	}
	if !cfg.Enabled || cfg.ClientID != "client-1" || cfg.Token != "secret" {
		t.Fatal("unexpected recovery config values")
	}
}
