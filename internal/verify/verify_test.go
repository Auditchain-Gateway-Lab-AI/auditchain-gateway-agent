package verify

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsValidSQLIdentifier(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Valid cases
		{"Valid lowercase", "users", true},
		{"Valid uppercase", "USERS", true},
		{"Valid snake_case", "schema_migrations", true},
		{"Valid with numbers", "table_123", true},
		{"Valid start with underscore", "_private_table", true},

		// Invalid cases
		{"Empty string", "", false},
		{"Too long (>63 chars)", "abcdefghijklmnopqrstuvwxyz_abcdefghijklmnopqrstuvwxyz_abcdefghijklmnopqrstuvwxyz", false},
		{"Contains space", "users table", false},
		{"Contains semicolon (SQL injection attempt)", "users; DROP TABLE users;", false},
		{"Contains comment marker", "users--", false},
		{"Contains quotes", "\"users\"", false},
		{"Contains single quotes", "'users'", false},
		{"Contains special characters", "users$", false},
		{"Starts with digit", "123users", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidSQLIdentifier(tt.input)
			if got != tt.expected {
				t.Errorf("IsValidSQLIdentifier(%q) = %v; want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestHandleHealth(t *testing.T) {
	server := NewServer(nil, "test-token", "9090")
	req, err := http.NewRequest(http.MethodGet, "/health", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.handleHealth)

	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	expected := `{"status":"ok"}` + "\n"
	if rr.Body.String() != expected {
		t.Errorf("handler returned unexpected body: got %q want %q", rr.Body.String(), expected)
	}
}

func TestHandleVerifyResource_Auth(t *testing.T) {
	server := NewServer(nil, "secret-token", "9090")

	// 1. Test unauthorized request (missing token)
	req1, err := http.NewRequest(http.MethodGet, "/verify-resource/users/1", nil)
	if err != nil {
		t.Fatal(err)
	}
	rr1 := httptest.NewRecorder()
	handler := http.HandlerFunc(server.handleVerifyResource)
	handler.ServeHTTP(rr1, req1)

	if rr1.Code != http.StatusUnauthorized {
		t.Errorf("expected unauthorized (401), got %v", rr1.Code)
	}

	// 2. Test authorized request (valid token)
	// We use a nil DB, so if auth passes, it will try to hit the DB query resource and return (but since it panics or fails query, we check if it reaches further)
	// Actually, let's catch if it goes past auth. If it goes past auth, it calls queryResource which will try to access s.db. Since s.db is nil, it will panic or fail.
	// But let's verify if wrong token gets unauthorized:
	req2, err := http.NewRequest(http.MethodGet, "/verify-resource/users/1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("Authorization", "Bearer wrong-token")
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusUnauthorized {
		t.Errorf("expected unauthorized (401) for wrong token, got %v", rr2.Code)
	}
}

func TestHandleVerify_Auth(t *testing.T) {
	server := NewServer(nil, "secret-token", "9090")

	// 1. Test unauthorized request (missing token)
	req1, err := http.NewRequest(http.MethodGet, "/verify/users/1", nil)
	if err != nil {
		t.Fatal(err)
	}
	rr1 := httptest.NewRecorder()
	handler := http.HandlerFunc(server.handleVerify)
	handler.ServeHTTP(rr1, req1)

	if rr1.Code != http.StatusUnauthorized {
		t.Errorf("expected unauthorized (401), got %v", rr1.Code)
	}

	// 2. Test authorized request but bad URL format (missing ID)
	req2, err := http.NewRequest(http.MethodGet, "/verify/users/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("Authorization", "Bearer secret-token")
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusBadRequest {
		t.Errorf("expected bad request (400), got %v", rr2.Code)
	}
}

func TestHandleVerify_DatabaseFailureIsNotFoundFalse(t *testing.T) {
	server := NewServer(nil, "secret-token", "9090")
	req, err := http.NewRequest(http.MethodGet, "/verify/users/1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	rr := httptest.NewRecorder()
	server.handleVerify(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected database_unreachable 503, got %d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() == "{\"found\":false}" {
		t.Fatal("database failure must not be reported as a missing row")
	}
}

func TestHandleVerifyAuditRequiresReadToken(t *testing.T) {
	server := NewServer(nil, "secret-token", "9090")
	request := httptest.NewRequest(http.MethodGet, "/verify-audit/620", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", response.Code)
	}
}

func TestSetAuditTrailProjectionNormalizesOracleNames(t *testing.T) {
	server := NewServer(nil, "secret-token", "9090")
	server.SetAuditTrailProjection(AuditTrailProjection{Schema: " simrs ", Table: " audit_trail "})
	if server.auditTrailProjection.Schema != "SIMRS" || server.auditTrailProjection.Table != "AUDIT_TRAIL" {
		t.Fatalf("projection = %+v, want uppercase Oracle identifiers", server.auditTrailProjection)
	}
}

func TestHandleVerifyAuditValidatesRecordIDAndReportsDatabaseUnavailable(t *testing.T) {
	server := NewServer(nil, "secret-token", "9090")
	badPath := httptest.NewRequest(http.MethodGet, "/verify-audit/", nil)
	badPath.Header.Set("Authorization", "Bearer secret-token")
	badResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(badResponse, badPath)
	if badResponse.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request for empty source ID, got %d", badResponse.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/verify-audit/620", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected database unavailable, got %d body=%s", response.Code, response.Body.String())
	}
}

func TestHandleLookupAuditTrailValidatesBoundedQuery(t *testing.T) {
	server := NewServer(nil, "secret-token", "9090")

	badWindow := httptest.NewRequest(http.MethodGet, "/verify-audit-lookup?table=RUANGAN&operation=DELETE&primary_key=ID&record_id=613&at=2026-10-08T03%3A00%3A00Z&window_seconds=301", nil)
	badWindow.Header.Set("Authorization", "Bearer secret-token")
	badResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(badResponse, badWindow)
	if badResponse.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request for oversized lookup window, got %d", badResponse.Code)
	}

	valid := httptest.NewRequest(http.MethodGet, "/verify-audit-lookup?table=RUANGAN&operation=DELETE&primary_key=ID&record_id=613&at=2026-10-08T03%3A00%3A00Z", nil)
	valid.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, valid)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected database unavailable after valid query, got %d body=%s", response.Code, response.Body.String())
	}
}

func TestDecodeAuditImage(t *testing.T) {
	image, err := decodeAuditImage(sql.NullString{String: `{"ID":620,"nama":"ruangan"}`, Valid: true})
	if err != nil {
		t.Fatalf("decodeAuditImage() error = %v", err)
	}
	if image["ID"].(json.Number).String() != "620" || image["nama"] != "ruangan" {
		t.Fatalf("decoded image = %#v", image)
	}
}

func TestAuditTrailRecordMatchesConfiguredPrimaryKey(t *testing.T) {
	record := AuditTrailRecord{DataLama: map[string]interface{}{"ROOM_ID": json.Number("613")}}
	if !auditTrailRecordMatchesKey(record, "room_id", "613") {
		t.Fatal("expected key match to ignore identifier casing")
	}
	if auditTrailRecordMatchesKey(record, "room_id", "614") {
		t.Fatal("unexpected source record key match")
	}
}

func TestTableEndpointDisabledByDefault(t *testing.T) {
	server := NewServer(nil, "", "9090")
	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/table/users", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected disabled table endpoint 404, got %d", rr.Code)
	}
}
