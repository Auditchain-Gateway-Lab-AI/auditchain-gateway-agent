package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"auditchain-agent/internal/verify"
)

type mockResourceReader struct {
	records map[string]verify.ResourceRecord
}

func (m *mockResourceReader) QueryResource(ctx context.Context, tableName, resourceID string) (verify.ResourceRecord, error) {
	key := tableName + ":" + resourceID
	if rec, ok := m.records[key]; ok {
		return rec, nil
	}
	return verify.ResourceRecord{Found: false, Table: tableName, ID: resourceID}, nil
}

func TestFilterLatestLogs(t *testing.T) {
	t0 := time.Now().Add(-2 * time.Hour)
	t1 := time.Now().Add(-1 * time.Hour)
	t2 := time.Now()

	logs := []OffchainLog{
		{LogID: "log-1", Resource: "KABUPATEN:1", Timestamp: t0, IsLatest: false},
		{LogID: "log-2", Resource: "KABUPATEN:1", Timestamp: t1, IsLatest: true},
		{LogID: "log-3", Resource: "KABUPATEN:2", Timestamp: t1, IsLatest: false},
		{LogID: "log-4", Resource: "KABUPATEN:2", Timestamp: t2, IsLatest: true},
	}

	filtered := FilterLatestLogs(logs)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 latest logs, got %d", len(filtered))
	}

	expectedIDs := map[string]bool{"log-2": true, "log-4": true}
	for _, l := range filtered {
		if !expectedIDs[l.LogID] {
			t.Errorf("unexpected log in filtered output: %s", l.LogID)
		}
		if !l.IsLatest {
			t.Errorf("filtered log %s should have IsLatest true", l.LogID)
		}
	}
}

func TestAuditSingleLog_ValidFlow(t *testing.T) {
	var merkleRootVerifyCalls int32

	// Mock Gateway server
	gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agent/verify-merkle-root":
			atomic.AddInt32(&merkleRootVerifyCalls, 1)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(VerifyMerkleRootResponse{
				IsMatch:            true,
				ExpectedMerkleRoot: "1111111111111111111111111111111111111111111111111111111111111111",
				ActualMerkleRoot:   "1111111111111111111111111111111111111111111111111111111111111111",
				BlockchainTxID:     "tx-123",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gatewayServer.Close()

	reader := &mockResourceReader{
		records: map[string]verify.ResourceRecord{
			"KABUPATEN:1": {
				Found: true,
				Table: "KABUPATEN",
				ID:    "1",
				Data: map[string]interface{}{
					"nama": "KABUPATEN BOGOR",
				},
			},
		},
	}

	gwClient := NewGatewayClient(gatewayServer.URL, "test-token", 5*time.Second)
	svc := NewService(ServiceConfig{ClientID: "test-client"}, gwClient, reader, nil)

	txID := "tx-123"
	logEntry := OffchainLog{
		LogID:          "log-001",
		ClientID:       "test-client",
		Resource:       "KABUPATEN:1",
		Action:         "UPDATE",
		Metadata:       `{"data_baru":{"nama":"KABUPATEN BOGOR"}}`,
		MerkleRoot:     "1111111111111111111111111111111111111111111111111111111111111111",
		BlockchainTxID: &txID,
		IsLatest:       true,
	}

	result := svc.AuditSingleLog(context.Background(), logEntry, false)

	if result.DataStatus != "VALID" {
		t.Fatalf("expected DataStatus VALID, got %s (err: %s)", result.DataStatus, result.Error)
	}
	if result.BlockchainStatus != "VALID" {
		t.Fatalf("expected BlockchainStatus VALID, got %s (err: %s)", result.BlockchainStatus, result.Error)
	}
	if calls := atomic.LoadInt32(&merkleRootVerifyCalls); calls != 1 {
		t.Fatalf("expected verify-merkle-root to be called 1 time, got %d", calls)
	}
}

func TestAuditSingleLog_TamperedFlow_SkipsMerkleVerification(t *testing.T) {
	var merkleRootVerifyCalls int32

	// Mock Gateway server
	gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agent/verify-merkle-root":
			atomic.AddInt32(&merkleRootVerifyCalls, 1)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gatewayServer.Close()

	// Client database has tampered data
	reader := &mockResourceReader{
		records: map[string]verify.ResourceRecord{
			"KABUPATEN:1": {
				Found: true,
				Table: "KABUPATEN",
				ID:    "1",
				Data: map[string]interface{}{
					"nama": "KABUPATEN BOGOR (TAMPERED BY ATTACKER)",
				},
			},
		},
	}

	gwClient := NewGatewayClient(gatewayServer.URL, "test-token", 5*time.Second)
	svc := NewService(ServiceConfig{ClientID: "test-client"}, gwClient, reader, nil)

	txID := "tx-123"
	logEntry := OffchainLog{
		LogID:          "log-002",
		ClientID:       "test-client",
		Resource:       "KABUPATEN:1",
		Action:         "UPDATE",
		Metadata:       `{"data_baru":{"nama":"KABUPATEN BOGOR"}}`,
		MerkleRoot:     "1111111111111111111111111111111111111111111111111111111111111111",
		BlockchainTxID: &txID,
		IsLatest:       true,
	}

	result := svc.AuditSingleLog(context.Background(), logEntry, false)

	if result.DataStatus != "TAMPERED" {
		t.Fatalf("expected DataStatus TAMPERED, got %s", result.DataStatus)
	}
	if result.BlockchainStatus != "SKIPPED" {
		t.Fatalf("expected BlockchainStatus SKIPPED when data is tampered, got %s", result.BlockchainStatus)
	}

	// CRITICAL TEST: Verify Merkle Root must NOT be called if data is tampered!
	if calls := atomic.LoadInt32(&merkleRootVerifyCalls); calls != 0 {
		t.Fatalf("verify-merkle-root API must NOT be called when client hash is tampered, but was called %d times", calls)
	}
}

func TestAuditTimeRange_FiltersHistoricalLogs(t *testing.T) {
	t0 := time.Now().Add(-2 * time.Hour)
	t1 := time.Now().Add(-1 * time.Hour)

	mockLogs := []OffchainLog{
		{
			LogID:      "log-hist",
			ClientID:   "test-client",
			Resource:   "KABUPATEN:1",
			Action:     "UPDATE",
			Metadata:   `{"data_baru":{"nama":"KABUPATEN BOGOR LAMA"}}`,
			Timestamp:  t0,
			IsLatest:   false,
		},
		{
			LogID:      "log-latest",
			ClientID:   "test-client",
			Resource:   "KABUPATEN:1",
			Action:     "UPDATE",
			Metadata:   `{"data_baru":{"nama":"KABUPATEN BOGOR BARU"}}`,
			Timestamp:  t1,
			IsLatest:   true,
		},
	}

	gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(mockLogs)
	}))
	defer gatewayServer.Close()

	reader := &mockResourceReader{
		records: map[string]verify.ResourceRecord{
			"KABUPATEN:1": {
				Found: true,
				Table: "KABUPATEN",
				ID:    "1",
				Data: map[string]interface{}{
					"nama": "KABUPATEN BOGOR BARU",
				},
			},
		},
	}

	gwClient := NewGatewayClient(gatewayServer.URL, "test-token", 5*time.Second)
	svc := NewService(ServiceConfig{ClientID: "test-client"}, gwClient, reader, nil)

	report, err := svc.AuditTimeRange(context.Background(), t0.Add(-1*time.Hour), t1.Add(1*time.Hour), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.TotalLogs != 2 {
		t.Errorf("expected TotalLogs 2, got %d", report.TotalLogs)
	}
	if report.AuditedLogs != 1 {
		t.Errorf("expected AuditedLogs 1, got %d", report.AuditedLogs)
	}
	if report.HistoricalSkipped != 1 {
		t.Errorf("expected HistoricalSkipped 1, got %d", report.HistoricalSkipped)
	}
	if report.DataValid != 1 {
		t.Errorf("expected DataValid 1, got %d", report.DataValid)
	}
	if report.DataTampered != 0 {
		t.Errorf("expected DataTampered 0, got %d (historical was wrongly audited)", report.DataTampered)
	}
}
