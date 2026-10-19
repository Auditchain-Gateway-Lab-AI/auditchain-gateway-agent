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
	req := httptest.NewRequest(http.MethodPost, "/recover/KABUPATEN/1", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", rec.Code)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	handler := NewHandler(nil, "")
	req := httptest.NewRequest(http.MethodGet, "/recover/KABUPATEN/1", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 Method Not Allowed, got %d", rec.Code)
	}
}

func TestHandler_MissingData(t *testing.T) {
	handler := NewHandler(&Service{}, "token")
	req := httptest.NewRequest(http.MethodPost, "/recover/KABUPATEN/1", bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", rec.Code)
	}
}

func TestHandler_ParseBody(t *testing.T) {
	body := map[string]interface{}{
		"table": "KABUPATEN",
		"id":    "3201",
		"data": map[string]interface{}{
			"NAMA": "KABUPATEN BOGOR",
		},
	}
	bodyBytes, _ := json.Marshal(body)

	handler := NewHandler(nil, "") // Nil service to verify parsing up to service call
	req := httptest.NewRequest(http.MethodPost, "/recover", bytes.NewReader(bodyBytes))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	// Because service is nil, it should return 503 after passing authorization
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for nil service, got %d", rec.Code)
	}
}
