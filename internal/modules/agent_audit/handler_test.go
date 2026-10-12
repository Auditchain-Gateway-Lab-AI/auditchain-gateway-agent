package agent_audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeStore struct {
	latestCalls int
	clientID    string
	resource    string
	logs        []json.RawMessage
}

func (s *fakeStore) LatestByResource(_ context.Context, clientID, resource string) ([]json.RawMessage, error) {
	s.latestCalls++
	s.clientID, s.resource = clientID, resource
	return s.logs, nil
}

func (s *fakeStore) ByTimeRange(_ context.Context, clientID string, _, _ time.Time, _ int) ([]json.RawMessage, error) {
	s.clientID = clientID
	return s.logs, nil
}

type fakeFabric struct {
	root  string
	calls int
}

func (f *fakeFabric) QueryMerkleRoot(_ string) (string, error) {
	f.calls++
	return f.root, nil
}

func newTestHandler(t *testing.T, store *fakeStore, fabric *fakeFabric) *Handler {
	t.Helper()
	h, err := NewHandler("client-123", "verify-secret", store, fabric)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestOffchainResourceIsScopedToConfiguredClient(t *testing.T) {
	store := &fakeStore{logs: []json.RawMessage{json.RawMessage(`{"resource":"AGAMA:723"}`)}}
	h := newTestHandler(t, store, &fakeFabric{})
	r := httptest.NewRequest(http.MethodGet, offchainPath+"/AGAMA:723", nil)
	r.Header.Set("Authorization", "Bearer verify-secret")
	r.Header.Set("X-Client-ID", "client-123")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	if store.latestCalls != 1 || store.clientID != "client-123" || store.resource != "AGAMA:723" {
		t.Fatalf("unexpected store call: %+v", store)
	}
}

func TestOffchainRejectsOtherClientID(t *testing.T) {
	store := &fakeStore{}
	h := newTestHandler(t, store, &fakeFabric{})
	r := httptest.NewRequest(http.MethodGet, offchainPath+"/AGAMA:723", nil)
	r.Header.Set("Authorization", "Bearer verify-secret")
	r.Header.Set("X-Client-ID", "another-client")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden || store.latestCalls != 0 {
		t.Fatalf("status=%d store_calls=%d body=%s", w.Code, store.latestCalls, w.Body.String())
	}
}

func TestVerifyMerkleRootUsesReadOnlyFabricReader(t *testing.T) {
	root := strings.Repeat("a", 64)
	fabric := &fakeFabric{root: root}
	h := newTestHandler(t, &fakeStore{}, fabric)
	r := httptest.NewRequest(http.MethodPost, verifyMerklePath, strings.NewReader(
		`{"merkle_root":"`+strings.ToUpper(root)+`","blockchain_tx_id":"anchor-1"}`,
	))
	r.Header.Set("Authorization", "Bearer verify-secret")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK || fabric.calls != 1 {
		t.Fatalf("status=%d fabric_calls=%d body=%s", w.Code, fabric.calls, w.Body.String())
	}
	var response struct {
		IsMatch bool `json:"is_match"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.IsMatch {
		t.Fatalf("expected matching root; body=%s", w.Body.String())
	}
}

func TestVerifyMerkleRootRejectsInvalidRootBeforeFabricCall(t *testing.T) {
	fabric := &fakeFabric{root: strings.Repeat("b", 64)}
	h := newTestHandler(t, &fakeStore{}, fabric)
	r := httptest.NewRequest(http.MethodPost, verifyMerklePath,
		strings.NewReader(`{"merkle_root":"bad","blockchain_tx_id":"anchor-1"}`))
	r.Header.Set("Authorization", "Bearer verify-secret")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest || fabric.calls != 0 {
		t.Fatalf("status=%d fabric_calls=%d body=%s", w.Code, fabric.calls, w.Body.String())
	}
}

func TestOffchainRejectsOversizedResponse(t *testing.T) {
	largeJSON, err := json.Marshal(strings.Repeat("x", maxResponseBytes))
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{logs: []json.RawMessage{largeJSON}}
	h := newTestHandler(t, store, &fakeFabric{})
	r := httptest.NewRequest(http.MethodGet, offchainPath+"/AGAMA:723", nil)
	r.Header.Set("Authorization", "Bearer verify-secret")
	r.Header.Set("X-Client-ID", "client-123")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestVerifyMerkleRootMismatchReturnsFalse(t *testing.T) {
	h := newTestHandler(t, &fakeStore{}, &fakeFabric{root: strings.Repeat("b", 64)})
	r := httptest.NewRequest(http.MethodPost, verifyMerklePath, strings.NewReader(
		`{"merkle_root":"`+strings.Repeat("a", 64)+`","blockchain_tx_id":"anchor-1"}`,
	))
	r.Header.Set("Authorization", "Bearer verify-secret")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	var response struct {
		IsMatch bool `json:"is_match"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.IsMatch {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
