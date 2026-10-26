package audit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestHandlerAuthorized_SharedJWT(t *testing.T) {
	jwtSecret := "test-secret-key-12345"
	handler := NewHandler(nil, "static-token-fallback", jwtSecret)

	// 1. Generate valid JWT token
	claims := jwt.MapClaims{
		"client_id": "tenant-001",
		"role":      "Auditor",
		"exp":       time.Now().Add(1 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/audit/resource/RUANGAN:1811", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	gotToken, ok := handler.authorized(req)
	if !ok {
		t.Fatalf("expected authorized=true for valid shared JWT token, got false")
	}
	if gotToken != tokenStr {
		t.Fatalf("expected callerToken=%s, got %s", tokenStr, gotToken)
	}

	// 2. Test context forwarding
	ctx := WithCallerToken(context.Background(), gotToken)
	forwarded := CallerTokenFromContext(ctx)
	if forwarded != tokenStr {
		t.Fatalf("expected context forwarded token=%s, got %s", tokenStr, forwarded)
	}

	// 3. Test wrong secret
	wrongToken := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	wrongStr, _ := wrongToken.SignedString([]byte("wrong-secret"))
	reqWrong := httptest.NewRequest(http.MethodGet, "/audit/resource/RUANGAN:1811", nil)
	reqWrong.Header.Set("Authorization", "Bearer "+wrongStr)

	_, okWrong := handler.authorized(reqWrong)
	if okWrong {
		t.Fatalf("expected authorized=false for wrong secret, got true")
	}

	// 4. Test expired token
	expiredClaims := jwt.MapClaims{
		"client_id": "tenant-001",
		"exp":       time.Now().Add(-1 * time.Hour).Unix(),
	}
	expiredToken := jwt.NewWithClaims(jwt.SigningMethodHS256, expiredClaims)
	expiredStr, _ := expiredToken.SignedString([]byte(jwtSecret))
	reqExpired := httptest.NewRequest(http.MethodGet, "/audit/resource/RUANGAN:1811", nil)
	reqExpired.Header.Set("Authorization", "Bearer "+expiredStr)

	_, okExpired := handler.authorized(reqExpired)
	if okExpired {
		t.Fatalf("expected authorized=false for expired token, got true")
	}

	// 5. Test fallback to static token
	reqStatic := httptest.NewRequest(http.MethodGet, "/audit/resource/RUANGAN:1811", nil)
	reqStatic.Header.Set("Authorization", "Bearer static-token-fallback")

	gotStatic, okStatic := handler.authorized(reqStatic)
	if !okStatic || gotStatic != "static-token-fallback" {
		t.Fatalf("expected fallback static token to work, got ok=%v, token=%s", okStatic, gotStatic)
	}
}
