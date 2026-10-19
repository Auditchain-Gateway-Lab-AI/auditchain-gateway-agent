package recovery

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandler_Unauthorized(t *testing.T) {
	handler := NewHandler(nil, "secret-token")
	req := httptest.NewRequest(http.MethodPost, "/recover", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", rec.Code)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	handler := NewHandler(nil, "")
	req := httptest.NewRequest(http.MethodGet, "/recover", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 Method Not Allowed, got %d", rec.Code)
	}
}

func TestHandler_MissingResourceAndTable(t *testing.T) {
	handler := NewHandler(&Service{}, "token")
	req := httptest.NewRequest(http.MethodPost, "/recover", bytes.NewReader([]byte(`{"data":{"NAMA":"TEST"}}`)))
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for missing table/id or resource, got %d", rec.Code)
	}
}

func TestHandler_EmptyData(t *testing.T) {
	handler := NewHandler(&Service{}, "token")
	req := httptest.NewRequest(http.MethodPost, "/recover", bytes.NewReader([]byte(`{"resource":"KABUPATEN:1","data":{}}`)))
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for empty data, got %d", rec.Code)
	}
}

func TestHandler_ValidResourceFormat(t *testing.T) {
	reqBody := RecoverRequest{
		Resource: "KABUPATEN:3201",
		Data: map[string]interface{}{
			"NAMA":   "KABUPATEN BOGOR",
			"STATUS": "AKTIF",
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	handler := NewHandler(nil, "") // Nil service to verify parsing up to service call
	req := httptest.NewRequest(http.MethodPost, "/recover", bytes.NewReader(bodyBytes))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	// Because service is nil, it should return 503 after passing payload validation
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for nil service after valid payload, got %d", rec.Code)
	}
}

func TestHandler_ValidTableAndIDFormat(t *testing.T) {
	reqBody := RecoverRequest{
		Table: "KABUPATEN",
		ID:    "3201",
		Data: map[string]interface{}{
			"NAMA":   "KABUPATEN BOGOR",
			"STATUS": "AKTIF",
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	handler := NewHandler(nil, "")
	req := httptest.NewRequest(http.MethodPost, "/recover", bytes.NewReader(bodyBytes))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for nil service after valid payload, got %d", rec.Code)
	}
}
