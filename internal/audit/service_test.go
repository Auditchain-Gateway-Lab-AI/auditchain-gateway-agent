package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"auditchain-agent/internal/recovery"
	"auditchain-agent/internal/verify"
)

type mockResourceReader struct {
	records map[string]verify.ResourceRecord
}

type recoveryExecutorStub struct{ calls int }

func (s *recoveryExecutorStub) RecoverRow(_ context.Context, _, _ string, _ map[string]interface{}) (*recovery.RecoverResult, error) {
	s.calls++
	return &recovery.RecoverResult{Status: "SUCCESS"}, nil
}

func TestAuditSingleLogExplicitFalseDisablesConfiguredAutoRecovery(t *testing.T) {
	reader := &mockResourceReader{records: map[string]verify.ResourceRecord{
		"RUANGAN:1019": {Found: true, Table: "RUANGAN", ID: "1019", Data: map[string]interface{}{"id": 1019, "nama": "TAMPERED"}},
	}}
	recoveryExecutor := &recoveryExecutorStub{}
	service := NewService(ServiceConfig{ClientID: "tenant-1", AutoRecover: true}, NewGatewayClient("http://127.0.0.1:1", "token", time.Second), reader, recoveryExecutor)
	logEntry := OffchainLog{
		LogID: "log-1019", ClientID: "tenant-1", Resource: "RUANGAN:1019", Action: "UPDATE",
		Metadata: `{"data_baru":{"id":1019,"nama":"EXPECTED"}}`, IsLatest: true,
	}

	result := service.AuditSingleLog(context.Background(), logEntry, false)
	if result.DataStatus != "TAMPERED" {
		t.Fatalf("status = %q, want TAMPERED", result.DataStatus)
	}
	if recoveryExecutor.calls != 0 {
		t.Fatalf("recovery calls = %d, want zero when auto_recover=false", recoveryExecutor.calls)
	}
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
			LogID:     "log-hist",
			ClientID:  "test-client",
			Resource:  "KABUPATEN:1",
			Action:    "UPDATE",
			Metadata:  `{"data_baru":{"nama":"KABUPATEN BOGOR LAMA"}}`,
			Timestamp: t0,
			IsLatest:  false,
		},
		{
			LogID:     "log-latest",
			ClientID:  "test-client",
			Resource:  "KABUPATEN:1",
			Action:    "UPDATE",
			Metadata:  `{"data_baru":{"nama":"KABUPATEN BOGOR BARU"}}`,
			Timestamp: t1,
			IsLatest:  true,
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

func TestSubmitAuditReport(t *testing.T) {
	var receivedReport Report
	var authHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/audit-report" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		authHeader = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&receivedReport); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","run_id":"` + receivedReport.RunID + `"}`))
	}))
	defer server.Close()

	client := NewGatewayClient(server.URL, "secret-token", 5*time.Second)
	svc := NewService(ServiceConfig{ClientID: "test-client"}, client, nil, nil)

	testReport := &Report{
		RunID:        "run-12345",
		From:         time.Now().Add(-1 * time.Hour),
		To:           time.Now(),
		TotalLogs:    10,
		AuditedLogs:  5,
		DataValid:    4,
		DataTampered: 1,
	}

	err := svc.SubmitReport(context.Background(), testReport)
	if err != nil {
		t.Fatalf("SubmitReport failed: %v", err)
	}

	if authHeader != "Bearer secret-token" {
		t.Errorf("expected Bearer secret-token, got %s", authHeader)
	}
	if receivedReport.RunID != "run-12345" {
		t.Errorf("expected RunID run-12345, got %s", receivedReport.RunID)
	}
	if receivedReport.AuditedLogs != 5 {
		t.Errorf("expected AuditedLogs 5, got %d", receivedReport.AuditedLogs)
	}
}
