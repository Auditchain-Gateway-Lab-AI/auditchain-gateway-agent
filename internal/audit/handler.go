package audit

import (
	"context"
	"log"
	"crypto/subtle"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Handler serves HTTP endpoints for the audit module.
type Handler struct {
	service   *Service
	token     string
	jwtSecret string
}

// NewHandler creates a new HTTP Handler for audit operations.
func NewHandler(service *Service, token string, jwtSecret ...string) *Handler {
	secret := ""
	if len(jwtSecret) > 0 {
		secret = jwtSecret[0]
	}
	return &Handler{
		service:   service,
		token:     token,
		jwtSecret: secret,
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
// HandleRun godoc
// @Summary Jalankan audit integritas data
// @Description Memicu proses audit integritas data: mengambil offchain log dari Gateway, membandingkan hash dengan data aktual di database Oracle klien, dan memverifikasi Merkle Root di blockchain via Gateway. Hasilnya otomatis dilaporkan ke Gateway untuk memperbarui statistik dashboard.
// @Tags Audit
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body AuditRunRequest false "Parameter audit waktu atau resource (hanya untuk POST)"
// @Param from query string false "Waktu awal audit (format RFC3339, e.g. 2026-10-19T00:00:00Z)"
// @Param to query string false "Waktu akhir audit (format RFC3339, e.g. 2026-10-20T00:00:00Z)"
// @Param resource query string false "Filter resource spesifik (format TABLE:ID)"
// @Param auto_recover query bool false "Otomatis pulihkan data jika ditemukan anomali/tamper"
// @Success 200 {object} Report "Laporan hasil audit"
// @Failure 400 {string} string "Bad Request"
// @Failure 401 {string} string "Unauthorized"
// @Failure 500 {string} string "Internal Server Error"
// @Router /audit/run [post]
// @Router /audit/run [get]
func (h *Handler) HandleRun(w http.ResponseWriter, r *http.Request) {
	callerToken, ok := h.authorized(r)
	if !ok {
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
	if callerToken != "" {
		ctx = WithCallerToken(ctx, callerToken)
	}

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
	go func(rep *Report, callerTok string) {
		submitCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if callerTok != "" {
			submitCtx = WithCallerToken(submitCtx, callerTok)
		}
		if err := h.service.SubmitReport(submitCtx, rep); err != nil {
			log.Printf("[AuditHandler] Gagal mengirim audit report ke Gateway: %v", err)
		} else {
			log.Printf("[AuditHandler] Berhasil mengirim audit report ke Gateway (RunID: %s)", rep.RunID)
		}
	}(report, callerToken)

	writeJSON(w, http.StatusOK, report)
}

// HandleAuditResource handles GET /audit/resource/:resource.
// HandleAuditResource godoc
// @Summary Audit satu resource spesifik
// @Description Mengaudit integritas satu resource spesifik (format TABLE:ID atau TABLE/ID) terhadap log offchain dan blockchain.
// @Tags Audit
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param resource path string true "Resource yang diaudit (contoh: KABUPATEN:3201 atau KABUPATEN/3201)"
// @Param auto_recover query bool false "Otomatis pulihkan data jika tampered"
// @Success 200 {object} LogResult "Hasil audit satu resource"
// @Failure 400 {string} string "Bad Request"
// @Failure 401 {string} string "Unauthorized"
// @Failure 500 {string} string "Internal Server Error"
// @Router /audit/resource/{resource} [get]
func (h *Handler) HandleAuditResource(w http.ResponseWriter, r *http.Request) {
	callerToken, ok := h.authorized(r)
	if !ok {
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
	if callerToken != "" {
		ctx = WithCallerToken(ctx, callerToken)
	}

	res, err := h.service.AuditResource(ctx, resource, autoRecover)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, res)
}

// HandleHealth handles GET /audit/health.
// HandleHealth godoc
// @Summary Health check modul audit
// @Description Mengecek status kesiapan modul audit.
// @Tags Audit
// @Produce json
// @Success 200 {object} map[string]string "Modul audit ok"
// @Router /audit/health [get]
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "module": "audit"})
}

func (h *Handler) authorized(r *http.Request) (string, bool) {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if authHeader == "" {
		if h.token == "" && h.jwtSecret == "" {
			return "", true
		}
		log.Println("⚠️ [AgentAuth] Header Authorization kosong")
		return "", false
	}

	tokenString := authHeader
	if strings.HasPrefix(tokenString, "Bearer ") {
		tokenString = strings.TrimSpace(strings.TrimPrefix(tokenString, "Bearer "))
	}
	// Tangani kasus user di Apidog mengetik kata 'Bearer ' ganda
	if strings.HasPrefix(tokenString, "Bearer ") {
		tokenString = strings.TrimSpace(strings.TrimPrefix(tokenString, "Bearer "))
	}

	// 1. Validasi JWT token jika jwtSecret dikonfigurasi
	if h.jwtSecret != "" {
		token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, errors.New("unexpected signing method")
			}
			return []byte(h.jwtSecret), nil
		})
		if err == nil && token.Valid {
			return tokenString, true
		}
		if err != nil {
			log.Printf("⚠️ [AgentAuth] Validasi JWT gagal: %v (pastikan JWT_SECRET di Agent sama persis dengan Gateway)", err)
		}
	} else if strings.HasPrefix(tokenString, "eyJ") {
		log.Println("⚠️ [AgentAuth] Request membawa JWT token tetapi JWT_SECRET belum dikonfigurasi di file .env Agent")
	}

	// 2. Cocokkan dengan static token jika ada
	if h.token != "" && subtle.ConstantTimeCompare([]byte(tokenString), []byte(h.token)) == 1 {
		return tokenString, true
	}

	// 3. Jika tidak ada secret maupun token yang dikonfigurasi, izinkan
	if h.token == "" && h.jwtSecret == "" {
		return tokenString, true
	}

	log.Printf("⚠️ [AgentAuth] Akses ditolak: token tidak cocok dengan JWT_SECRET maupun AGENT_VERIFY_TOKEN (panjang token=%d)", len(tokenString))
	return "", false
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
