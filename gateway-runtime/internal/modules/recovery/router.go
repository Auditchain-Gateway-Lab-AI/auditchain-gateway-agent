package recovery

import (
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"go-blockchain-api/internal/middleware"
)

func RegisterRoutes(routerGroup *gin.RouterGroup, h *Handler) {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("RECOVERY_ENABLED")), "true") {
		return
	}

	readRoutes := routerGroup.Group("/dashboard/recovery", middleware.JWTAuth())
	{
		readRoutes.GET("/incidents", h.ListIncidents)
		readRoutes.GET("/incidents/:id", h.GetIncident)
		readRoutes.GET("/incidents/:id/candidates", h.ListCandidates)
		readRoutes.POST("/incidents/:id/preflight", h.Preflight)
		readRoutes.GET("/resources/:resource/versions", h.ListVersions)
		readRoutes.GET("/requests", h.ListRequests)
		readRoutes.GET("/requests/:id", h.GetRequest)
		readRoutes.GET("/events", h.ListEvents)
		readRoutes.GET("/events/:id", h.GetEvent)
		readRoutes.GET("/events/:id/verify", h.VerifyEvent)
		readRoutes.POST("/requests", h.CreateRequest)
	}

	// Recovery is self-service for an authenticated client user. The JWT
	// client_id is enforced by Handler.clientID, so a user cannot operate on a
	// different tenant by supplying a query parameter. Platform administrators
	// are intentionally not part of this workflow.
	clientRecoveryRoutes := routerGroup.Group("/dashboard/recovery", middleware.JWTAuth())
	{
		clientRecoveryRoutes.POST("/requests/:id/execute", h.Execute)
	}
}
