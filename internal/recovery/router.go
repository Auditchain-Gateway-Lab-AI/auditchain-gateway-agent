package recovery

import "net/http"

// RegisterRoutes registers all recovery endpoints onto the provided http.ServeMux.
func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("/recover", h.HandleRecover)
	mux.Handle("/recover/", h)
}
