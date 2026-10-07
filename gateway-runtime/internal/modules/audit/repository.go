package audit

import (
	"errors"
	"strings"
	"time"

	"go-blockchain-api/internal/models"

	"gorm.io/gorm"
)

type AuditRepository interface {
	CreateLog(log *models.AuditLog) error
	GetLogByHash(hash, clientID string) (*models.AuditLog, error)
	GetLogByID(logID, clientID string) (*models.AuditLog, error)
	GetProofsByHash(hash string) ([]models.MerkleProof, error)
	GetDashboardStats(clientID string) (map[string]int64, error)
	GetLatestLogByResource(resource, clientID string) (*models.AuditLog, error)
	GetLatestClientLogByResource(resource, clientID string) (*models.AuditLog, error)
	GetClientDBEngine(clientID string) (string, error)

	GetRecentLogsPage(clientID string, page, pageSize int, sortOrder, sourceTable string, fromTime, toTime *time.Time) ([]models.AuditLog, int64, error)
	CountAnchoredLogs(clientID string) (int64, error)
	GetAnchoredLogsPage(clientID string, page, pageSize int) ([]models.AuditLog, error)

	GetResourceInventory(clientID string) ([]models.AuditLog, error)
	GetClientTables(clientID string) ([]models.ClientTable, error)
	UpsertClientTable(clientID, tableName, action, actor string, ts time.Time) error
	GetLogsByResource(resource, clientID string) ([]models.AuditLog, error)
	GetTableResources(tableName, clientID string) ([]models.AuditLog, error)
	CountLogsByTimeRange(from, to time.Time, clientID string) (int64, error)
	GetLogsByTimeRange(from, to time.Time, clientID string) ([]models.AuditLog, error)
	GetLogsByTimeRangePage(from, to time.Time, clientID string, limit, offset int) ([]models.AuditLog, error)
}

type auditRepoImpl struct {
	db *gorm.DB
}

const clientAuditEventPredicate = "COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY'"

func NewAuditRepository(db *gorm.DB) AuditRepository {
	return &auditRepoImpl{db: db}
}

func extractTableName(resource string) string {
	if strings.Contains(resource, ":") {
		return strings.Split(resource, ":")[0]
	}
	return resource
}

func (r *auditRepoImpl) CreateLog(log *models.AuditLog) error {
	log.IsLatest = true

	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.AuditLog{}).
			Where("client_id = ? AND resource = ?", log.ClientID, log.Resource).
			Update("is_latest", false).Error; err != nil {
			return err
		}

		if err := tx.Create(log).Error; err != nil {
			return err
		}

		tableName := extractTableName(log.Resource)
		_ = r.UpsertClientTable(log.ClientID, tableName, log.Action, log.Actor, log.Timestamp)
		return nil
	})
}

func (r *auditRepoImpl) GetLogByHash(hash, clientID string) (*models.AuditLog, error) {
	var log models.AuditLog
	err := r.db.Where("hash_value = ? AND client_id = ?", hash, clientID).First(&log).Error
	return &log, err
}

func (r *auditRepoImpl) GetLogByID(logID, clientID string) (*models.AuditLog, error) {
	var log models.AuditLog
	err := r.db.Where("log_id = ? AND client_id = ?", logID, clientID).First(&log).Error
	return &log, err
}

func (r *auditRepoImpl) GetProofsByHash(hash string) ([]models.MerkleProof, error) {
	var proofs []models.MerkleProof
	err := r.db.Where("transaction_hash = ?", hash).Order("tree_level asc").Find(&proofs).Error
	return proofs, err
}

// GetDashboardStats mengambil total, anchored, dan pending dalam satu query
// menggunakan conditional aggregation — jauh lebih cepat dari tiga COUNT terpisah.
func (r *auditRepoImpl) GetDashboardStats(clientID string) (map[string]int64, error) {
	var result struct {
		Total    int64
		Anchored int64
		Pending  int64
	}

	err := r.db.Raw(`
		SELECT
			COUNT(*)                                                        AS total,
			COUNT(*) FILTER (WHERE status = 'ANCHORED')                    AS anchored,
			COUNT(*) FILTER (WHERE status IN ('RECEIVED','HASHED','AGGREGATED')) AS pending
		FROM audit_logs
		WHERE client_id = ? AND `+clientAuditEventPredicate+`
	`, clientID).Scan(&result).Error

	if err != nil {
		return nil, err
	}

	return map[string]int64{
		"total_logs":    result.Total,
		"anchored_logs": result.Anchored,
		"pending_logs":  result.Pending,
	}, nil
}

func (r *auditRepoImpl) GetLatestLogByResource(resource, clientID string) (*models.AuditLog, error) {
	var log models.AuditLog
	err := r.db.Where("resource = ? AND client_id = ? AND "+clientAuditEventPredicate, resource, clientID).
		Order("timestamp desc").First(&log).Error
	return &log, err
}

// GetLatestClientLogByResource ignores internal RECOVERY events because only
// client-originated events can be compared with the live database via Agent.
func (r *auditRepoImpl) GetLatestClientLogByResource(resource, clientID string) (*models.AuditLog, error) {
	var log models.AuditLog
	err := r.db.Where(
		"resource = ? AND client_id = ? AND COALESCE(UPPER(TRIM(action)), '') <> ?",
		resource,
		clientID,
		"RECOVERY",
	).Order("timestamp desc").First(&log).Error
	return &log, err
}

func (r *auditRepoImpl) GetClientDBEngine(clientID string) (string, error) {
	var kafkaCfg models.ClientKafkaConfig
	err := r.db.Where("client_id = ?", clientID).First(&kafkaCfg).Error
	if err == nil && strings.TrimSpace(kafkaCfg.DBEngine) != "" {
		return kafkaCfg.DBEngine, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}

	var agentCfg models.AgentConfig
	err = r.db.Where("client_id = ?", clientID).First(&agentCfg).Error
	if err == nil && strings.TrimSpace(agentCfg.DBEngine) != "" {
		return agentCfg.DBEngine, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}

	return "", nil
}

// GetRecentLogsPage mengembalikan satu halaman log terbaru (tanpa filter
// integrity_status) beserta total count untuk keperluan pagination di
// dashboard. Mendukung sortOrder (asc/desc), filter sourceTable, serta rentang dari/ke.
func (r *auditRepoImpl) GetRecentLogsPage(clientID string, page, pageSize int, sortOrder, sourceTable string, fromTime, toTime *time.Time) ([]models.AuditLog, int64, error) {
	var logs []models.AuditLog
	var total int64

	countQuery := r.db.Model(&models.AuditLog{}).Where("client_id = ? AND "+clientAuditEventPredicate, clientID)
	if sourceTable != "" {
		countQuery = countQuery.Where("resource LIKE ? OR resource = ?", sourceTable+":%", sourceTable)
	}
	if fromTime != nil && toTime != nil {
		f := *fromTime
		t := *toTime
		if f.After(t) {
			f, t = t, f
		}
		countQuery = countQuery.Where("timestamp >= ? AND timestamp <= ?", f, t)
	}

	if err := countQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	orderClause := "timestamp desc"
	if sortOrder == "asc" {
		orderClause = "timestamp asc"
	}

	offset := (page - 1) * pageSize
	dataQuery := r.db.Where("client_id = ? AND "+clientAuditEventPredicate, clientID)
	if sourceTable != "" {
		dataQuery = dataQuery.Where("resource LIKE ? OR resource = ?", sourceTable+":%", sourceTable)
	}
	if fromTime != nil && toTime != nil {
		f := *fromTime
		t := *toTime
		if f.After(t) {
			f, t = t, f
		}
		dataQuery = dataQuery.Where("timestamp >= ? AND timestamp <= ?", f, t)
	}

	err := dataQuery.
		Order(orderClause).
		Limit(pageSize).
		Offset(offset).
		Find(&logs).Error

	return logs, total, err
}

// CountAnchoredLogs menghitung total log berstatus ANCHORED untuk klien.
// Dipakai sebagai basis perhitungan pagination APPROXIMATE saat filter
// integrity_status aktif (lihat catatan di service.go).
func (r *auditRepoImpl) CountAnchoredLogs(clientID string) (int64, error) {
	var total int64
	err := r.db.Model(&models.AuditLog{}).
		Where("client_id = ? AND status = ? AND "+clientAuditEventPredicate, clientID, "ANCHORED").
		Count(&total).Error
	return total, err
}

// GetAnchoredLogsPage mengambil satu halaman log berstatus ANCHORED saja.
// Dipakai saat integrity_status filter aktif, karena hanya log ANCHORED
// yang bisa diverifikasi penuh sampai Layer 4 (valid/tampered/unreachable);
// log yang masih pending tidak relevan untuk filter ini.
func (r *auditRepoImpl) GetAnchoredLogsPage(clientID string, page, pageSize int) ([]models.AuditLog, error) {
	var logs []models.AuditLog
	offset := (page - 1) * pageSize
	err := r.db.Where("client_id = ? AND status = ? AND "+clientAuditEventPredicate, clientID, "ANCHORED").
		Order("timestamp desc").
		Limit(pageSize).
		Offset(offset).
		Find(&logs).Error
	return logs, err
}

func (r *auditRepoImpl) GetResourceInventory(clientID string) ([]models.AuditLog, error) {
	var logs []models.AuditLog
	err := r.db.Raw(
		"SELECT DISTINCT ON (resource) * FROM audit_logs WHERE client_id = ? AND "+clientAuditEventPredicate+" ORDER BY resource, timestamp DESC",
		clientID,
	).Scan(&logs).Error
	return logs, err
}

func (r *auditRepoImpl) GetLogsByResource(resource, clientID string) ([]models.AuditLog, error) {
	var logs []models.AuditLog
	err := r.db.Where("resource = ? AND client_id = ? AND "+clientAuditEventPredicate, resource, clientID).
		Order("timestamp asc").Find(&logs).Error
	return logs, err
}

func (r *auditRepoImpl) GetTableResources(tableName, clientID string) ([]models.AuditLog, error) {
	var logs []models.AuditLog
	// Fetch the latest log for each resource in the table
	err := r.db.Raw(`
		SELECT DISTINCT ON (resource) * 
		FROM audit_logs 
		WHERE client_id = ? AND `+clientAuditEventPredicate+` AND (resource = ? OR resource LIKE ?)
		ORDER BY resource, timestamp DESC
	`, clientID, tableName, tableName+":%").Scan(&logs).Error
	return logs, err
}

func (r *auditRepoImpl) UpsertClientTable(clientID, tableName, action, actor string, ts time.Time) error {
	if tableName == "" || clientID == "" {
		return nil
	}

	query := `
		INSERT INTO client_tables (client_id, table_name, row_count, last_action, last_actor, last_updated_at, created_at)
		VALUES (?, ?, CASE WHEN ? = 'INSERT' THEN 1 ELSE 0 END, ?, ?, ?, NOW())
		ON CONFLICT (client_id, table_name) DO UPDATE SET
			row_count = CASE 
				WHEN EXCLUDED.last_action = 'INSERT' THEN client_tables.row_count + 1
				WHEN EXCLUDED.last_action = 'DELETE' THEN GREATEST(client_tables.row_count - 1, 0)
				ELSE client_tables.row_count 
			END,
			last_action = EXCLUDED.last_action,
			last_actor = EXCLUDED.last_actor,
			last_updated_at = EXCLUDED.last_updated_at
	`
	return r.db.Exec(query, clientID, tableName, action, action, actor, ts).Error
}

func (r *auditRepoImpl) GetClientTables(clientID string) ([]models.ClientTable, error) {
	var tables []models.ClientTable
	err := r.db.Where("client_id = ?", clientID).Order("table_name ASC").Find(&tables).Error
	if err != nil {
		return nil, err
	}

	// Auto-sync jika client_tables belum terisi untuk client_id ini (data legacy)
	if len(tables) == 0 {
		syncQuery := `
			INSERT INTO client_tables (client_id, table_name, row_count, last_action, last_actor, last_updated_at, created_at)
			SELECT 
				client_id,
				SPLIT_PART(resource, ':', 1) AS table_name,
				GREATEST(COUNT(*) FILTER (WHERE action = 'INSERT') - COUNT(*) FILTER (WHERE action = 'DELETE'), 0) AS row_count,
				(ARRAY_AGG(action ORDER BY timestamp DESC))[1] AS last_action,
				(ARRAY_AGG(actor ORDER BY timestamp DESC))[1] AS last_actor,
				MAX(timestamp) AS last_updated_at,
				NOW() AS created_at
			FROM audit_logs
			WHERE client_id = ? AND ` + clientAuditEventPredicate + ` AND resource IS NOT NULL AND resource != ''
			GROUP BY client_id, SPLIT_PART(resource, ':', 1)
			ON CONFLICT (client_id, table_name) DO NOTHING
		`
		_ = r.db.Exec(syncQuery, clientID)
		r.db.Where("client_id = ?", clientID).Order("table_name ASC").Find(&tables)
	}

	for i := range tables {
		tables[i].Resource = tables[i].TableName
	}

	return tables, nil
}

func (r *auditRepoImpl) GetLogsByTimeRange(from, to time.Time, clientID string) ([]models.AuditLog, error) {
	var logs []models.AuditLog
	err := r.db.Where("client_id = ? AND timestamp BETWEEN ? AND ? AND "+clientAuditEventPredicate, clientID, from, to).
		Order("timestamp asc, log_id asc").Find(&logs).Error
	return logs, err
}

func (r *auditRepoImpl) GetLogsByTimeRangePage(from, to time.Time, clientID string, limit, offset int) ([]models.AuditLog, error) {
	var logs []models.AuditLog
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	err := r.db.Where("client_id = ? AND timestamp BETWEEN ? AND ? AND "+clientAuditEventPredicate, clientID, from, to).
		Order("timestamp asc, log_id asc").
		Limit(limit).
		Offset(offset).
		Find(&logs).Error
	return logs, err
}

func (r *auditRepoImpl) CountLogsByTimeRange(from, to time.Time, clientID string) (int64, error) {
	var total int64
	err := r.db.Model(&models.AuditLog{}).
		Where("client_id = ? AND timestamp BETWEEN ? AND ? AND "+clientAuditEventPredicate, clientID, from, to).
		Count(&total).Error
	return total, err
}
