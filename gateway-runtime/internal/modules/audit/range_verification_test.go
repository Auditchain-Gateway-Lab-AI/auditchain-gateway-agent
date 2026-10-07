package audit

import (
	"testing"
	"time"

	"go-blockchain-api/internal/models"
)

type rangeRepositoryStub struct {
	AuditRepository
	logs []models.AuditLog
}

func (r *rangeRepositoryStub) CountLogsByTimeRange(time.Time, time.Time, string) (int64, error) {
	return int64(len(r.logs)), nil
}

func (r *rangeRepositoryStub) GetLogsByTimeRange(time.Time, time.Time, string) ([]models.AuditLog, error) {
	return r.logs, nil
}

func (r *rangeRepositoryStub) GetLogsByTimeRangePage(_ time.Time, _ time.Time, _ string, limit, offset int) ([]models.AuditLog, error) {
	if offset >= len(r.logs) {
		return []models.AuditLog{}, nil
	}
	end := offset + limit
	if end > len(r.logs) {
		end = len(r.logs)
	}
	return r.logs[offset:end], nil
}

func TestVerifyLogRangeUsesEveryLogAndCachedResults(t *testing.T) {
	checkedAt := time.Date(2026, time.September, 30, 8, 0, 0, 0, time.UTC)
	repo := &rangeRepositoryStub{logs: []models.AuditLog{
		{
			LogID:              "log-1",
			ClientID:           "client-1",
			Resource:           "RUANGAN:283",
			IntegrityStatus:    models.IntegrityStatusValid,
			IntegrityCheckedAt: &checkedAt,
		},
		{
			LogID:              "log-2",
			ClientID:           "client-1",
			Resource:           "RUANGAN:283",
			IntegrityStatus:    models.IntegrityStatusTampered,
			IntegrityCheckedAt: &checkedAt,
		},
	}}

	service := &auditService{repo: repo}
	first, err := service.verifyLogRange(time.Time{}, time.Time{}, "client-1", "test-1")
	if err != nil {
		t.Fatalf("first range verification returned error: %v", err)
	}
	if first.Summary.Total != 2 || first.Summary.Valid != 1 || first.Summary.Invalid != 1 {
		t.Fatalf("unexpected first summary: %+v", first.Summary)
	}
	if first.Summary.AlreadyVerified != 2 || first.Summary.VerifiedNow != 0 {
		t.Fatalf("expected both logs to use cached results: %+v", first.Summary)
	}

	second, err := service.verifyLogRange(time.Time{}, time.Time{}, "client-1", "test-2")
	if err != nil {
		t.Fatalf("replayed range verification returned error: %v", err)
	}
	if second.Summary != first.Summary {
		t.Fatalf("replayed range changed summary: first=%+v second=%+v", first.Summary, second.Summary)
	}
}

func TestCachedRangeStatus(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   string
	}{
		{name: "valid", status: models.IntegrityStatusValid, want: "success"},
		{name: "pending", status: models.IntegrityStatusPending, want: "pending"},
		{name: "tampered", status: models.IntegrityStatusTampered, want: "failed_cached"},
		{name: "unreachable", status: models.IntegrityStatusUnreachable, want: "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := cachedRangeStatus(tt.status)
			if got != tt.want {
				t.Fatalf("cachedRangeStatus(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestVerifyRangeBatchUsesBoundedPageAndCachedSummary(t *testing.T) {
	checkedAt := time.Date(2026, time.September, 30, 8, 0, 0, 0, time.UTC)
	repo := &rangeRepositoryStub{logs: []models.AuditLog{
		{LogID: "log-1", ClientID: "client-1", IntegrityStatus: models.IntegrityStatusValid, IntegrityCheckedAt: &checkedAt},
		{LogID: "log-2", ClientID: "client-1", IntegrityStatus: models.IntegrityStatusPending, IntegrityCheckedAt: &checkedAt},
	}}

	service := &auditService{repo: repo}
	summary, loaded, err := service.VerifyRangeBatch(time.Time{}, time.Time{}, "client-1", "batch-test", "run-1", models.IntegritySourceScheduledRun, 1, 1)
	if err != nil {
		t.Fatalf("batch verification returned error: %v", err)
	}
	if loaded != 1 {
		t.Fatalf("loaded = %d, want 1", loaded)
	}
	if summary.Total != 1 || summary.Pending != 1 || summary.AlreadyVerified != 1 || summary.VerifiedNow != 0 {
		t.Fatalf("unexpected batch summary: %+v", summary)
	}
}

func TestIntegritySourceForVerificationRun(t *testing.T) {
	tests := []struct {
		requestedBy string
		want        string
	}{
		{requestedBy: "system:scheduler", want: models.IntegritySourceScheduledRun},
		{requestedBy: " SYSTEM:SCHEDULER ", want: models.IntegritySourceScheduledRun},
		{requestedBy: "user-123", want: models.IntegritySourceBackgroundRun},
		{requestedBy: "", want: models.IntegritySourceBackgroundRun},
	}

	for _, test := range tests {
		if got := integritySourceForVerificationRun(test.requestedBy); got != test.want {
			t.Errorf("integritySourceForVerificationRun(%q) = %q, want %q", test.requestedBy, got, test.want)
		}
	}
}
