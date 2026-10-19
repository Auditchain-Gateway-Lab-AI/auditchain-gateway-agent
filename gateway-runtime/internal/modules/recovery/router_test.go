package recovery

import (
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRegisterRoutesUsesClientAuthForRecoveryExecution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("RECOVERY_ENABLED", "true")

	router := gin.New()
	RegisterRoutes(router.Group("/api"), &Handler{})

	routes := make(map[string]bool)
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}

	if !routes["POST /api/dashboard/recovery/requests/:id/execute"] {
		t.Fatal("client recovery execute route is not registered")
	}
	if !routes["GET /api/dashboard/recovery/events"] || !routes["GET /api/dashboard/recovery/events/:id/verify"] {
		t.Fatal("recovery event read/verify routes are not registered")
	}
	if routes["POST /api/dashboard/recovery/requests/:id/approve"] {
		t.Fatal("platform-admin approval route must not be registered")
	}
	if routes["POST /api/dashboard/recovery/requests/:id/reject"] {
		t.Fatal("platform-admin reject route must not be registered")
	}
}
