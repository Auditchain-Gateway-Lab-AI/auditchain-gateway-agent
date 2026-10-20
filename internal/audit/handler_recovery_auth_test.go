package audit

import (
	"net/http/httptest"
	"testing"
)

func TestAuditHandlerRequiresSeparateRecoveryTokenForAutoRecover(t *testing.T) {
	handler := NewHandler(nil, "verify-token")
	handler.SetRecoveryAuthorization("recovery-token", true)

	request := httptest.NewRequest("POST", "/audit/run", nil)
	if handler.authorizedRecovery(request) {
		t.Fatal("auto-recovery must be denied without the separate write token")
	}
	request.Header.Set("X-Agent-Recovery-Token", "verify-token")
	if handler.authorizedRecovery(request) {
		t.Fatal("verify token must not grant recovery write permission")
	}
	request.Header.Set("X-Agent-Recovery-Token", "recovery-token")
	if !handler.authorizedRecovery(request) {
		t.Fatal("valid distinct recovery token should be accepted")
	}

	handler.SetRecoveryAuthorization("verify-token", true)
	if handler.authorizedRecovery(request) {
		t.Fatal("recovery token must be distinct from verify token")
	}
	handler.SetRecoveryAuthorization("recovery-token", false)
	if handler.authorizedRecovery(request) {
		t.Fatal("disabled recovery must deny auto-recovery")
	}
}
