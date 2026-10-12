package models

import "time"

// AuditLog is the portable audit-event shape shared by the Agent pipeline.
// It intentionally contains no database-specific tags or behavior.
type AuditLog struct {
	LogID                string    `json:"log_id"`
	ClientID             string    `json:"client_id"`
	Actor                string    `json:"actor"`
	Action               string    `json:"action"`
	Resource             string    `json:"resource"`
	Timestamp            time.Time `json:"timestamp"`
	SourceSystem         string    `json:"source_system"`
	AuthorizationContext string    `json:"authorization_context"`
	Metadata             string    `json:"metadata"`
	HashValue            string    `json:"hash_value,omitempty"`
}
