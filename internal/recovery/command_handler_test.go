package recovery

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"auditchain-agent/internal/canonicalstate"
)

func validCommandPayload() RecoveryCommandRequest {
	request := RecoveryCommandRequest{
		RequestID: "550e8400-e29b-41d4-a716-446655440000", IdempotencyKey: "agent-command-1",
		Operation: "UPSERT", ExpectedBeforeHash: strings.Repeat("a", 64), DesiredStateHash: strings.Repeat("b", 64),
		DesiredState: map[string]interface{}{"id": 1019, "nama": "RUANGAN 1019"}, IssuedAt: time.Now().UTC(),
	}
	request.Reference.LogID = "audit-log-1019"
	request.Reference.AuditLeafHash = strings.Repeat("c", 64)
	request.Reference.MerkleRoot = strings.Repeat("d", 64)
	request.Reference.AnchorID = "fabric-anchor-1"
	return request
}

func TestCommandHandlerRequiresRecoveryTokenSeparateFromVerifyToken(t *testing.T) {
	commandService := &CommandService{enabled: true, clientID: "tenant-1", policy: nil, maxSkew: time.Minute, maxColumns: 64}
	handler := NewConfiguredHandler(nil, "recovery-token-that-is-long-enough", true, "tenant-1", commandService, 4096, time.Second)
	command := validCommandPayload()
	command.DesiredStateHash, _, _ = canonicalstate.HashState("tenant-1", "RUANGAN:1019", command.DesiredState)
	body, _ := json.Marshal(command)

	verifyRequest := httptest.NewRequest(http.MethodPost, "/recover/RUANGAN/1019", bytes.NewReader(body))
	verifyRequest.Header.Set("Authorization", "Bearer verify-token")
	verifyResponse := httptest.NewRecorder()
	handler.ServeHTTP(verifyResponse, verifyRequest)
	if verifyResponse.Code != http.StatusUnauthorized || !strings.Contains(verifyResponse.Body.String(), "invalid_agent_token") {
		t.Fatalf("verify-token response = %d %s", verifyResponse.Code, verifyResponse.Body.String())
	}

	writeRequest := httptest.NewRequest(http.MethodPost, "/recover/RUANGAN/1019", bytes.NewReader(body))
	writeRequest.Header.Set("Authorization", "Bearer recovery-token-that-is-long-enough")
	writeResponse := httptest.NewRecorder()
	handler.ServeHTTP(writeResponse, writeRequest)
	if writeResponse.Code != http.StatusForbidden || !strings.Contains(writeResponse.Body.String(), "table_not_allowed") {
		t.Fatalf("recovery-token response = %d %s", writeResponse.Code, writeResponse.Body.String())
	}
}

func TestCommandHandlerRejectsDisabledWriteRoute(t *testing.T) {
	handler := NewConfiguredHandler(nil, "recovery-token", false, "", nil, 1024, time.Second)
	req := httptest.NewRequest(http.MethodPost, "/recover/RUANGAN/1019", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer recovery-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "recovery_disabled") {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestCommandHandlerEnforcesBodyLimit(t *testing.T) {
	handler := NewConfiguredHandler(nil, "recovery-token", true, "tenant-1", &CommandService{enabled: true, clientID: "tenant-1"}, 8, time.Second)
	req := httptest.NewRequest(http.MethodPost, "/recover/RUANGAN/1019", strings.NewReader(`{"large":"payload"}`))
	req.Header.Set("Authorization", "Bearer recovery-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}
