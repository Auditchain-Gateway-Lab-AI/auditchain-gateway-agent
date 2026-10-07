package audit

import (
	"testing"

	"go-blockchain-api/internal/models"
)

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
