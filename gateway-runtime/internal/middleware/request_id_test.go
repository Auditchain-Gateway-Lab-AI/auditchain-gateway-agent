package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestIDPreservesSafeCallerID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"request_id": GetRequestID(c)})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(RequestIDHeader, "client-request-123")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if got := recorder.Header().Get(RequestIDHeader); got != "client-request-123" {
		t.Fatalf("response request ID = %q, want caller ID", got)
	}
}

func TestRequestIDReplacesUnsafeCallerID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID())
	router.GET("/test", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(RequestIDHeader, "unsafe id\nsecond-line")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	got := recorder.Header().Get(RequestIDHeader)
	if got == "" || got == "unsafe id\nsecond-line" || !safeRequestID.MatchString(got) {
		t.Fatalf("response request ID = %q, want generated safe ID", got)
	}
}
