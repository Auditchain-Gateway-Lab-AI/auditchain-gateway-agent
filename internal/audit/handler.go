package audit

import (
	"context"
	"log"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Handler serves HTTP endpoints for the audit module.
type Handler struct {
	service *Service
	token   string
}

// NewHandler creates a new HTTP Handler for audit operations.
func NewHandler(service *Service, token string) *Handler {
	return &Handler{
		service: service,
		token:   token,
	}
}

// ServeHTTP implements http.Handler for backward compatibility.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/audit")
	path = strings.TrimPrefix(path, "/")

	switch {
	case path == "run":
		h.HandleRun(w, r)
	case strings.HasPrefix(path, "resource/"):
		h.HandleAuditResource(w, r)
	case path == "health":
		h.HandleHealth(w, r)
	default:
		http.NotFound(w, r)
	}
}

// HandleRun handles POST /audit/run and GET /audit/run.
func (h *Handler) HandleRun(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var fromStr, toStr, resource string
	var autoRecover bool

	if r.Method == http.MethodPost {
		var req AuditRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			fromStr = req.From
			toStr = req.To
			resource = req.Resource
			autoRecover = req.AutoRecover
		}
	} else if r.Method == http.MethodGet {
		fromStr = r.URL.Query().Get("from")
		toStr = r.URL.Query().Get("to")
		resource = r.URL.Query().Get("resource")
		if autoRecoverStr := r.URL.Query().Get("auto_recover"); autoRecoverStr != "" {
			autoRecover, _ = strconv.ParseBool(autoRecoverStr)
		}
	} else {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	// If resource is specified, audit single resource
	if resource != "" {
		res, err := h.service.AuditResource(ctx, resource, autoRecover)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}

	// Default to last 24 hours if from and to are empty
	var from, to time.Time
	var err error
	if fromStr != "" && toStr != "" {
		from, err = time.Parse(time.RFC3339, fromStr)
		if err != nil {
			http.Error(w, "invalid from timestamp, use RFC3339", http.StatusBadRequest)
			return
		}
		to, err = time.Parse(time.RFC3339, toStr)
		if err != nil {
			http.Error(w, "invalid to timestamp, use RFC3339", http.StatusBadRequest)
			return
		}
	} else {
		to = time.Now()
		from = to.Add(-24 * time.Hour)
	}

	report, err := h.service.AuditTimeRange(ctx, from, to, autoRecover)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Kirim report ke Gateway secara asinkron agar statistik dashboard segera ter-update
	go func(rep *Report) {
		submitCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := h.service.SubmitReport(submitCtx, rep); err != nil {
			log.Printf("[AuditHandler] Gagal mengirim audit report ke Gateway: %v", err)
		} else {
			log.Printf("[AuditHandler] Berhasil mengirim audit report ke Gateway (RunID: %s)", rep.RunID)
		}
	}(report)

	writeJSON(w, http.StatusOK, report)
}

// HandleAuditResource handles GET /audit/resource/:resource.
func (h *Handler) HandleAuditResource(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rawResource := strings.TrimPrefix(r.URL.Path, "/audit/resource/")
	rawResource = strings.TrimPrefix(rawResource, "/")
	resource, err := url.PathUnescape(rawResource)
	if err != nil || resource == "" {
		http.Error(w, "invalid resource in path", http.StatusBadRequest)
		return
	}

	autoRecover := false
	if autoRecoverStr := r.URL.Query().Get("auto_recover"); autoRecoverStr != "" {
		autoRecover, _ = strconv.ParseBool(autoRecoverStr)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	res, err := h.service.AuditResource(ctx, resource, autoRecover)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, res)
}

// HandleHealth handles GET /audit/health.
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "module": "audit"})
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

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
