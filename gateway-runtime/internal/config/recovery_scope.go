package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// LoadRecoveryCutoff returns the deployment boundary for the recovery scope.
// Rows created before this instant are legacy audit history: they remain
// verifiable against Fabric, but are intentionally not recoverable because
// they predate the MinIO snapshot pipeline.
func LoadRecoveryCutoff() (*time.Time, error) {
	raw := strings.TrimSpace(os.Getenv("RECOVERY_CUTOFF_AT"))
	if raw == "" {
		return nil, nil
	}
	cutoff, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("RECOVERY_CUTOFF_AT harus memakai RFC3339: %w", err)
	}
	cutoff = cutoff.UTC()
	return &cutoff, nil
}

// InRecoveryScope reports whether a row belongs to the snapshot-backed
// recovery scope. A nil cutoff preserves the legacy behavior for components
// that are explicitly used without recovery enabled (for example local tools).
func InRecoveryScope(createdAt *time.Time, cutoff *time.Time) bool {
	if cutoff == nil {
		return true
	}
	return createdAt != nil && !createdAt.Before(*cutoff)
}
