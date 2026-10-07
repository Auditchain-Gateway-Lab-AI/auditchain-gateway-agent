package agentverifier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-blockchain-api/internal/models"
)

func TestExecuteRecoveryRequestUsesWriteTokenAndContract(t *testing.T) {
	var seenAuth string
	var seenPath string
	var seen RecoveryCommand
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenPath = r.URL.Path
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected request method/content type: %s/%s", r.Method, r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Fatalf("decode command: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"req-1","operation":"UPSERT","applied":true,"before_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","after_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","readback_match":true,"found_after":true,"checked_at":"2026-09-29T00:00:01Z"}`))
	}))
	defer server.Close()

	command := validRecoveryCommand()
	cfg := &models.AgentConfig{AgentURL: server.URL, RecoveryToken: "write-token", TimeoutSeconds: 2}
	result, err := executeRecoveryRequest(context.Background(), cfg, command, "RUANGAN", "7", nil)
	if err != nil {
		t.Fatalf("executeRecoveryRequest() error = %v", err)
	}
	if result == nil || !result.Applied || !result.ReadbackMatch {
		t.Fatalf("unexpected result: %#v", result)
	}
	if seenAuth != "Bearer write-token" {
		t.Fatalf("authorization = %q, want recovery token", seenAuth)
	}
	if seenPath != "/recover/RUANGAN/7" {
		t.Fatalf("path = %q", seenPath)
	}
	if seen.RequestID != command.RequestID || seen.Reference.AnchorID != command.Reference.AnchorID {
		t.Fatalf("decoded command lost immutable reference: %#v", seen)
	}
}

func TestExecuteRecoveryRequestClassifiesAgentError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"source_state_changed","message":"state changed"}`))
	}))
	defer server.Close()

	_, err := executeRecoveryRequest(context.Background(), &models.AgentConfig{AgentURL: server.URL, RecoveryToken: "write-token"}, validRecoveryCommand(), "RUANGAN", "7", nil)
	if err == nil {
		t.Fatal("expected conflict error")
	}
	recoveryErr, ok := err.(*RecoveryError)
	if !ok || recoveryErr.HTTPStatus != http.StatusConflict || recoveryErr.Code != "source_state_changed" {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestValidateRecoveryCommandRejectsInvalidOperationsAndHashes(t *testing.T) {
	command := validRecoveryCommand()
	command.Operation = "SQL"
	if err := validateRecoveryCommand(command); err == nil || !strings.Contains(err.Error(), "operation_invalid") {
		t.Fatalf("invalid operation error = %v", err)
	}
	command = validRecoveryCommand()
	command.ExpectedBeforeHash = "not-a-hash"
	if err := validateRecoveryCommand(command); err == nil || !strings.Contains(err.Error(), "expected_before_hash_invalid") {
		t.Fatalf("invalid hash error = %v", err)
	}
}

func TestHashResourceStateIsTenantAndResourceScoped(t *testing.T) {
	state := map[string]interface{}{"ID": 7, "Nama": "A"}
	one, err := HashResourceState("client-a", "RUANGAN:7", state)
	if err != nil {
		t.Fatalf("HashResourceState() error = %v", err)
	}
	two, err := HashResourceState("client-b", "RUANGAN:7", state)
	if err != nil {
		t.Fatalf("HashResourceState() error = %v", err)
	}
	if one == two || len(one) != 64 {
		t.Fatalf("hashes are not tenant scoped: %q/%q", one, two)
	}
}

func validRecoveryCommand() RecoveryCommand {
	return RecoveryCommand{
		ClientID:           "client-a",
		RequestID:          "req-1",
		IdempotencyKey:     "idem-1",
		Operation:          models.RecoveryOperationUpsert,
		ExpectedBeforeHash: strings.Repeat("a", 64),
		DesiredStateHash:   strings.Repeat("b", 64),
		DesiredState:       map[string]interface{}{"id": 7, "nama": "A"},
		Reference: RecoveryReference{
			LogID:         "log-1",
			AuditLeafHash: strings.Repeat("c", 64),
			MerkleRoot:    strings.Repeat("d", 64),
			AnchorID:      "anchor-1",
		},
		IssuedAt: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC),
	}
}
