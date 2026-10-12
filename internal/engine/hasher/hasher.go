package hasher

import (
	"crypto/sha3"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"auditchain-agent/internal/models"
)

// GenerateLogHash computes the same canonical SHA3-256 leaf as the Besu
// backend. The order and timestamp precision are part of the shared contract.
func GenerateLogHash(auditLog *models.AuditLog) string {
	if auditLog == nil {
		return ""
	}

	authorizationContext := auditLog.AuthorizationContext
	if authorizationContext == "null" || authorizationContext == "<nil>" || authorizationContext == "" {
		authorizationContext = ""
	}
	metadata := canonicalizeJSON(auditLog.Metadata)

	canonical := fmt.Sprintf("%s|%s|%s|%s|%d|%s|%s|%s",
		auditLog.LogID,
		auditLog.Actor,
		auditLog.Action,
		auditLog.Resource,
		auditLog.Timestamp.UnixMicro(),
		auditLog.SourceSystem,
		authorizationContext,
		metadata,
	)
	value := sha3.Sum256([]byte(canonical))
	return hex.EncodeToString(value[:])
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
