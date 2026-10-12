package clientapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"auditchain-agent/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type trendWindow struct {
	name      string
	duration  time.Duration
	bucketQty int
}

func parseTrendRange(value string) (trendWindow, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "8H":
		return trendWindow{"8H", 8 * time.Hour, 8}, true
	case "24H":
		return trendWindow{"24H", 24 * time.Hour, 12}, true
	case "7D":
		return trendWindow{"7D", 7 * 24 * time.Hour, 7}, true
	case "30D":
		return trendWindow{"30D", 30 * 24 * time.Hour, 10}, true
	default:
		return trendWindow{}, false
	}
}

type trendPoint struct {
	BucketStart time.Time `json:"bucket_start"`
	TotalLogs   int64     `json:"total_logs"`
	Valid       int64     `json:"valid"`
	Tampered    int64     `json:"tampered"`
	Pending     int64     `json:"pending"`
	Unavailable int64     `json:"unavailable"`
	NotChecked  int64     `json:"not_checked"`
	Insert      int64     `json:"insert"`
	Update      int64     `json:"update"`
	Delete      int64     `json:"delete"`
}

type trendAggregate struct {
	BucketIndex int   `gorm:"column:bucket_index"`
	TotalLogs   int64 `gorm:"column:total_logs"`
	Valid       int64 `gorm:"column:valid"`
	Tampered    int64 `gorm:"column:tampered"`
	Pending     int64 `gorm:"column:pending"`
	Unavailable int64 `gorm:"column:unavailable"`
	NotChecked  int64 `gorm:"column:not_checked"`
	Insert      int64 `gorm:"column:insert_count"`
	Update      int64 `gorm:"column:update_count"`
	Delete      int64 `gorm:"column:delete_count"`
}

// RegisterStats serves only the tenant's existing dashboard statistics.
func RegisterStats(api *gin.RouterGroup, db *gorm.DB, jwtSecret []byte) {
	api.GET("/dashboard/stats", requireClient(jwtSecret), func(c *gin.Context) { stats(c, db) })
}

func stats(c *gin.Context, db *gorm.DB) {
	clientID := c.GetString("client_id")
	trendRange := strings.TrimSpace(c.Query("trend_range"))
	var window trendWindow
	if trendRange != "" {
		var valid bool
		window, valid = parseTrendRange(trendRange)
		if !valid {
			c.JSON(http.StatusBadRequest, gin.H{"error": "trend_range harus 8H, 24H, 7D, atau 30D"})
			return
		}
	}
	var counts struct {
		TotalLogs    int64 `gorm:"column:total_logs"`
		AnchoredLogs int64 `gorm:"column:anchored_logs"`
		PendingLogs  int64 `gorm:"column:pending_logs"`
	}
	if err := db.Raw(`
		SELECT COUNT(*) AS total_logs,
			COUNT(*) FILTER (WHERE status = 'ANCHORED') AS anchored_logs,
			COUNT(*) FILTER (WHERE status IN ('RECEIVED', 'HASHED', 'AGGREGATED')) AS pending_logs
		FROM audit_logs WHERE client_id = ?
			AND COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY'
	`, clientID).Scan(&counts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal menghitung statistik audit"})
		return
	}
	var cached models.ClientDashboardStats
	if err := db.Where("client_id = ?", clientID).First(&cached).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal mengambil data statistik"})
		return
	}
	if cached.ClientID == "" {
		cached.ClientID = clientID
	}
	monitoredTables := cached.TotalTablesAudited
	var tableCount int64
	if err := db.Table("client_tables").Where("client_id = ?", clientID).Count(&tableCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal menghitung tabel client"})
		return
	}
	if tableCount == 0 && counts.TotalLogs > 0 {
		if err := db.Raw(`
			SELECT COUNT(DISTINCT NULLIF(SPLIT_PART(resource, ':', 1), ''))
			FROM audit_logs WHERE client_id = ?
				AND COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY'
		`, clientID).Scan(&tableCount).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal menghitung tabel audit"})
			return
		}
	}
	if tableCount > 0 {
		monitoredTables = int(tableCount)
	}
	anchorPercentage := 0.0
	if counts.TotalLogs > 0 {
		anchorPercentage = float64(counts.AnchoredLogs) / float64(counts.TotalLogs) * 100
	}
	var tableResults any
	if cached.TableVerifyResults != "" {
		_ = json.Unmarshal([]byte(cached.TableVerifyResults), &tableResults)
	}
	data := gin.H{
		"id": cached.ID, "client_id": cached.ClientID,
		"total_logs": counts.TotalLogs, "logs_today": cached.LogsToday,
		"total_tables_audited": monitoredTables,
		"total_inserts":        cached.TotalInserts, "total_updates": cached.TotalUpdates,
		"total_deletes": cached.TotalDeletes, "last_log_at": cached.LastLogAt,
		"total_anchored": counts.AnchoredLogs, "anchored_logs": counts.AnchoredLogs,
		"total_pending": counts.PendingLogs, "pending_logs": counts.PendingLogs,
		"anchor_percentage":   anchorPercentage,
		"total_verifications": cached.TotalVerifications,
		"total_rows_verified": cached.TotalRowsVerified,
		"total_valid":         cached.TotalValid, "total_tampered": cached.TotalTampered,
		"total_verify_pending": cached.TotalVerifyPending,
		"total_agent_error":    cached.TotalAgentError,
		"total_fabric_error":   cached.TotalFabricError,
		"integrity_score":      cached.IntegrityScore,
		"last_verified_at":     cached.LastVerifiedAt,
		"last_verified_table":  cached.LastVerifiedTable,
		"integrity_check": gin.H{
			"source": cached.LastCheckSource, "checked_at": cached.LastCheckAt,
			"run_id": cached.LastCheckRunID, "checked_logs": cached.LastCheckLogs,
		},
		"table_verify_results": tableResults,
		"created_at":           cached.CreatedAt, "updated_at": cached.UpdatedAt,
	}
	if trendRange != "" {
		trend, err := loadTrend(db, clientID, window, time.Now().UTC())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal menghitung trend dashboard"})
			return
		}
		data["trend"] = trend
	}
	c.JSON(http.StatusOK, gin.H{
		"message":    "Berhasil mengambil statistik",
		"total_logs": counts.TotalLogs, "pending_logs": counts.PendingLogs,
		"anchored_logs": counts.AnchoredLogs, "data": data,
	})
}

func loadTrend(db *gorm.DB, clientID string, window trendWindow, to time.Time) (gin.H, error) {
	from := to.Add(-window.duration)
	bucketDuration := window.duration / time.Duration(window.bucketQty)
	bucketSeconds := int64(bucketDuration.Seconds())
	var aggregates []trendAggregate
	if err := db.Raw(`
		SELECT LEAST(?, GREATEST(0, FLOOR(EXTRACT(EPOCH FROM (timestamp - ?)) / ?)::int)) AS bucket_index,
			COUNT(*) AS total_logs,
			COUNT(*) FILTER (WHERE UPPER(TRIM(COALESCE(integrity_status, ''))) = 'VALID') AS valid,
			COUNT(*) FILTER (WHERE UPPER(TRIM(COALESCE(integrity_status, ''))) = 'TAMPERED') AS tampered,
			COUNT(*) FILTER (WHERE UPPER(TRIM(COALESCE(integrity_status, ''))) = 'PENDING') AS pending,
			COUNT(*) FILTER (WHERE UPPER(TRIM(COALESCE(integrity_status, ''))) IN ('UNREACHABLE', 'UNAVAILABLE', 'AGENT_ERROR', 'FABRIC_ERROR')) AS unavailable,
			COUNT(*) FILTER (WHERE UPPER(TRIM(COALESCE(integrity_status, ''))) IN ('', 'NOT_CHECKED')) AS not_checked,
			COUNT(*) FILTER (WHERE UPPER(TRIM(COALESCE(action, ''))) = 'INSERT') AS insert_count,
			COUNT(*) FILTER (WHERE UPPER(TRIM(COALESCE(action, ''))) = 'UPDATE') AS update_count,
			COUNT(*) FILTER (WHERE UPPER(TRIM(COALESCE(action, ''))) = 'DELETE') AS delete_count
		FROM audit_logs
		WHERE client_id = ? AND timestamp >= ? AND timestamp < ?
			AND COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY'
		GROUP BY bucket_index ORDER BY bucket_index
	`, window.bucketQty-1, from, bucketSeconds, clientID, from, to).Scan(&aggregates).Error; err != nil {
		return nil, err
	}
	points := make([]trendPoint, window.bucketQty)
	for i := range points {
		points[i].BucketStart = from.Add(time.Duration(i) * bucketDuration)
	}
	for _, aggregate := range aggregates {
		if aggregate.BucketIndex < 0 || aggregate.BucketIndex >= len(points) {
			continue
		}
		points[aggregate.BucketIndex] = trendPoint{
			BucketStart: points[aggregate.BucketIndex].BucketStart,
			TotalLogs:   aggregate.TotalLogs, Valid: aggregate.Valid,
			Tampered: aggregate.Tampered, Pending: aggregate.Pending,
			Unavailable: aggregate.Unavailable, NotChecked: aggregate.NotChecked,
			Insert: aggregate.Insert, Update: aggregate.Update, Delete: aggregate.Delete,
		}
	}
	return gin.H{
		"range": window.name, "from": from, "to": to,
		"bucket_seconds": bucketSeconds, "points": points,
	}, nil
}
