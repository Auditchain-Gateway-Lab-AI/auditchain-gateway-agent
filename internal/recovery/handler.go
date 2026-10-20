package recovery

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Handler serves the HTTP recovery endpoint.
type Handler struct {
	service *Service
	token   string
}

// NewHandler creates a new HTTP Handler for recovery.
func NewHandler(service *Service, token string) *Handler {
	return &Handler{
		service: service,
		token:   token,
	}
}

// ServeHTTP implements http.Handler for backward compatibility.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.HandleRecover(w, r)
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

	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if h.service == nil {
		http.Error(w, "recovery service is disabled or unconfigured", http.StatusServiceUnavailable)
		return
	}

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

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
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
