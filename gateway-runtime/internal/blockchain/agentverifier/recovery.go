package agentverifier

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go-blockchain-api/internal/models"
	"go-blockchain-api/pkg/canonicalstate"
	"gorm.io/gorm"
)

// RecoveryReference is the immutable Gateway evidence that authorised a
// client-side write. The Agent does not trust the reference for database
// access; it records it in its command log and returns it as correlation
// context only.
type RecoveryReference struct {
	LogID         string `json:"log_id"`
	AuditLeafHash string `json:"audit_leaf_hash"`
	MerkleRoot    string `json:"merkle_root"`
	AnchorID      string `json:"anchor_id"`
}

// RecoveryCommand is the only write contract exposed to the client Agent.
// There is deliberately no SQL, predicate, or arbitrary column expression in
// this structure. Table/primary-key mapping and writable-column allowlists
// remain local to the Agent.
type RecoveryCommand struct {
	ClientID           string                 `json:"-"`
	RequestID          string                 `json:"request_id"`
	IdempotencyKey     string                 `json:"idempotency_key"`
	Operation          string                 `json:"operation"`
	ExpectedBeforeHash string                 `json:"expected_before_hash"`
	DesiredStateHash   string                 `json:"desired_state_hash"`
	DesiredState       map[string]interface{} `json:"desired_state,omitempty"`
	Reference          RecoveryReference      `json:"reference"`
	IssuedAt           time.Time              `json:"issued_at"`
}

// RecoveryResult is returned by POST /recover/:table/:record_id. A 2xx
// response is not considered success unless ReadbackMatch is true.
type RecoveryResult struct {
	RequestID        string    `json:"request_id"`
	Operation        string    `json:"operation"`
	Applied          bool      `json:"applied"`
	IdempotentReplay bool      `json:"idempotent_replay"`
	BeforeHash       string    `json:"before_hash"`
	AfterHash        string    `json:"after_hash"`
	ReadbackMatch    bool      `json:"readback_match"`
	FoundAfter       bool      `json:"found_after"`
	CheckedAt        time.Time `json:"checked_at"`
}

// RecoveryError preserves the Agent's stable error code without exposing the
// response body (which could contain client database details).
type RecoveryError struct {
	HTTPStatus int
	Code       string
	Message    string
}

func (e *RecoveryError) Error() string {
	if e == nil {
		return "agent recovery error"
	}
	if e.Message == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// ReadResource reads the current client row for a resource using the existing
// read token. It is used by recovery preflight and execute revalidation.
func (s *Service) ReadResource(ctx context.Context, clientID, resource string) (*ResourceRecord, error) {
	cfg, err := s.loadAgentConfig(clientID)
	if err != nil {
		if errors.Is(err, gormErrRecordNotFound()) {
			return nil, errors.New("agent_not_configured")
		}
		return nil, fmt.Errorf("agent_config_load_failed: %w", err)
	}
	if !cfg.IsActive || strings.TrimSpace(cfg.AgentURL) == "" {
		return nil, errors.New("agent_not_configured")
	}
	table, recordID, err := splitResource(resource)
	if err != nil {
		return nil, err
	}
	return s.fetchResourceFromAgentContext(ctx, cfg, table, recordID)
}

// RecoverResource sends one idempotent recovery command to the Agent. The
// request is authenticated with RecoveryToken, never VerifyToken.
func (s *Service) RecoverResource(ctx context.Context, command RecoveryCommand, resource string) (*RecoveryResult, error) {
	cfg, err := s.loadAgentConfig(command.ClientID)
	if err != nil {
		if errors.Is(err, gormErrRecordNotFound()) {
			return nil, errors.New("agent_not_configured")
		}
		return nil, fmt.Errorf("agent_config_load_failed: %w", err)
	}
	if !cfg.IsActive || strings.TrimSpace(cfg.AgentURL) == "" {
		return nil, errors.New("agent_not_configured")
	}
	if strings.TrimSpace(cfg.RecoveryToken) == "" {
		return nil, errors.New("agent_recovery_token_missing")
	}
	table, recordID, err := splitResource(resource)
	if err != nil {
		return nil, err
	}
	if err := validateRecoveryCommand(command); err != nil {
		return nil, err
	}

	return executeRecoveryRequest(ctx, cfg, command, table, recordID, nil)
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func executeRecoveryRequest(ctx context.Context, cfg *models.AgentConfig, command RecoveryCommand, table, recordID string, transport httpDoer) (*RecoveryResult, error) {
	if cfg == nil {
		return nil, errors.New("agent_not_configured")
	}
	body, err := json.Marshal(command)
	if err != nil {
		return nil, fmt.Errorf("agent_recovery_payload_invalid: %w", err)
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	requestURL := fmt.Sprintf("%s/recover/%s/%s", strings.TrimRight(cfg.AgentURL, "/"), url.PathEscape(table), url.PathEscape(recordID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("agent_recovery_request_invalid: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.RecoveryToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if transport == nil {
		transport = &http.Client{Timeout: timeout}
	}
	resp, err := transport.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent_recovery_unreachable: %w", err)
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return nil, fmt.Errorf("agent_recovery_response_read_failed: %w", readErr)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, parseRecoveryError(resp.StatusCode, responseBody)
	}
	var result RecoveryResult
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return nil, fmt.Errorf("agent_recovery_response_invalid: %w", err)
	}
	if result.RequestID != "" && result.RequestID != command.RequestID {
		return nil, errors.New("agent_recovery_response_request_mismatch")
	}
	if !result.ReadbackMatch {
		return nil, &RecoveryError{HTTPStatus: http.StatusInternalServerError, Code: "readback_mismatch", Message: "Agent readback tidak cocok dengan state yang diminta"}
	}
	return &result, nil
}

func validateRecoveryCommand(command RecoveryCommand) error {
	if strings.TrimSpace(command.ClientID) == "" || strings.TrimSpace(command.RequestID) == "" || strings.TrimSpace(command.IdempotencyKey) == "" {
		return errors.New("agent_recovery_invalid_payload")
	}
	switch strings.ToUpper(strings.TrimSpace(command.Operation)) {
	case models.RecoveryOperationUpsert:
		if len(command.DesiredState) == 0 {
			return errors.New("agent_recovery_desired_state_missing")
		}
	case models.RecoveryOperationDelete, models.RecoveryOperationNoop:
	default:
		return errors.New("agent_recovery_operation_invalid")
	}
	for name, value := range map[string]string{
		"expected_before_hash": command.ExpectedBeforeHash,
		"desired_state_hash":   command.DesiredStateHash,
		"audit_leaf_hash":      command.Reference.AuditLeafHash,
		"merkle_root":          command.Reference.MerkleRoot,
	} {
		if !isSHA3Hash(value) {
			return fmt.Errorf("agent_recovery_%s_invalid", name)
		}
	}
	if strings.TrimSpace(command.Reference.LogID) == "" || strings.TrimSpace(command.Reference.AnchorID) == "" {
		return errors.New("agent_recovery_reference_invalid")
	}
	if command.IssuedAt.IsZero() {
		return errors.New("agent_recovery_issued_at_missing")
	}
	return nil
}

func parseRecoveryError(status int, body []byte) error {
	var payload struct {
		Code    string `json:"code"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &payload)
	code := strings.TrimSpace(payload.Code)
	if code == "" {
		code = strings.TrimSpace(payload.Error)
	}
	if code == "" {
		code = fmt.Sprintf("agent_http_%d", status)
	}
	message := strings.TrimSpace(payload.Message)
	if message == "" && payload.Error != code {
		message = strings.TrimSpace(payload.Error)
	}
	return &RecoveryError{HTTPStatus: status, Code: code, Message: message}
}

func splitResource(resource string) (string, string, error) {
	parts := strings.SplitN(strings.TrimSpace(resource), ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", errors.New("resource_mapping_not_found")
	}
	if strings.ContainsAny(parts[0], "/\\?&#") || strings.ContainsAny(parts[1], "/\\?&#") {
		return "", "", errors.New("resource_mapping_invalid")
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func isSHA3Hash(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// gormErrRecordNotFound is kept as a small indirection so the recovery
// contract code does not expose GORM in its public types.
func gormErrRecordNotFound() error { return gorm.ErrRecordNotFound }

// HashResourceState computes the hash used in Agent preconditions and
// readback evidence. It intentionally uses the same canonical JSON package as
// the trusted-reference validator, but includes tenant and resource scope.
func HashResourceState(clientID, resource string, state map[string]interface{}) (string, error) {
	if state == nil {
		state = map[string]interface{}{}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	hash, _, err := canonicalstate.HashJSON(clientID, resource, raw)
	return hash, err
}
