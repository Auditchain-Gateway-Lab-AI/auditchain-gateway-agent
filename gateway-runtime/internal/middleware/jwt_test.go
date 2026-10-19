package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func signedTestToken(t *testing.T, role string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"role":      role,
		"client_id": "client-1",
		"user_id":   "user-1",
		"exp":       time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("jwt-test-secret"))
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	return signed
}

func TestAdminAuthRejectsNonAdmin(t *testing.T) {
	t.Setenv("JWT_SECRET", "jwt-test-secret")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/recovery", AdminAuth(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/recovery", nil)
	req.Header.Set("Authorization", "Bearer "+signedTestToken(t, "auditor"))
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusForbidden)
	}
}

func TestAdminAuthAcceptsAdmin(t *testing.T) {
	t.Setenv("JWT_SECRET", "jwt-test-secret")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/recovery", AdminAuth(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/recovery", nil)
	req.Header.Set("Authorization", "Bearer "+signedTestToken(t, "admin"))
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusNoContent)
	}
}
