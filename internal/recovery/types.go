package recovery

import "time"

// RecoverRequest is the body accepted by the recovery endpoint.
type RecoverRequest struct {
	Resource string                 `json:"resource,omitempty"` // e.g. "KABUPATEN:3201"
	Table    string                 `json:"table,omitempty"`    // e.g. "KABUPATEN"
	ID       string                 `json:"id,omitempty"`       // e.g. "3201"
	Data     map[string]interface{} `json:"data,omitempty"`     // Clean column-value map
}

// RecoverResult contains the result of a single-row recovery execution.
type RecoverResult struct {
	Status           string    `json:"status"`            // "SUCCESS" | "FAILED"
	Table            string    `json:"table"`             // e.g. "KABUPATEN"
	ID               string    `json:"id"`                // e.g. "3201"
	Operation        string    `json:"operation"`         // "UPDATE" | "INSERT"
	RecoveredColumns []string  `json:"recovered_columns"` // list of updated columns
	Timestamp        time.Time `json:"timestamp"`
	Message          string    `json:"message,omitempty"`
}
