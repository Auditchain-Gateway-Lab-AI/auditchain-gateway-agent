package api

import (
	"go-blockchain-api/internal/blockchain/agentverifier"
	"go-blockchain-api/internal/middleware"
	"go-blockchain-api/internal/modules/audit"
	"go-blockchain-api/internal/modules/auth"
	"go-blockchain-api/internal/modules/client"
	"go-blockchain-api/internal/modules/recovery"
	"go-blockchain-api/internal/modules/report"
	"os"
	"strings"

	_ "go-blockchain-api/docs"

	"gorm.io/gorm"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

func SetupRouter(
	auditHandler *audit.Handler,
	authHandler *auth.Handler,
	clientHandler *client.Handler,
	agentHandler *agentverifier.Handler,
	reportHandler *report.Handler,
	recoveryHandler *recovery.Handler,
	db *gorm.DB,
) *gin.Engine {
	router := gin.Default()
	router.Use(middleware.RequestID())

	corsConfig := cors.DefaultConfig()

	allowedOrigins := os.Getenv("ALLOWED_ORIGINS")
	if allowedOrigins != "" {
		corsConfig.AllowOrigins = strings.Split(allowedOrigins, ",")
	} else {
		// Fallback for development if not set, but warn
		corsConfig.AllowAllOrigins = true
	}

	corsConfig.AllowMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	corsConfig.AllowHeaders = []string{"Origin", "Content-Length", "Content-Type", "Authorization", middleware.RequestIDHeader}
	corsConfig.ExposeHeaders = []string{middleware.RequestIDHeader}
	router.Use(cors.New(corsConfig))

	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// Sajikan install.sh agar klien bisa mengunduh via:
	// curl -fsSL https://semeru.tailfe278a.ts.net/scripts/install.sh | sudo bash
	router.StaticFile("/scripts/install.sh", "./scripts/install.sh")

	apiGroup := router.Group("/api")

	auth.RegisterRoutes(apiGroup, authHandler)
	client.RegisterRoutes(apiGroup, clientHandler)
	audit.RegisterRoutes(apiGroup, auditHandler)
	report.RegisterRoutes(apiGroup, reportHandler)
	recovery.RegisterRoutes(apiGroup, recoveryHandler)
	agentverifier.RegisterRoutes(apiGroup.Group("/dashboard"), agentHandler)

	return router
}
