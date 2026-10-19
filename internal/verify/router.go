package verify

import "net/http"

// RegisterRoutes registers all inbound verify endpoints onto the provided http.ServeMux.
// All endpoint paths and behaviors remain strictly identical for auditchain-api compatibility.
func RegisterRoutes(mux *http.ServeMux, s *Server) {
	// Read one current operational resource row.
	mux.HandleFunc("/verify/", s.handleVerify)

	// Read one historical source event by its audit_trail ID.
	mux.HandleFunc("/verify-audit/", s.handleVerifyAudit)
	mux.HandleFunc("/verify-audit-lookup", s.handleLookupAuditTrail)

	// Resource geospasial (untuk Satu Peta)
	// Format: GET /verify-resource/<table>/<id>
	mux.HandleFunc("/verify-resource/", s.handleVerifyResource)

	if s.tableEndpointEnabled {
		mux.HandleFunc("/table/", s.handleTable)
	} else {
		mux.HandleFunc("/table/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "table endpoint disabled", http.StatusNotFound)
		})
	}
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
}
