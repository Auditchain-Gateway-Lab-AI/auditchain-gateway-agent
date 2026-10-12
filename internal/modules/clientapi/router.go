package clientapi

import (
	"errors"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// RegisterRoutes wires the client-only API subset. It intentionally never
// registers /api/admin, Agent setup, verification, or recovery endpoints.
func RegisterRoutes(api *gin.RouterGroup, db *gorm.DB, jwtSecret []byte) error {
	if db == nil {
		return errors.New("client API requires a PostgreSQL connection")
	}
	if len(jwtSecret) == 0 {
		return errors.New("client API requires JWT_SECRET")
	}
	RegisterAuth(api, db, jwtSecret)
	RegisterStats(api, db, jwtSecret)
	RegisterInventory(api, db, jwtSecret)
	return nil
}
