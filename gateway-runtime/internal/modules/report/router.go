package report

import (
	"go-blockchain-api/internal/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterRoutes(routerGroup *gin.RouterGroup, h *Handler) {
	dashAPI := routerGroup.Group("/dashboard/reports")
	dashAPI.Use(middleware.JWTAuth())
	{
		dashAPI.POST("/generate", h.GenerateReport)
	}
}
