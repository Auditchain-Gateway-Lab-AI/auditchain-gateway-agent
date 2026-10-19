package hasher

import (
	"encoding/json"
	"fmt"
	"go-blockchain-api/internal/models"
	"go-blockchain-api/pkg/crypto"
	"log"

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

// ProcessPendingLogs mencari log berstatus RECEIVED dan memprosesnya menjadi HASHED.
// Dipakai untuk log yang masuk via ingestion manual (bukan Kafka).
func (h *Engine) ProcessPendingLogs() error {
	var pendingLogs []models.AuditLog

	// RECOVERY rows written by older releases are forensic compatibility data,
	// not client-originated audit events. Never hash or send them through the
	// normal audit Merkle pipeline.
	if err := h.DB.Where("status = ? AND COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY'", "RECEIVED").Order("timestamp asc").Find(&pendingLogs).Error; err != nil {
		return err
	}

	if len(pendingLogs) == 0 {
		return nil
	}

	for _, auditLog := range pendingLogs {
		hashValue := GenerateLogHash(&auditLog)
		auditLog.HashValue = hashValue
		auditLog.Status = "HASHED"
		if err := h.DB.Save(&auditLog).Error; err != nil {
			log.Printf("[Hasher] Gagal menyimpan hash untuk log %s: %v", auditLog.LogID, err)
			continue
		}

		log.Printf("[Hasher] ✅ Log %s berhasil di-hash: %s", auditLog.LogID, hashValue)
	}

	return nil
}
