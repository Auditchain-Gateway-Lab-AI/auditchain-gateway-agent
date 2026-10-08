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

func TestFetchAuditTrailUsesDedicatedReadEndpoint(t *testing.T) {
	var seenAuth, seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"found":true,"id":"620","tabel":"RUANGAN","operasi":"DELETE","db_user":"POLINEMA","data_lama":{"id":620,"nama":"ruangan"},"data_baru":null,"waktu":"2026-10-08T02:00:00Z"}`))
	}))
	defer server.Close()

	record, err := fetchAuditTrailFromAgent(context.Background(), &models.AgentConfig{
		AgentURL: server.URL, VerifyToken: "read-token", TimeoutSeconds: 2,
	}, "620")
	if err != nil {
		t.Fatalf("fetchAuditTrailFromAgent() error = %v", err)
	}
	if seenAuth != "Bearer read-token" || seenPath != "/verify-audit/620" {
		t.Fatalf("request auth/path = %q/%q", seenAuth, seenPath)
	}
	metadata, err := record.MetadataJSON()
	if err != nil {
		t.Fatalf("MetadataJSON() error = %v", err)
	}
	if string(metadata) != `{"data_lama":{"id":620,"nama":"ruangan"}}` {
		t.Fatalf("metadata = %s", metadata)
	}
}

func TestFetchAuditTrailCandidatesUsesBoundedLookupContract(t *testing.T) {
	var seenAuth string
	var seenPath string
	var seenQuery = make(map[string]string)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenPath = r.URL.Path
		for key, values := range r.URL.Query() {
			if len(values) > 0 {
				seenQuery[key] = values[0]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"records":[{"found":true,"id":"audit-613","tabel":"RUANGAN","operasi":"DELETE","data_lama":{"ID":613},"waktu":"2026-10-08T03:00:00Z"}],"truncated":false}`))
	}))
	defer server.Close()

	at := time.Date(2026, 10, 8, 3, 0, 0, 0, time.FixedZone("UTC+7", 7*60*60))
	result, err := fetchAuditTrailCandidatesFromAgent(context.Background(), &models.AgentConfig{
		AgentURL: server.URL, VerifyToken: "read-token", TimeoutSeconds: 2,
	}, "RUANGAN", "DELETE", "ID", "613", at)
	if err != nil {
		t.Fatalf("fetchAuditTrailCandidatesFromAgent() error = %v", err)
	}
	if seenAuth != "Bearer read-token" || seenPath != "/verify-audit-lookup" {
		t.Fatalf("request auth/path = %q/%q", seenAuth, seenPath)
	}
	if seenQuery["table"] != "RUANGAN" || seenQuery["operation"] != "DELETE" || seenQuery["primary_key"] != "ID" || seenQuery["record_id"] != "613" || seenQuery["window_seconds"] != "300" || seenQuery["limit"] != "50" {
		t.Fatalf("unexpected lookup query: %#v", seenQuery)
	}
	if result == nil || len(result.Records) != 1 || result.Records[0].ID != "audit-613" || result.Truncated {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestFetchAuditTrailCandidatesMapsAgentLookupErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantReason string
	}{
		{
			name: "database unavailable", status: http.StatusServiceUnavailable,
			body: `{"code":"database_unreachable"}`, wantReason: "agent_audit_trail_lookup_database_unavailable",
		},
		{
			name: "lookup query failed", status: http.StatusInternalServerError,
			body: `{"code":"audit_trail_lookup_failed"}`, wantReason: "agent_audit_trail_lookup_failed",
		},
		{
			name: "history table missing", status: http.StatusNotFound,
			body: `{"code":"audit_trail_table_not_found"}`, wantReason: "agent_audit_trail_table_not_found",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			_, err := fetchAuditTrailCandidatesFromAgent(context.Background(), &models.AgentConfig{
				AgentURL: server.URL, TimeoutSeconds: 2,
			}, "RUANGAN", "DELETE", "ID", "613", time.Now())
			if err == nil || !strings.HasPrefix(err.Error(), test.wantReason) {
				t.Fatalf("error = %v, want prefix %q", err, test.wantReason)
			}
		})
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
