package audit

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type verifyContractServiceStub struct {
	Service
	result *VerificationResult
	err    error
}

func (s verifyContractServiceStub) VerifyLogIntegrity(string, string) (*VerificationResult, error) {
	return s.result, s.err
}

func invokeVerifyLogHandler(t *testing.T, result *VerificationResult, err error) (int, map[string]interface{}) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "log_id", Value: "1791354247037786430"}}
	ctx.Set("client_id", "client-1")

	handler := NewHandler(verifyContractServiceStub{result: result, err: err})
	handler.VerifyLog(ctx)

	var payload map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response body %q: %v", recorder.Body.String(), err)
	}
	return recorder.Code, payload
}

func TestVerifyLogResponseIncludesPersistedIncidentReference(t *testing.T) {
	status, payload := invokeVerifyLogHandler(t, &VerificationResult{
		Status:         "failed_local",
		Message:        "tampered",
		LogID:          "1791354247037786430",
		IncidentID:     "incident-620",
		IncidentScope:  "GATEWAY_INTEGRITY",
		IncidentType:   "METADATA_HASH_MISMATCH",
		IncidentStatus: "OPEN",
	}, nil)

	if status != 409 {
		t.Fatalf("HTTP status = %d, want 409", status)
	}
	if payload["code"] != "INTEGRITY_TAMPERED" {
		t.Fatalf("code = %v, want INTEGRITY_TAMPERED", payload["code"])
	}
	data, ok := payload["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data = %T, want object", payload["data"])
	}
	if data["incident_id"] != "incident-620" || data["incident_scope"] != "GATEWAY_INTEGRITY" {
		t.Fatalf("incident reference = (%v, %v), want incident-620/GATEWAY_INTEGRITY", data["incident_id"], data["incident_scope"])
	}
}

func TestVerifyLogResponseSurfacesIncidentPersistenceFailure(t *testing.T) {
	serviceErr := &IncidentPersistenceError{
		LogID:         "1791354247037786430",
		IncidentType:  "METADATA_HASH_MISMATCH",
		IncidentScope: "GATEWAY_INTEGRITY",
		Cause:         errors.New("database unavailable"),
	}
	status, payload := invokeVerifyLogHandler(t, &VerificationResult{
		Status:  "failed_local",
		Message: "tampered",
		LogID:   "1791354247037786430",
	}, serviceErr)

	if status != 500 {
		t.Fatalf("HTTP status = %d, want 500", status)
	}
	if payload["code"] != "INCIDENT_PERSISTENCE_FAILED" {
		t.Fatalf("code = %v, want INCIDENT_PERSISTENCE_FAILED", payload["code"])
	}
	if payload["log_id"] != "1791354247037786430" {
		t.Fatalf("log_id = %v, want the verified log ID", payload["log_id"])
	}
}
