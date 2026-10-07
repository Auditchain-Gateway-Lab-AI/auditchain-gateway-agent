package client

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

type DashboardTrendResponse struct {
	Range         string                        `json:"range" example:"8H"`
	From          time.Time                     `json:"from"`
	To            time.Time                     `json:"to"`
	BucketSeconds int64                         `json:"bucket_seconds" example:"3600"`
	Points        []DashboardTrendPointResponse `json:"points"`
}

type DashboardTrendPointResponse struct {
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

type dashboardTrendWindow struct {
	name      string
	duration  time.Duration
	bucketQty int
}

func parseDashboardTrendRange(value string) (dashboardTrendWindow, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "8H":
		return dashboardTrendWindow{name: "8H", duration: 8 * time.Hour, bucketQty: 8}, true
	case "24H":
		return dashboardTrendWindow{name: "24H", duration: 24 * time.Hour, bucketQty: 12}, true
	case "7D":
		return dashboardTrendWindow{name: "7D", duration: 7 * 24 * time.Hour, bucketQty: 7}, true
	case "30D":
		return dashboardTrendWindow{name: "30D", duration: 30 * 24 * time.Hour, bucketQty: 10}, true
	default:
		return dashboardTrendWindow{}, false
	}
}

type dashboardTrendAggregate struct {
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

func loadDashboardTrend(db *gorm.DB, clientID string, window dashboardTrendWindow, to time.Time) (*DashboardTrendResponse, error) {
	from := to.Add(-window.duration)
	bucketDuration := window.duration / time.Duration(window.bucketQty)
	bucketSeconds := int64(bucketDuration.Seconds())

	var aggregates []dashboardTrendAggregate
	err := db.Raw(`
		SELECT
			LEAST(?, GREATEST(0, FLOOR(EXTRACT(EPOCH FROM (timestamp - ?)) / ?)::int)) AS bucket_index,
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
		WHERE client_id = ?
		  AND timestamp >= ?
		  AND timestamp < ?
		  AND COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY'
		GROUP BY bucket_index
		ORDER BY bucket_index
	`, window.bucketQty-1, from, bucketSeconds, clientID, from, to).Scan(&aggregates).Error
	if err != nil {
		return nil, err
	}

	return buildDashboardTrendResponse(window, from, to, bucketDuration, bucketSeconds, aggregates), nil
}

func buildDashboardTrendResponse(window dashboardTrendWindow, from, to time.Time, bucketDuration time.Duration, bucketSeconds int64, aggregates []dashboardTrendAggregate) *DashboardTrendResponse {
	points := make([]DashboardTrendPointResponse, window.bucketQty)
	for index := range points {
		points[index].BucketStart = from.Add(time.Duration(index) * bucketDuration)
	}
	for _, aggregate := range aggregates {
		if aggregate.BucketIndex < 0 || aggregate.BucketIndex >= len(points) {
			continue
		}
		points[aggregate.BucketIndex] = DashboardTrendPointResponse{
			BucketStart: points[aggregate.BucketIndex].BucketStart,
			TotalLogs:   aggregate.TotalLogs,
			Valid:       aggregate.Valid,
			Tampered:    aggregate.Tampered,
			Pending:     aggregate.Pending,
			Unavailable: aggregate.Unavailable,
			NotChecked:  aggregate.NotChecked,
			Insert:      aggregate.Insert,
			Update:      aggregate.Update,
			Delete:      aggregate.Delete,
		}
	}

	return &DashboardTrendResponse{
		Range:         window.name,
		From:          from,
		To:            to,
		BucketSeconds: bucketSeconds,
		Points:        points,
	}
}
