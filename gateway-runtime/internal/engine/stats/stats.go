package stats

import (
	"go-blockchain-api/internal/models"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RecordLogStats updates the dashboard stats when a new log is received.
func RecordLogStats(db *gorm.DB, clientID string, action string) error {
	today := time.Now().Truncate(24 * time.Hour)

	return db.Transaction(func(tx *gorm.DB) error {
		var stat models.ClientDashboardStats
		err := tx.Where("client_id = ?", clientID).First(&stat).Error
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				stat = models.ClientDashboardStats{
					ClientID:      clientID,
					LogsTodayDate: &today,
				}
				if err := tx.Create(&stat).Error; err != nil {
					return err
				}
			} else {
				return err
			}
		}

		if stat.LogsTodayDate == nil || !stat.LogsTodayDate.Equal(today) {
			stat.LogsTodayDate = &today
			stat.LogsToday = 0
		}

		now := time.Now()
		updates := map[string]interface{}{
			"total_logs":      gorm.Expr("total_logs + 1"),
			"logs_today":      gorm.Expr("logs_today + 1"),
			"logs_today_date": today,
			"last_log_at":     now,
			"total_pending":   gorm.Expr("total_pending + 1"),
		}

		switch action {
		case "INSERT":
			updates["total_inserts"] = gorm.Expr("total_inserts + 1")
		case "UPDATE":
			updates["total_updates"] = gorm.Expr("total_updates + 1")
		case "DELETE":
			updates["total_deletes"] = gorm.Expr("total_deletes + 1")
		}

		return tx.Model(&stat).Updates(updates).Error
	})
}

// RecordAnchorStats updates the blockchain stats when a log is anchored.
func RecordAnchorStats(db *gorm.DB, clientID string, count int) error {
	if count <= 0 {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var stat models.ClientDashboardStats
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("client_id = ?", clientID).First(&stat).Error
		if err != nil {
			return err
		}

		newAnchored := stat.TotalAnchored + int64(count)
		newPending := stat.TotalPending - int64(count)
		if newPending < 0 {
			newPending = 0
		}

		var pct float64
		if stat.TotalLogs > 0 {
			pct = float64(newAnchored) / float64(stat.TotalLogs) * 100
		}

		return tx.Model(&stat).Updates(map[string]interface{}{
			"total_anchored":    newAnchored,
			"total_pending":     newPending,
			"anchor_percentage": pct,
		}).Error
	})
}
