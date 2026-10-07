package client

import (
	"go-blockchain-api/internal/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterRoutes(routerGroup *gin.RouterGroup, h *Handler) {
	adminRoutes := routerGroup.Group("/admin", middleware.AdminAuth())
	{
		adminRoutes.POST("/clients", h.CreateClient)
		adminRoutes.POST("/clients/:id/regenerate-api-key", h.RegenerateAPIKey)
		adminRoutes.GET("/clients", h.ListClients)
		adminRoutes.POST("/kafka-config", h.CreateKafkaConfig)
		adminRoutes.GET("/kafka-configs", h.ListKafkaConfigs)
		adminRoutes.PATCH("/kafka-config/:id/toggle", h.ToggleKafkaConfig)
		adminRoutes.GET("/summary", h.GetDashboardSummary)
		adminRoutes.DELETE("/kafka-config/:id", h.DeleteKafkaConfig)
		adminRoutes.PATCH("/clients/:id/toggle", h.ToggleClientStatus)
		adminRoutes.PUT("/clients/:id", h.UpdateClient)
		adminRoutes.GET("/clients/:id/detail", h.GetClientDetail)
		adminRoutes.DELETE("/clients/:id", h.DeleteClient)
		adminRoutes.GET("/users", h.ListUsers)
		adminRoutes.POST("/users", h.CreateUser)
		adminRoutes.GET("/clients/:id/users", h.GetClientUsers)
		adminRoutes.POST("/clients/:id/users", h.CreateClientUser)
		adminRoutes.PUT("/clients/:id/user-table-config", h.UpdateUserTableConfig)
		adminRoutes.DELETE("/users/:id", h.DeleteClientUser)
		adminRoutes.GET("/client-cdc-users", h.GetClientUsersCDC)

		// Agent Config Routes
		adminRoutes.POST("/clients/:id/agent-config", h.CreateAgentConfig)
		adminRoutes.GET("/clients/:id/agent-config", h.GetAgentConfig)
		adminRoutes.DELETE("/clients/:id/agent-config", h.DeleteAgentConfig)
		adminRoutes.GET("/clients/:id/agent-ping", h.PingAgentConfig)
	}

	// Dashboard Routes (Untuk Client yang login)
	dashRoutes := routerGroup.Group("/dashboard", middleware.JWTAuth())
	{
		dashRoutes.GET("/my-users", h.GetMyUsersCDC)
		dashRoutes.GET("/actor-config", h.GetActorConfig)
		dashRoutes.PATCH("/actor-config", h.UpdateActorConfig)
		dashRoutes.GET("/stats", h.GetClientStats)
	}

	// Public Telemetry & Installer Routes (dapat diakses oleh install.sh)
	routerGroup.POST("/agent/telemetry", h.ProcessTelemetry)
	routerGroup.POST("/agent/tailscale-key", h.GenerateTailscaleKey)
	routerGroup.GET("/install.sh", h.ServeInstallScript)
}
