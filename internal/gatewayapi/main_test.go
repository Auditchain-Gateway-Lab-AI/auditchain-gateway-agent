package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestPreviewRequiresExplicitEnable(t *testing.T) {
	t.Setenv("GATEWAY_CLIENT_API_PREVIEW_ENABLED", "false")
	if err := run(); err == nil {
		t.Fatal("API preview started without opt-in")
	}
}

func TestPreviewAddressMustBeLoopback(t *testing.T) {
	for _, address := range []string{"", ":8080", "0.0.0.0:8080", "100.87.214.4:8080", "127.0.0.1:0"} {
		if err := validatePreviewAddress(address); err == nil {
			t.Errorf("accepted unsafe preview bind %q", address)
		}
	}
	for _, address := range []string{"127.0.0.1:18081", "localhost:18081", "[::1]:18081"} {
		if err := validatePreviewAddress(address); err != nil {
			t.Errorf("rejected loopback bind %q: %v", address, err)
		}
	}
}

func TestPreviewRoutesDoNotExposeAdminOrUnimplementedAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router, err := newRouter(&gorm.DB{}, []byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/admin/clients", "/api/auth/register",
		"/api/dashboard/verify-range/client", "/api/dashboard/recovery/incidents",
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("%s returned %d, want 404", path, response.Code)
		}
	}
	for _, path := range []string{"/api/auth/me", "/api/dashboard/stats", "/api/dashboard/inventory"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s returned %d, want 401", path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Errorf("/healthz returned %d, want 200", response.Code)
	}
}
