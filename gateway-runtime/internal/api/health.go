package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const readinessDatabaseTimeout = 2 * time.Second

// RegisterHealthRoutes exposes unauthenticated endpoints for container and
// deployment probes. The handlers intentionally return no dependency details
// so credentials, DSNs, and internal topology cannot leak through a probe.
func RegisterHealthRoutes(router *gin.Engine, db *gorm.DB) {
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	router.GET("/readyz", func(c *gin.Context) {
		if err := checkDatabaseReadiness(c.Request.Context(), db); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})
}

func checkDatabaseReadiness(parent context.Context, db *gorm.DB) error {
	if db == nil {
		return gorm.ErrInvalidDB
	}

	sqlDB, err := db.DB()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(parent, readinessDatabaseTimeout)
	defer cancel()

	return sqlDB.PingContext(ctx)
}
