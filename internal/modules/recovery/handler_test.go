package recovery

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func recoveryTestContext(path string) *gin.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, path, nil)
	return ctx
}

func TestClientIDAlwaysUsesTokenTenant(t *testing.T) {
	h := &Handler{}
	ctx := recoveryTestContext("/api/dashboard/recovery/incidents?client_id=morbis-client")
	ctx.Set("client_id", "admin-default-client")
	ctx.Set("role", "admin")

	clientID, ok := h.clientID(ctx)
	if !ok {
		t.Fatal("expected token tenant to be accepted")
	}
	if clientID != "admin-default-client" {
		t.Fatalf("client_id = %q, want token client_id %q", clientID, "admin-default-client")
	}
}

func TestClientIDUserCannotOverrideTenant(t *testing.T) {
	h := &Handler{}
	ctx := recoveryTestContext("/api/dashboard/recovery/incidents?client_id=other-client")
	ctx.Set("client_id", "user-client")
	ctx.Set("role", "Auditor")

	clientID, ok := h.clientID(ctx)
	if !ok {
		t.Fatal("expected user token client_id to be accepted")
	}
	if clientID != "user-client" {
		t.Fatalf("client_id = %q, want token client_id %q", clientID, "user-client")
	}
}

func TestClientIDRejectsMissingIdentity(t *testing.T) {
	h := &Handler{}
	ctx := recoveryTestContext("/api/dashboard/recovery/incidents")

	if _, ok := h.clientID(ctx); ok {
		t.Fatal("expected missing client identity to be rejected")
	}
	if ctx.Writer.Status() != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", ctx.Writer.Status(), http.StatusUnauthorized)
	}
}

func TestClientOperatorRejectsPlatformAdmin(t *testing.T) {
	h := &Handler{}
	ctx := recoveryTestContext("/api/dashboard/recovery/requests")
	ctx.Set("role", "admin")
	ctx.Set("user_id", "admin-user")
	if h.clientOperator(ctx) {
		t.Fatal("platform admin must not execute client recovery")
	}
	if ctx.Writer.Status() != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", ctx.Writer.Status(), http.StatusForbidden)
	}
}

func TestClientOperatorAcceptsAuditor(t *testing.T) {
	h := &Handler{}
	ctx := recoveryTestContext("/api/dashboard/recovery/requests")
	ctx.Set("role", "Auditor")
	ctx.Set("user_id", "client-user")
	if !h.clientOperator(ctx) {
		t.Fatal("client auditor should be able to execute recovery")
	}
}
