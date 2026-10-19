package audit

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"go-blockchain-api/internal/blockchain/agentverifier"
	"go-blockchain-api/internal/models"
)

func TestClassifyAgentVerificationFailure(t *testing.T) {
	tests := []struct {
		name             string
		err              error
		wantAgentStatus  string
		wantSourceStatus string
	}{
		{
			name:             "forbidden is not unreachable",
			err:              fmt.Errorf("wrapped verifier error: %w", &agentverifier.HTTPStatusError{StatusCode: http.StatusForbidden}),
			wantAgentStatus:  "forbidden",
			wantSourceStatus: models.SourceStatusNotComparable,
		},
		{
			name:             "unauthorized is not unreachable",
			err:              &agentverifier.HTTPStatusError{StatusCode: http.StatusUnauthorized},
			wantAgentStatus:  "unauthorized",
			wantSourceStatus: models.SourceStatusNotComparable,
		},
		{
			name:             "other HTTP failures are not connectivity failures",
			err:              &agentverifier.HTTPStatusError{StatusCode: http.StatusServiceUnavailable},
			wantAgentStatus:  "http_error",
			wantSourceStatus: models.SourceStatusNotComparable,
		},
		{
			name:             "transport failure is unreachable",
			err:              errors.New("dial tcp: connection refused"),
			wantAgentStatus:  "unreachable",
			wantSourceStatus: models.SourceStatusUnreachable,
		},
		{
			name:             "missing result is invalid response",
			wantAgentStatus:  "invalid_response",
			wantSourceStatus: models.SourceStatusNotComparable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAgentStatus, gotSourceStatus, message := classifyAgentVerificationFailure(tt.err)
			if gotAgentStatus != tt.wantAgentStatus || gotSourceStatus != tt.wantSourceStatus {
				t.Fatalf("classification = (%q, %q), want (%q, %q)", gotAgentStatus, gotSourceStatus, tt.wantAgentStatus, tt.wantSourceStatus)
			}
			if message == "" {
				t.Fatal("classification message is empty")
			}
		})
	}
}

func TestSourceStatusFromAgentStatusTreatsDenialsAsNotComparable(t *testing.T) {
	for _, status := range []string{"forbidden", "unauthorized", "http_error", "invalid_response"} {
		if got := sourceStatusFromAgentStatus(status); got != models.SourceStatusNotComparable {
			t.Errorf("sourceStatusFromAgentStatus(%q) = %q, want %q", status, got, models.SourceStatusNotComparable)
		}
	}
}

type verifyLogRepositoryStub struct {
	AuditRepository
	log models.AuditLog
}

func (r *verifyLogRepositoryStub) GetLogByID(logID, clientID string) (*models.AuditLog, error) {
	if logID != r.log.LogID || clientID != r.log.ClientID {
		return nil, errors.New("unexpected tenant-scoped log lookup")
	}
	logCopy := r.log
	return &logCopy, nil
}

func (r *verifyLogRepositoryStub) GetLogsByResource(string, string) ([]models.AuditLog, error) {
	return []models.AuditLog{r.log}, nil
}

func TestManualVerifyReportsTamperIncidentPersistenceFailure(t *testing.T) {
	repo := &verifyLogRepositoryStub{log: models.AuditLog{
		LogID:     "1791354247037786430",
		ClientID:  "client-1",
		Resource:  "RUANGAN:620",
		Action:    "UPDATE",
		Metadata:  `{"id":620,"nama":"tampered"}`,
		HashValue: "trusted-hash",
	}}
	service := &auditService{repo: repo}

	result, err := service.VerifyLogIntegrity(repo.log.LogID, repo.log.ClientID)
	if !errors.Is(err, errIncidentPersistence) {
		t.Fatalf("VerifyLogIntegrity error = %v, want tamper incident persistence error", err)
	}
	if result == nil || result.Status != "failed_local" {
		t.Fatalf("VerifyLogIntegrity result = %+v, want local tamper result", result)
	}
	if result.IncidentType != "METADATA_HASH_MISMATCH" {
		t.Fatalf("incident type = %q, want METADATA_HASH_MISMATCH", result.IncidentType)
	}
}

func TestResourceVerifyReportsTamperIncidentPersistenceFailure(t *testing.T) {
	repo := &verifyLogRepositoryStub{log: models.AuditLog{
		LogID:     "1791354247037786430",
		ClientID:  "client-1",
		Resource:  "RUANGAN:620",
		Action:    "RECOVERY",
		Metadata:  `{"id":620,"nama":"tampered"}`,
		HashValue: "trusted-hash",
	}}
	service := &auditService{repo: repo}

	_, err := service.VerifyResourceHistory(repo.log.Resource, repo.log.ClientID)
	if !errors.Is(err, errIncidentPersistence) {
		t.Fatalf("VerifyResourceHistory error = %v, want incident persistence error", err)
	}
}

func TestResourceGatewayStatusesDoNotUseAgentReachability(t *testing.T) {
	tests := []struct {
		name          string
		baseStatus    string
		agentStatus   string
		wantIntegrity string
		wantChain     string
	}{
		{
			name:          "valid gateway remains valid when agent unreachable",
			baseStatus:    "valid",
			agentStatus:   "unreachable",
			wantIntegrity: "valid",
			wantChain:     "valid",
		},
		{
			name:          "valid gateway remains valid when agent mismatches",
			baseStatus:    "valid",
			agentStatus:   "mismatch",
			wantIntegrity: "valid",
			wantChain:     "valid",
		},
		{
			name:          "tampered gateway remains tampered",
			baseStatus:    "tampered",
			agentStatus:   "unreachable",
			wantIntegrity: "tampered",
			wantChain:     "tampered",
		},
		{
			name:          "fabric unreachable remains unreachable",
			baseStatus:    "unreachable",
			agentStatus:   "matched",
			wantIntegrity: "unreachable",
			wantChain:     "unreachable",
		},
		{
			name:          "unanchored gateway remains pending",
			baseStatus:    "pending",
			agentStatus:   "unreachable",
			wantIntegrity: "pending",
			wantChain:     "pending",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIntegrity, gotChain := resourceGatewayStatuses(tt.baseStatus, tt.agentStatus)
			if gotIntegrity != tt.wantIntegrity || gotChain != tt.wantChain {
				t.Fatalf("resourceGatewayStatuses(%q) = (%q, %q), want (%q, %q)",
					tt.baseStatus, gotIntegrity, gotChain, tt.wantIntegrity, tt.wantChain)
			}
		})
	}
}

func TestRecoveryDisplayStatus(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   string
	}{
		{name: "no request", status: "", want: recoveryDisplayNotRecovered},
		{name: "client executable", status: models.RecoveryStatusPendingExecution, want: recoveryDisplayPending},
		{name: "legacy approval", status: models.RecoveryStatusApproved, want: recoveryDisplayPending},
		{name: "executing", status: models.RecoveryStatusExecuting, want: recoveryDisplayPending},
		{name: "succeeded", status: models.RecoveryStatusSucceeded, want: recoveryDisplayRecovered},
		{name: "verification failed", status: models.RecoveryStatusFailedVerification, want: recoveryDisplayFailed},
		{name: "execution failed", status: models.RecoveryStatusFailedExecution, want: recoveryDisplayFailed},
		{name: "rejected", status: models.RecoveryStatusRejected, want: recoveryDisplayFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := recoveryDisplayStatus(tt.status); got != tt.want {
				t.Fatalf("recoveryDisplayStatus(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestShouldVerifyResourceWithAgent(t *testing.T) {
	tests := []struct {
		name     string
		action   string
		isLatest bool
		want     bool
	}{
		{name: "latest client event", action: "UPDATE", isLatest: true, want: true},
		{name: "historical client event", action: "UPDATE", isLatest: false, want: false},
		{name: "latest recovery event", action: "RECOVERY", isLatest: true, want: false},
		{name: "latest recovery event ignores casing and spaces", action: " recovery ", isLatest: true, want: false},
		{name: "historical recovery event", action: "RECOVERY", isLatest: false, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := models.AuditLog{Action: tt.action}
			if got := shouldVerifyResourceWithAgent(log, tt.isLatest); got != tt.want {
				t.Fatalf("shouldVerifyResourceWithAgent(%q, %t) = %t, want %t", tt.action, tt.isLatest, got, tt.want)
			}
		})
	}
}

func TestLatestClientEventIndexSkipsRecoveryEvents(t *testing.T) {
	tests := []struct {
		name string
		logs []models.AuditLog
		want int
	}{
		{
			name: "update remains latest client event after recovery",
			logs: []models.AuditLog{
				{Action: "INSERT"},
				{Action: "UPDATE"},
				{Action: "RECOVERY"},
			},
			want: 1,
		},
		{
			name: "multiple recovery events do not hide delete",
			logs: []models.AuditLog{
				{Action: "DELETE"},
				{Action: "RECOVERY"},
				{Action: " recovery "},
			},
			want: 0,
		},
		{
			name: "new client event after recovery becomes latest",
			logs: []models.AuditLog{
				{Action: "UPDATE"},
				{Action: "RECOVERY"},
				{Action: "UPDATE"},
			},
			want: 2,
		},
		{
			name: "only recovery events have no client event",
			logs: []models.AuditLog{
				{Action: "RECOVERY"},
			},
			want: -1,
		},
		{
			name: "empty history has no client event",
			want: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := latestClientEventIndex(tt.logs); got != tt.want {
				t.Fatalf("latestClientEventIndex() = %d, want %d", got, tt.want)
			}
		})
	}
}
