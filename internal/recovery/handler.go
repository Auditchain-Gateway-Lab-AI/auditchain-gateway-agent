package recovery

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
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

// ServeHTTP handles POST /recover/:table/:id and POST /recover.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	// Extract table and id from URL path if present: /recover/<table>/<id>
	path := strings.TrimPrefix(r.URL.Path, "/recover")
	path = strings.TrimPrefix(path, "/")

	var table, recordID string
	if path != "" {
		parts := strings.SplitN(path, "/", 2)
		if len(parts) >= 1 && parts[0] != "" {
			table, _ = url.PathUnescape(parts[0])
		}
		if len(parts) == 2 && parts[1] != "" {
			recordID, _ = url.PathUnescape(parts[1])
		}
	}

	// Decode JSON body
	var bodyMap map[string]interface{}
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(&bodyMap); err != nil {
		http.Error(w, "invalid JSON payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Check if body has wrapper fields { "resource": "TABLE:ID", "data": { ... } } or { "table": "...", "id": "..." }
	var dataToRecover map[string]interface{}

	if rawResource, ok := bodyMap["resource"].(string); ok && rawResource != "" {
		resParts := strings.SplitN(rawResource, ":", 2)
		if len(resParts) == 2 {
			table = resParts[0]
			recordID = resParts[1]
		}
	}
	if bTable, ok := bodyMap["table"].(string); ok && bTable != "" {
		table = bTable
	}
	if bID, ok := bodyMap["id"].(string); ok && bID != "" {
		recordID = bID
	}

	if wrappedData, ok := bodyMap["data"].(map[string]interface{}); ok && len(wrappedData) > 0 {
		dataToRecover = wrappedData
	} else {
		// Treat the whole body as column map, removing meta fields
		dataToRecover = make(map[string]interface{}, len(bodyMap))
		for k, v := range bodyMap {
			if k != "table" && k != "id" && k != "resource" && k != "data" {
				dataToRecover[k] = v
			}
		}
	}

	if table == "" || recordID == "" {
		http.Error(w, "table and id must be specified via URL path (/recover/:table/:id) or in JSON body", http.StatusBadRequest)
		return
	}

	if len(dataToRecover) == 0 {
		http.Error(w, "no data columns provided to recover", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	result, err := h.service.RecoverRow(ctx, table, recordID, dataToRecover)
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
