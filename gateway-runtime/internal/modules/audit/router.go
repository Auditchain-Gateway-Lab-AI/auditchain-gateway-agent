package audit

import (
	"go-blockchain-api/internal/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterRoutes(routerGroup *gin.RouterGroup, h *Handler) {
	dashAPI := routerGroup.Group("/dashboard")
	dashAPI.Use(middleware.JWTAuth())
	{
		// dashAPI.GET("/stats", h.GetStats) (Moved to client module)
		dashAPI.GET("/logs", h.GetRecentLogs)
		dashAPI.GET("/logs/by-resource/:resource", h.GetLogsByResource)
		dashAPI.GET("/verify/:log_id", h.VerifyLog)
		dashAPI.GET("/fabric/:anchor_id", h.GetFabricRecord)
		dashAPI.POST("/verify-data", h.VerifyData)
		dashAPI.GET("/inventory", h.GetResourceInventory)
		dashAPI.GET("/verify-resource/:resource", h.VerifyResourceHistory)
		dashAPI.GET("/verify-range/estimate", h.EstimateLogRange)
		dashAPI.GET("/verify-range/internal", h.VerifyInternalLogRange)
		dashAPI.GET("/verify-range/client", h.VerifyClientLogRange)
		dashAPI.POST("/verification-runs", h.CreateVerificationRun)
		dashAPI.GET("/verification-runs/latest", h.GetLatestVerificationRun)
		dashAPI.GET("/verification-runs/:id", h.GetVerificationRun)
		// Compatibility route for older gateway-dashboard bundles. New clients
		// should call /verify-range/internal explicitly.
		dashAPI.GET("/verify-range", h.VerifyLegacyLogRange)
	}
}
