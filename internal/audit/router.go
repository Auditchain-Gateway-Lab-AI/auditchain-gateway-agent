package audit

import "net/http"

// RegisterRoutes registers all audit endpoints onto the provided http.ServeMux.
func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("/audit/run", h.HandleRun)
	mux.HandleFunc("/audit/resource/", h.HandleAuditResource)
	mux.HandleFunc("/audit/health", h.HandleHealth)
}
