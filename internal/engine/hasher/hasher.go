package hasher

import (
	"auditchain-agent/internal/backendsupport/crypto"
	"auditchain-agent/internal/models"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

type Engine struct {
	DB *gorm.DB
}

func canonicalizeJSON(raw string) string {
	if raw == "" || raw == "null" || raw == "<nil>" {
		return ""
	}

	var parsed interface{}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return raw
	}

	normalized, err := json.Marshal(parsed)
	if err != nil {
		return raw
	}

	return string(normalized)
}

// GenerateLogHash menghasilkan hash deterministik dari AuditLog.
// CATATAN: prevHash TIDAK LAGI menjadi bagian dari hash formula.
// Local chain (PreviousHash) dihapus dari skema verifikasi — lihat
// AUDIT_CONTEXT.md testing branch notes. Parameter prevHash dipertahankan
// pada signature hanya untuk kompatibilitas caller lama; TIDAK dipakai
// dalam perhitungan hash.
func GenerateLogHash(auditLog *models.AuditLog) string {
	authCtx := auditLog.AuthorizationContext
	if authCtx == "null" || authCtx == "<nil>" || authCtx == "" {
		authCtx = ""
	}
	metadata := canonicalizeJSON(auditLog.Metadata)

	contextString := fmt.Sprintf("%s|%s|%s|%s|%d|%s|%s|%s",
		auditLog.LogID,
		auditLog.Actor,
		auditLog.Action,
		auditLog.Resource,
		auditLog.Timestamp.UnixMicro(),
		auditLog.SourceSystem,
		authCtx,
		metadata,
	)

	return crypto.GenerateSHA3_256(contextString)
}

// ProcessPendingLogsSince avoids touching legacy rows outside this cutover.
func (h *Engine) ProcessPendingLogsSince(cutoff time.Time) error {
	var pending []models.AuditLog
	if err := h.DB.Where("status = ? AND db_timestamp >= ? AND COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY'", "RECEIVED", cutoff).
		Order("timestamp asc").Limit(100).Find(&pending).Error; err != nil {
		return err
	}
	for _, item := range pending {
		hash := GenerateLogHash(&item)
		if err := h.DB.Model(&models.AuditLog{}).Where("log_id = ? AND status = ?", item.LogID, "RECEIVED").
			Updates(map[string]interface{}{"hash_value": hash, "status": "HASHED"}).Error; err != nil {
			return err
		}
	}
	return nil
}
