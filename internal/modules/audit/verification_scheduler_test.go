package audit

import (
	"testing"
	"time"

	"go-blockchain-api/internal/models"
)

func TestCalculateVerificationRangeUsesLookbackForFirstRun(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.FixedZone("WIB", 7*60*60))
	from, to := calculateVerificationRange(now, nil, 24*time.Hour, 5*time.Minute)

	if !to.Equal(now.UTC()) {
		t.Fatalf("to = %s, want %s", to, now.UTC())
	}
	if !from.Equal(now.UTC().Add(-24 * time.Hour)) {
		t.Fatalf("from = %s, want %s", from, now.UTC().Add(-24*time.Hour))
	}
}

func TestCalculateVerificationRangeResumesFromLastCompletedRunWithOverlap(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	latest := &models.VerificationRun{ToTime: now.Add(-2 * time.Hour)}
	from, to := calculateVerificationRange(now, latest, 24*time.Hour, 5*time.Minute)

	if !to.Equal(now) {
		t.Fatalf("to = %s, want %s", to, now)
	}
	wantFrom := now.Add(-2*time.Hour - 5*time.Minute)
	if !from.Equal(wantFrom) {
		t.Fatalf("from = %s, want %s", from, wantFrom)
	}
}

func TestCalculateVerificationRangeFallsBackWhenCursorIsInTheFuture(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	latest := &models.VerificationRun{ToTime: now.Add(time.Hour)}
	from, to := calculateVerificationRange(now, latest, 24*time.Hour, 5*time.Minute)

	if !from.Equal(now.Add(-24 * time.Hour)) {
		t.Fatalf("from = %s, want %s", from, now.Add(-24*time.Hour))
	}
	if !to.Equal(now) {
		t.Fatalf("to = %s, want %s", to, now)
	}
}
