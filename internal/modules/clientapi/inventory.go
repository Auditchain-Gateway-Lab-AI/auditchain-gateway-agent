package clientapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type inventoryRow struct {
	ID            uint      `json:"id"`
	ClientID      string    `json:"client_id"`
	TableName     string    `json:"table_name"`
	RowCount      int64     `json:"row_count"`
	LastAction    string    `json:"last_action"`
	LastActor     string    `json:"last_actor"`
	LastUpdatedAt time.Time `json:"last_updated_at"`
	CreatedAt     time.Time `json:"created_at"`
	Resource      string    `gorm:"-" json:"resource,omitempty"`
}

// RegisterInventory exposes the tenant-scoped read endpoint used by the
// Client Portal watchlist. It performs no legacy backfill writes on Mini PC.
func RegisterInventory(api *gin.RouterGroup, db *gorm.DB, jwtSecret []byte) {
	api.GET("/dashboard/inventory", requireClient(jwtSecret), func(c *gin.Context) {
		clientID := c.GetString("client_id")
		rows := make([]inventoryRow, 0)
		if err := db.Table("client_tables").Where("client_id = ?", clientID).
			Order("table_name ASC").Find(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal memuat daftar data"})
			return
		}
		if len(rows) == 0 {
			// Match the Besu legacy fallback without INSERT/DDL from Mini PC.
			if err := db.Raw(`
				SELECT client_id, SPLIT_PART(resource, ':', 1) AS table_name,
					GREATEST(COUNT(*) FILTER (WHERE action = 'INSERT') -
						COUNT(*) FILTER (WHERE action = 'DELETE'), 0) AS row_count,
					(ARRAY_AGG(action ORDER BY timestamp DESC))[1] AS last_action,
					(ARRAY_AGG(actor ORDER BY timestamp DESC))[1] AS last_actor,
					MAX(timestamp) AS last_updated_at
				FROM audit_logs
				WHERE client_id = ? AND COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY'
					AND resource IS NOT NULL AND resource <> ''
				GROUP BY client_id, SPLIT_PART(resource, ':', 1)
				ORDER BY table_name ASC`, clientID).Scan(&rows).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal memuat daftar data"})
				return
			}
		}
		for i := range rows {
			rows[i].Resource = rows[i].TableName
		}
		c.JSON(http.StatusOK, rows)
	})
}
