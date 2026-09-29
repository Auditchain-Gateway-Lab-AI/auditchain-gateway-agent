package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"auditchain-agent/internal/canonicalstate"
)

type fakeRepository struct {
	mu    sync.Mutex
	state map[string]any
}

func (f *fakeRepository) Read(_ context.Context, table TablePolicy, recordID string) (StateSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == nil {
		f.state = map[string]any{}
	}
	copyState := cloneState(f.state)
	hash, _, err := canonicalstate.HashState("client-a", table.ResourceTable+":"+recordID, copyState)
	if err != nil {
		return StateSnapshot{}, err
	}
	return StateSnapshot{Found: len(copyState) > 0, State: copyState, Hash: hash}, nil
}

func (f *fakeRepository) Apply(_ context.Context, table TablePolicy, recordID string, request RecoveryRequest) (ApplyResult, error) {
	current, _ := f.Read(context.Background(), table, recordID)
	if current.Hash != request.ExpectedBeforeHash {
		return ApplyResult{}, ErrSourceStateChanged
	}
	f.mu.Lock()
	if request.Operation == OperationDelete {
		f.state = map[string]any{}
	} else if request.Operation == OperationUpsert {
		f.state = cloneState(request.DesiredState)
	}
	afterState := cloneState(f.state)
	f.mu.Unlock()
	after, _, err := canonicalstate.HashState("client-a", table.ResourceTable+":"+recordID, afterState)
	if err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{BeforeHash: current.Hash, AfterHash: after, FoundAfter: len(afterState) > 0, ReadbackMatch: after == request.DesiredStateHash}, nil
}

func cloneState(state map[string]any) map[string]any {
	copyState := make(map[string]any, len(state))
	for key, value := range state {
		copyState[key] = value
	}
	return copyState
}

func testService(t *testing.T) (*Service, RecoveryRequest, string) {
	t.Helper()
	policy, err := NewPolicySet(testPolicyFile())
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenIdempotencyStore(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(ServiceConfig{
		Enabled:      true,
		ClientID:     "client-a",
		MaxColumns:   8,
		MaxClockSkew: 5 * time.Minute,
		Retention:    24 * time.Hour,
	}, policy, &fakeRepository{}, store)
	service.now = func() time.Time { return time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC) }
	emptyHash, _, err := canonicalstate.HashState("client-a", "ROOM:1", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	desired := map[string]any{"id": "1", "name": "A"}
	desiredHash, _, err := canonicalstate.HashState("client-a", "ROOM:1", desired)
	if err != nil {
		t.Fatal(err)
	}
	request := RecoveryRequest{
		RequestID:          "123e4567-e89b-12d3-a456-426614174000",
		IdempotencyKey:     "command-1",
		Operation:          OperationUpsert,
		ExpectedBeforeHash: emptyHash,
		DesiredStateHash:   desiredHash,
		DesiredState:       desired,
		Reference: Reference{
			LogID:         "log-1",
			AuditLeafHash: strings.Repeat("a", 64),
			MerkleRoot:    strings.Repeat("b", 64),
			AnchorID:      "anchor-1",
		},
		IssuedAt: service.now(),
	}
	return service, request, desiredHash
}

func TestServiceIdempotentReplayAndConflict(t *testing.T) {
	service, request, _ := testService(t)
	first, err := service.Execute(context.Background(), "ROOM", "1", request)
	if err != nil {
		t.Fatal(err)
	}
	if first.IdempotentReplay || !first.Applied || !first.ReadbackMatch {
		t.Fatalf("unexpected first response: %#v", first)
	}

	replay, err := service.Execute(context.Background(), "ROOM", "1", request)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.IdempotentReplay || replay.AfterHash != first.AfterHash {
		t.Fatalf("unexpected replay response: %#v", replay)
	}

	request.DesiredState["name"] = "B"
	request.DesiredStateHash, _, _ = canonicalstate.HashState("client-a", "ROOM:1", request.DesiredState)
	_, err = service.Execute(context.Background(), "ROOM", "1", request)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "idempotency_conflict" {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestRecoveryHandlerDisabledAndAuth(t *testing.T) {
	handler := NewHandler(nil, HTTPConfig{Enabled: false, Token: strings.Repeat("x", 32), MaxBodyBytes: 1024})
	req := httptest.NewRequest(http.MethodPost, "/recover/ROOM/1", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusForbidden {
		t.Fatalf("disabled status = %d", response.Code)
	}

	service, request, _ := testService(t)
	handler = NewHandler(service, HTTPConfig{Enabled: true, Token: strings.Repeat("r", 32), MaxBodyBytes: 4096})
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/recover/ROOM/1", strings.NewReader(string(payload)))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("w", 32))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-token status = %d", response.Code)
	}
}
