package auth

import (
	"go-blockchain-api/internal/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterRoutes(routerGroup *gin.RouterGroup, h *Handler) {
	authRoutes := routerGroup.Group("/auth")
	{
		authRoutes.POST("/register", h.Register)
		authRoutes.POST("/login", h.Login)
		authRoutes.GET("/me", middleware.JWTAuth(), h.GetProfile)
		authRoutes.PUT("/me", middleware.JWTAuth(), h.UpdateProfile)
	}
}
