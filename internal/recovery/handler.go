package recovery

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Handler serves the HTTP recovery endpoint.
type Handler struct {
	service        *Service
	token          string
	command        *CommandService
	enabled        bool
	configured     bool
	clientID       string
	maxBodyBytes   int64
	requestTimeout time.Duration
}

// NewHandler creates a new HTTP Handler for recovery.
func NewHandler(service *Service, token string) *Handler {
	return &Handler{
		service:        service,
		token:          token,
		enabled:        true,
		maxBodyBytes:   1 << 20,
		requestTimeout: 15 * time.Second,
	}
}

func NewConfiguredHandler(service *Service, recoveryToken string, enabled bool, clientID string, command *CommandService, maxBodyBytes int64, requestTimeout time.Duration) *Handler {
	if maxBodyBytes <= 0 {
		maxBodyBytes = 1 << 20
	}
	if requestTimeout <= 0 {
		requestTimeout = 15 * time.Second
	}
	return &Handler{
		service: service, token: recoveryToken, enabled: enabled, configured: true,
		clientID: clientID, command: command, maxBodyBytes: maxBodyBytes, requestTimeout: requestTimeout,
	}
}

// ServeHTTP implements http.Handler for backward compatibility.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/recover/capabilities" {
		h.HandleCapabilities(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/recover/") {
		h.HandleCommand(w, r)
		return
	}
	h.HandleRecover(w, r)
}

// HandleCapabilities reports whether the separately authenticated write path
// is enabled for one policy-mapped resource table. It does not inspect or
// mutate a client row.
// @Summary Check Agent Recovery write capability
// @Tags Recovery
// @Produce json
// @Security BearerAuth
// @Param table query string true "Resource table"
// @Success 200 {object} map[string]interface{}
// @Router /recover/capabilities [get]
func (h *Handler) HandleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeCommandError(w, commandFailure(http.StatusMethodNotAllowed, "method_not_allowed", "GET is required"))
		return
	}
	if !h.authorized(r) {
		writeCommandError(w, commandFailure(http.StatusUnauthorized, "invalid_agent_token", "recovery token is invalid"))
		return
	}
	table := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("table")))
	if table == "" {
		writeCommandError(w, commandFailure(http.StatusBadRequest, "invalid_payload", "table is required"))
		return
	}
	ready := h.enabled && h.command != nil && h.command.enabled && h.command.policy != nil && h.clientID != ""
	allowed := false
	if ready {
		_, allowed = h.command.policy.tables[table]
	}
	reason := ""
	if !ready {
		reason = "recovery_disabled"
	} else if !allowed {
		reason = "table_not_allowed"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"enabled": ready, "allowed": allowed, "reason": reason,
	})
}

// HandleRecover handles POST /recover.
// HandleRecover godoc
// @Summary Pemulihan data satu baris (Single-row Recovery)
// @Description Melakukan pemulihan (rollback/update) satu baris data operasional di database Oracle klien menggunakan payload data bersih dari blockchain/offchain log.
// @Tags Recovery
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body RecoverRequest true "Detail baris dan data bersih yang akan dipulihkan"
// @Success 200 {object} RecoverResult "Hasil pemulihan data"
// @Failure 400 {string} string "Bad Request"
// @Failure 401 {string} string "Unauthorized"
// @Failure 500 {object} map[string]interface{} "Gagal memulihkan baris"
// @Failure 503 {string} string "Recovery service disabled"
// @Router /recover [post]
func (h *Handler) HandleRecover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.configured && !h.enabled {
		writeCommandError(w, commandFailure(http.StatusForbidden, "recovery_disabled", "Recovery write operations are disabled"))
		return
	}
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if h.service == nil {
		http.Error(w, "recovery service is disabled or unconfigured", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)

	var req RecoverRequest
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	table := strings.TrimSpace(req.Table)
	recordID := strings.TrimSpace(req.ID)

	// If resource is provided (format TABLE:ID), parse table and id
	if req.Resource != "" {
		parts := strings.SplitN(strings.TrimSpace(req.Resource), ":", 2)
		if len(parts) == 2 {
			table = strings.TrimSpace(parts[0])
			recordID = strings.TrimSpace(parts[1])
		} else {
			slashParts := strings.SplitN(strings.TrimSpace(req.Resource), "/", 2)
			if len(slashParts) == 2 {
				table = strings.TrimSpace(slashParts[0])
				recordID = strings.TrimSpace(slashParts[1])
			}
		}
	}

	if table == "" || recordID == "" {
		http.Error(w, "table and id (or resource with format TABLE:ID) must be provided in JSON body", http.StatusBadRequest)
		return
	}

	if len(req.Data) == 0 {
		http.Error(w, "data object cannot be empty", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.requestTimeout)
	defer cancel()

	result, err := h.service.RecoverRow(ctx, table, recordID, req.Data)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "FAILED",
			"table":   table,
			"id":      recordID,
			"error":   err.Error(),
			"message": "failed to recover row",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}

// HandleCommand executes the Gateway-to-Agent, tenant-bound recovery contract.
// @Summary Restore one source row using a verified Gateway command
// @Description Applies one idempotent, policy-limited row restore after checking the current hash and reading the row back.
// @Tags Recovery
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param table path string true "Policy-mapped table name"
// @Param id path string true "Primary key value"
// @Param request body RecoveryCommandRequest true "Recovery command"
// @Success 200 {object} RecoveryCommandResult
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /recover/{table}/{id} [post]
func (h *Handler) HandleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeCommandError(w, commandFailure(http.StatusMethodNotAllowed, "method_not_allowed", "POST is required"))
		return
	}
	if !h.enabled {
		writeCommandError(w, commandFailure(http.StatusForbidden, "recovery_disabled", "Recovery write operations are disabled"))
		return
	}
	if !h.authorized(r) {
		writeCommandError(w, commandFailure(http.StatusUnauthorized, "invalid_agent_token", "recovery token is invalid"))
		return
	}
	if h.command == nil || h.clientID == "" {
		writeCommandError(w, commandFailure(http.StatusServiceUnavailable, "recovery_disabled", "Recovery command service is unavailable"))
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/recover/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		writeCommandError(w, commandFailure(http.StatusBadRequest, "invalid_payload", "path must be /recover/{table}/{id}"))
		return
	}
	tableName, err := url.PathUnescape(parts[0])
	if err != nil {
		writeCommandError(w, commandFailure(http.StatusBadRequest, "invalid_payload", "table path is invalid"))
		return
	}
	recordID, err := url.PathUnescape(parts[1])
	if err != nil {
		writeCommandError(w, commandFailure(http.StatusBadRequest, "invalid_payload", "record path is invalid"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var request RecoveryCommandRequest
	if err := decoder.Decode(&request); err != nil {
		writeCommandError(w, commandFailure(http.StatusBadRequest, "invalid_payload", "command JSON is invalid"))
		return
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeCommandError(w, commandFailure(http.StatusBadRequest, "invalid_payload", "command JSON must contain one object"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.requestTimeout)
	defer cancel()
	result, failure := h.command.Execute(ctx, tableName, recordID, request)
	if failure != nil {
		writeCommandError(w, failure)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}

func writeCommandError(w http.ResponseWriter, failure *CommandFailure) {
	if failure == nil {
		failure = commandFailure(http.StatusInternalServerError, "internal_error", "recovery command failed")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(failure.Status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": failure.Code, "message": failure.Message})
}

func (h *Handler) authorized(r *http.Request) bool {
	if h.token == "" {
		return true
	}
	authHeader := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return false
	}
	token := authHeader[len(prefix):]
	return subtle.ConstantTimeCompare([]byte(token), []byte(h.token)) == 1
}
