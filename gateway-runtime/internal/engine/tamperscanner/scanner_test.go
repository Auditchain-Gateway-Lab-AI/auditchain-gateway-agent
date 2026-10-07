package tamperscanner

import (
	"testing"
	"time"

	"gorm.io/gorm"
)

type testVerifier struct{}

func (testVerifier) VerifyGatewayIntegrity(string, string) (string, error) {
	return "VALID", nil
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	if _, err := New(nil, testVerifier{}, Config{Interval: time.Minute, BatchSize: 1, Concurrency: 1}); err == nil {
		t.Fatal("expected missing database to be rejected")
	}
	if _, err := New(&gorm.DB{}, nil, Config{Interval: time.Minute, BatchSize: 1, Concurrency: 1}); err == nil {
		t.Fatal("expected missing verifier to be rejected")
	}
}

func TestNewAcceptsValidConfiguration(t *testing.T) {
	worker, err := New(&gorm.DB{}, testVerifier{}, Config{
		Enabled:     true,
		Interval:    5 * time.Minute,
		BatchSize:   100,
		Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if worker == nil {
		t.Fatal("expected worker instance")
	}
}
