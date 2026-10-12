package clientapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

func TestClientAuthRoutesHaveNoAdminAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterAuth(router.Group("/api"), nil, []byte("test-secret"))
	routes := router.Routes()
	if len(routes) != 2 {
		t.Fatalf("unexpected Mini PC routes: %#v", routes)
	}
	want := map[string]bool{"POST /api/auth/login": false, "GET /api/auth/me": false}
	for _, route := range routes {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; !ok {
			t.Fatalf("unexpected Mini PC route: %s", key)
		}
		want[key] = true
		if strings.HasPrefix(route.Path, "/api/admin/") {
			t.Fatalf("admin route exposed on Mini PC: %s", route.Path)
		}
	}
	for route, found := range want {
		if !found {
			t.Errorf("missing Mini PC route: %s", route)
		}
	}
}

func TestClientAuthRejectsAdminAndMissingTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := []byte("test-secret")
	router := gin.New()
	RegisterAuth(router.Group("/api"), nil, secret)
	for _, tc := range []struct {
		name, role, clientID string
	}{
		{"admin", "admin", "tenant-1"},
		{"missing tenant", "Auditor", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
				"user_id": "user-1", "client_id": tc.clientID, "role": tc.role,
				"exp": time.Now().Add(time.Hour).Unix(),
			}).SignedString(secret)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", response.Code)
			}
		})
	}
}

func TestInventoryRequiresClientToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterInventory(router.Group("/api"), nil, []byte("test-secret"))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/dashboard/inventory", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
	for _, route := range router.Routes() {
		if route.Method != http.MethodGet || route.Path != "/api/dashboard/inventory" {
			t.Fatalf("unexpected route: %s %s", route.Method, route.Path)
		}
	}
}

func TestStatsRejectsInvalidTrendWithoutDatabaseQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := []byte("test-secret")
	router := gin.New()
	RegisterStats(router.Group("/api"), nil, secret)
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": "user-1", "client_id": "tenant-1", "role": "Auditor",
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/stats?trend_range=unknown", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestSupportedTrendRanges(t *testing.T) {
	for _, value := range []string{"8H", "24H", "7D", "30D"} {
		if _, ok := parseTrendRange(value); !ok {
			t.Errorf("unsupported dashboard range %s", value)
		}
	}
}

func TestCombinedRouterExposesOnlyCurrentClientSubset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	api := router.Group("/api")
	if err := RegisterRoutes(api, nil, []byte("secret")); err == nil {
		t.Fatal("nil database accepted")
	}
	if err := RegisterRoutes(api, &gorm.DB{}, nil); err == nil {
		t.Fatal("empty JWT secret accepted")
	}
	if err := RegisterRoutes(api, &gorm.DB{}, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"POST /api/auth/login":         false,
		"GET /api/auth/me":             false,
		"GET /api/dashboard/stats":     false,
		"GET /api/dashboard/inventory": false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; !ok {
			t.Fatalf("unexpected route: %s", key)
		}
		want[key] = true
	}
	for route, found := range want {
		if !found {
			t.Errorf("route missing: %s", route)
		}
	}
}

func TestClientTenantComesFromSignedTokenNotHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := []byte("test-secret")
	router := gin.New()
	router.GET("/probe", requireClient(secret), func(c *gin.Context) {
		c.String(http.StatusOK, c.GetString("client_id"))
	})
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": "user-1", "client_id": "tenant-A", "role": "Auditor",
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Client-ID", "tenant-B")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK || response.Body.String() != "tenant-A" {
		t.Fatalf("status=%d tenant=%q, want 200 tenant-A", response.Code, response.Body.String())
	}
}
