package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestHealthzReturnsOKWithoutDependencies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterHealthRoutes(router, nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected healthz status %d, got %d", http.StatusOK, res.Code)
	}
	if got := res.Body.String(); got != `{"status":"ok"}` {
		t.Fatalf("unexpected healthz response: %s", got)
	}
}

func TestReadyzReturnsServiceUnavailableWithoutDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterHealthRoutes(router, nil)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected readyz status %d, got %d", http.StatusServiceUnavailable, res.Code)
	}
	if got := res.Body.String(); got != `{"status":"not_ready"}` {
		t.Fatalf("unexpected readyz response: %s", got)
	}
}
