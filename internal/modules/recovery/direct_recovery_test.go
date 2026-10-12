package recovery

import (
	"encoding/json"
	"strings"
	"testing"

	"go-blockchain-api/internal/blockchain/agentverifier"
	"go-blockchain-api/internal/models"
)

func TestDirectDesiredStateDeleteIsEmptyRegardlessOfHistoricalMetadata(t *testing.T) {
	logRow := &models.AuditLog{Metadata: `{"id":7,"nama":"old"}`}
	state, raw, err := directDesiredState(logRow, models.RecoveryOperationDelete)
	if err != nil {
		t.Fatalf("directDesiredState() error = %v", err)
	}
	if len(state) != 0 || string(raw) != `{}` {
		t.Fatalf("delete desired state = %#v/%s, want empty object", state, raw)
	}
}

func TestDirectSourceStatusDistinguishesMismatchAndMissing(t *testing.T) {
	missing := &agentverifier.ResourceRecord{Found: false}
	if got := directSourceStatus(models.RecoveryOperationUpsert, missing, strings.Repeat("a", 64), strings.Repeat("b", 64)); got != models.SourceStatusMissing {
		t.Fatalf("missing status = %q", got)
	}
	found := &agentverifier.ResourceRecord{Found: true}
	if got := directSourceStatus(models.RecoveryOperationUpsert, found, strings.Repeat("a", 64), strings.Repeat("b", 64)); got != models.SourceStatusMismatch {
		t.Fatalf("mismatch status = %q", got)
	}
	if got := directSourceStatus(models.RecoveryOperationDelete, found, strings.Repeat("a", 64), ""); got != models.SourceStatusUnexpectedPresent {
		t.Fatalf("delete status = %q", got)
	}
}

func TestDirectCDCMatchesCanonicalizesMetadata(t *testing.T) {
	request := &models.RecoveryRequest{
		ClientID: "client-a", Resource: "RUANGAN:7", Operation: models.RecoveryOperationUpsert,
		DesiredStateHash: "",
	}
	state := map[string]interface{}{"id": 7, "nama": "A"}
	hash, err := agentverifier.HashResourceState(request.ClientID, request.Resource, state)
	if err != nil {
		t.Fatalf("HashResourceState() error = %v", err)
	}
	request.DesiredStateHash = hash
	logRow := &models.AuditLog{Action: "UPDATE", Metadata: `{"NAMA":"A","ID":7}`}
	if !directCDCMatches(request, logRow) {
		t.Fatal("CDC metadata should match canonical desired state")
	}
}

func TestDirectFailureStatusesSeparateVerificationAndExecution(t *testing.T) {
	requestStatus, eventStatus := directFailureStatuses("source_state_changed")
	if requestStatus != models.RecoveryStatusFailedVerification || eventStatus != models.RecoveryResultVerification {
		t.Fatalf("source_state_changed statuses = %q/%q, want verification", requestStatus, eventStatus)
	}
	requestStatus, eventStatus = directFailureStatuses("readback_mismatch")
	if requestStatus != models.RecoveryStatusFailedExecution || eventStatus != models.RecoveryResultExecution {
		t.Fatalf("readback_mismatch statuses = %q/%q, want execution", requestStatus, eventStatus)
	}
}

func TestRecoveryEvidenceRedactsSensitiveMetadata(t *testing.T) {
	got := redactedRecoveryMetadata([]byte(`{"id":7,"password":"secret","nested":{"access_token":"bearer"}}`))
	want := `{"id":7,"nested":{"access_token":"***REDACTED***"},"password":"***REDACTED***"}`
	if got != want {
		t.Fatalf("redacted recovery metadata = %s, want %s", got, want)
	}
}

func TestRecoveryRequestDoesNotSerializeDesiredState(t *testing.T) {
	encoded, err := json.Marshal(models.RecoveryRequest{ID: "request-1", DesiredState: `{"password":"secret"}`})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "desired_state") {
		t.Fatalf("request JSON leaked desired state: %s", encoded)
	}
}
