package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type Operation string

const (
	OperationUpsert Operation = "UPSERT"
	OperationDelete Operation = "DELETE"
	OperationNoop   Operation = "NOOP"
)

func (o Operation) Valid() bool {
	return o == OperationUpsert || o == OperationDelete || o == OperationNoop
}

type RecoveryRequest struct {
	RequestID          string         `json:"request_id"`
	IdempotencyKey     string         `json:"idempotency_key"`
	Operation          Operation      `json:"operation"`
	ExpectedBeforeHash string         `json:"expected_before_hash"`
	DesiredStateHash   string         `json:"desired_state_hash"`
	DesiredState       map[string]any `json:"desired_state,omitempty"`
	Reference          Reference      `json:"reference"`
	IssuedAt           time.Time      `json:"issued_at"`
}

type Reference struct {
	LogID         string `json:"log_id"`
	AuditLeafHash string `json:"audit_leaf_hash"`
	MerkleRoot    string `json:"merkle_root"`
	AnchorID      string `json:"anchor_id"`
}

type RecoveryResponse struct {
	RequestID        string    `json:"request_id"`
	Operation        Operation `json:"operation"`
	Applied          bool      `json:"applied"`
	IdempotentReplay bool      `json:"idempotent_replay"`
	BeforeHash       string    `json:"before_hash"`
	AfterHash        string    `json:"after_hash"`
	ReadbackMatch    bool      `json:"readback_match"`
	FoundAfter       bool      `json:"found_after"`
	CheckedAt        time.Time `json:"checked_at"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e APIError) Error() string { return e.Code + ": " + e.Message }

func newAPIError(code, message string) *APIError {
	return &APIError{Code: code, Message: message}
}

var (
	ErrDatabaseUnreachable = errors.New("database unreachable")
	ErrSourceStateChanged  = errors.New("source state changed")
	ErrWriteRejected       = errors.New("write rejected")
	ErrReadbackMismatch    = errors.New("readback mismatch")
	ErrOutcomeUnknown      = errors.New("execution outcome unknown")
)

type StateSnapshot struct {
	Found bool
	State map[string]any
	Hash  string
}

type ApplyResult struct {
	BeforeHash    string
	AfterHash     string
	FoundAfter    bool
	ReadbackMatch bool
}

type Repository interface {
	Read(ctx context.Context, table TablePolicy, recordID string) (StateSnapshot, error)
	Apply(ctx context.Context, table TablePolicy, recordID string, request RecoveryRequest) (ApplyResult, error)
}

func (r RecoveryRequest) SemanticCopy() map[string]any {
	return map[string]any{
		"request_id":           r.RequestID,
		"idempotency_key":      r.IdempotencyKey,
		"operation":            string(r.Operation),
		"expected_before_hash": r.ExpectedBeforeHash,
		"desired_state_hash":   r.DesiredStateHash,
		"desired_state":        r.DesiredState,
		"reference": map[string]any{
			"log_id":          r.Reference.LogID,
			"audit_leaf_hash": r.Reference.AuditLeafHash,
			"merkle_root":     r.Reference.MerkleRoot,
			"anchor_id":       r.Reference.AnchorID,
		},
	}
}

func (r RecoveryRequest) Validate(_ string, maxColumns int, now time.Time, maxClockSkew time.Duration) error {
	if !isUUID(r.RequestID) {
		return newAPIError("invalid_payload", "request_id must be a UUID")
	}
	if strings.TrimSpace(r.IdempotencyKey) == "" || len(r.IdempotencyKey) > 256 {
		return newAPIError("invalid_payload", "idempotency_key is invalid")
	}
	if !r.Operation.Valid() {
		return newAPIError("invalid_payload", "operation is invalid")
	}
	if !validHash(r.ExpectedBeforeHash) || !validHash(r.DesiredStateHash) {
		return newAPIError("invalid_payload", "state hash is invalid")
	}
	if r.Operation == OperationDelete {
		if len(r.DesiredState) != 0 {
			return newAPIError("desired_state_invalid", "DELETE desired_state must be empty")
		}
	} else if r.DesiredState == nil {
		return newAPIError("desired_state_invalid", "desired_state must be an object")
	}
	if len(r.DesiredState) > maxColumns {
		return newAPIError("invalid_payload", "desired_state contains too many columns")
	}
	if r.Reference.LogID == "" || len(r.Reference.LogID) > 256 ||
		!validHash(r.Reference.AuditLeafHash) || !validHash(r.Reference.MerkleRoot) ||
		r.Reference.AnchorID == "" || len(r.Reference.AnchorID) > 256 {
		return newAPIError("invalid_payload", "reference is invalid")
	}
	if r.IssuedAt.IsZero() || r.IssuedAt.Before(now.Add(-maxClockSkew)) || r.IssuedAt.After(now.Add(maxClockSkew)) {
		return newAPIError("invalid_payload", "issued_at is outside the allowed clock skew")
	}
	return nil
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func decodeRequest(decoder *json.Decoder) (RecoveryRequest, error) {
	decoder.DisallowUnknownFields()
	var request RecoveryRequest
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return request, err
		}
		return request, newAPIError("invalid_payload", "request body is invalid JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return request, newAPIError("invalid_payload", "request body contains trailing JSON")
	} else if err != io.EOF {
		return request, newAPIError("invalid_payload", "request body contains invalid trailing data")
	}
	return request, nil
}
