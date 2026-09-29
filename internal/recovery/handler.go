package recovery

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type HTTPConfig struct {
	Enabled            bool
	Token              string
	MaxBodyBytes       int64
	RequestTimeout     time.Duration
	RateLimitPerMinute int
}

type Handler struct {
	service *Service
	cfg     HTTPConfig
	now     func() time.Time
	limiter *ipRateLimiter
}

func NewHandler(service *Service, cfg HTTPConfig) *Handler {
	if cfg.RateLimitPerMinute <= 0 {
		cfg.RateLimitPerMinute = 60
	}
	return &Handler{service: service, cfg: cfg, now: time.Now, limiter: newIPRateLimiter(cfg.RateLimitPerMinute)}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, newAPIError("invalid_payload", "method not allowed"))
		return
	}
	if !h.cfg.Enabled {
		writeAPIError(w, http.StatusForbidden, newAPIError("recovery_disabled", "recovery is disabled"))
		return
	}
	if h.limiter != nil && !h.limiter.Allow(clientIP(r)) {
		writeAPIError(w, http.StatusTooManyRequests, newAPIError("rate_limited", "recovery request rate limit exceeded"))
		return
	}
	if !validBearer(r.Header.Get("Authorization"), h.cfg.Token) {
		writeAPIError(w, http.StatusUnauthorized, newAPIError("invalid_agent_token", "recovery token is invalid"))
		return
	}
	if h.cfg.MaxBodyBytes <= 0 {
		writeAPIError(w, http.StatusForbidden, newAPIError("recovery_disabled", "recovery is not configured"))
		return
	}
	tableName, recordID, ok := parsePath(r.URL.Path)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, newAPIError("invalid_payload", "path must be /recover/:table/:record_id"))
		return
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" && !strings.HasPrefix(strings.ToLower(contentType), "application/json") {
		writeAPIError(w, http.StatusBadRequest, newAPIError("invalid_payload", "content type must be application/json"))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	request, err := decodeRequest(decoder)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, newAPIError("invalid_payload", "request body is too large"))
			return
		}
		apiErr := asAPIError(err)
		if apiErr == nil {
			apiErr = newAPIError("invalid_payload", "request body is invalid")
		}
		writeAPIError(w, http.StatusBadRequest, apiErr)
		return
	}

	timeout := h.cfg.RequestTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	response, err := h.service.Execute(ctx, tableName, recordID, request)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func validBearer(header, expected string) bool {
	prefix := "Bearer "
	if expected == "" || len(header) != len(prefix)+len(expected) || !strings.HasPrefix(header, prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(expected)) == 1
}

func parsePath(path string) (string, string, bool) {
	if !strings.HasPrefix(path, "/recover/") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/recover/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	table, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", "", false
	}
	recordID, err := url.PathUnescape(parts[1])
	if err != nil || strings.Contains(table, "/") || strings.Contains(recordID, "/") || len(recordID) > 256 {
		return "", "", false
	}
	return table, recordID, true
}

func writeServiceError(w http.ResponseWriter, err error) {
	if apiErr := asAPIError(err); apiErr != nil {
		status := statusForCode(apiErr.Code)
		writeAPIError(w, status, apiErr)
		return
	}
	writeAPIError(w, http.StatusInternalServerError, newAPIError("write_rejected", "recovery request could not be processed"))
}

func statusForCode(code string) int {
	switch code {
	case "invalid_payload", "desired_state_invalid":
		return http.StatusBadRequest
	case "invalid_agent_token":
		return http.StatusUnauthorized
	case "recovery_disabled", "table_not_allowed", "column_not_allowed":
		if code == "recovery_disabled" {
			return http.StatusForbidden
		}
		return http.StatusForbidden
	case "resource_mapping_not_found":
		return http.StatusNotFound
	case "source_state_changed", "idempotency_conflict", "command_in_progress":
		return http.StatusConflict
	case "write_rejected":
		return http.StatusUnprocessableEntity
	case "database_unreachable", "execution_outcome_unknown":
		return http.StatusServiceUnavailable
	case "rate_limited":
		return http.StatusTooManyRequests
	case "readback_mismatch", "idempotency_store_failed":
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func writeAPIError(w http.ResponseWriter, status int, apiErr *APIError) {
	writeJSON(w, status, apiErr)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type ipRateLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	limit   int
	clients map[string]rateEntry
}

type rateEntry struct {
	started time.Time
	count   int
}

func newIPRateLimiter(limit int) *ipRateLimiter {
	return &ipRateLimiter{window: time.Minute, limit: limit, clients: make(map[string]rateEntry)}
}

func (l *ipRateLimiter) Allow(client string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.clients[client]
	if !ok || now.Sub(entry.started) >= l.window {
		if len(l.clients) >= 4096 {
			l.clients = make(map[string]rateEntry)
		}
		l.clients[client] = rateEntry{started: now, count: 1}
		return true
	}
	if entry.count >= l.limit {
		return false
	}
	entry.count++
	l.clients[client] = entry
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "unknown"
}
