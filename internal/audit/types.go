// Package audit implements the Agent-initiated outbound audit flow.
// It fetches offchain logs from the Gateway API, reads live client data
// from the local Oracle database, compares hashes, and optionally verifies
// Merkle roots against the blockchain via the Gateway.
package audit

import "time"

// OffchainLog mirrors the relevant fields of models.AuditLog returned by the
// Gateway's GET /api/agent/offchain-data endpoint.
type OffchainLog struct {
	LogID          string    `json:"log_id"`
	ClientID       string    `json:"client_id"`
	Actor          string    `json:"actor"`
	Action         string    `json:"action"`
	Resource       string    `json:"resource"`
	Timestamp      time.Time `json:"timestamp"`
	SourceSystem   string    `json:"source_system"`
	Metadata       string    `json:"metadata"`
	HashValue      string    `json:"hash_value"`
	MerkleRoot     string    `json:"merkle_root"`
	BlockchainTxID *string   `json:"blockchain_tx_id"`
	Status         string    `json:"status"`
	IsLatest       bool      `json:"is_latest"`
}

// VerifyMerkleRootRequest is the body sent to POST /api/agent/verify-merkle-root.
type VerifyMerkleRootRequest struct {
	LeafHash       string `json:"leaf_hash,omitempty"`
	MerkleRoot     string `json:"merkle_root"`
	BlockchainTxID string `json:"blockchain_tx_id"`
}

// VerifyMerkleRootResponse is the response from POST /api/agent/verify-merkle-root.
type VerifyMerkleRootResponse struct {
	IsMatch            bool   `json:"is_match"`
	LeafHash           string `json:"leaf_hash,omitempty"`
	ReconstructedRoot  string `json:"reconstructed_root,omitempty"`
	ExpectedMerkleRoot string `json:"expected_merkle_root"`
	ActualMerkleRoot   string `json:"actual_merkle_root"`
	BlockchainTxID     string `json:"blockchain_tx_id"`
}

// Discrepancy represents a mismatch between offchain metadata and client data.
type Discrepancy struct {
	Field    string      `json:"field"`
	Expected interface{} `json:"expected"`
	Actual   interface{} `json:"actual"`
}

// LogResult holds the audit outcome for a single offchain log entry.
type LogResult struct {
	LogID            string        `json:"log_id"`
	Resource         string        `json:"resource"`
	Action           string        `json:"action"`
	DataStatus       string        `json:"data_status"`       // VALID | TAMPERED | NOT_FOUND | ERROR
	BlockchainStatus string        `json:"blockchain_status"` // VALID | TAMPERED | PENDING | SKIPPED | ERROR
	ClientHash       string        `json:"client_hash,omitempty"`
	OffchainHash     string        `json:"offchain_hash,omitempty"`
	MerkleRoot       string        `json:"merkle_root,omitempty"`
	BlockchainTxID   string        `json:"blockchain_tx_id,omitempty"`
	Discrepancies    []Discrepancy `json:"discrepancies,omitempty"`
	AutoRecovered    bool          `json:"auto_recovered,omitempty"`
	RecoveryError    string        `json:"recovery_error,omitempty"`
	Error            string        `json:"error,omitempty"`
}

// Report aggregates audit results for an entire batch run.
type Report struct {
	RunID              string      `json:"run_id,omitempty"`
	From               time.Time   `json:"from"`
	To                 time.Time   `json:"to"`
	TotalLogs          int         `json:"total_logs"`
	AuditedLogs        int         `json:"audited_logs"`
	HistoricalSkipped  int         `json:"historical_skipped"`
	DataValid          int         `json:"data_valid"`
	DataTampered       int         `json:"data_tampered"`
	DataNotFound       int         `json:"data_not_found"`
	DataError          int         `json:"data_error"`
	BlockchainValid    int         `json:"blockchain_valid"`
	BlockchainTampered int         `json:"blockchain_tampered"`
	BlockchainPending  int         `json:"blockchain_pending"`
	BlockchainSkipped  int         `json:"blockchain_skipped"`
	BlockchainError    int         `json:"blockchain_error"`
	AutoRecoveredCount int         `json:"auto_recovered_count"`
	Results            []LogResult `json:"results"`
	Duration           string      `json:"duration"`
}

// AuditRunRequest is used when triggering an audit via HTTP endpoint.
type AuditRunRequest struct {
	From        string `json:"from"`         // RFC3339 timestamp
	To          string `json:"to"`           // RFC3339 timestamp
	Resource    string `json:"resource"`     // Optional: filter by single resource TABLE:ID
	AutoRecover bool   `json:"auto_recover"` // If true and data is tampered, trigger recovery
}
