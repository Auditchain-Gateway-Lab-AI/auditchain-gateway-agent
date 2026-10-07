package recovery

import (
	"net/http"
	"strconv"
	"strings"

	"go-blockchain-api/internal/models"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	Service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{Service: service}
}

type ErrorResponse struct {
	Error string `json:"error" example:"Pesan kesalahan"`
}

type IncidentListResponse struct {
	Data []models.TamperIncident `json:"data"`
}

type IncidentDetailResponse struct {
	Data IncidentDetailView `json:"data"`
}

type RequestListResponse struct {
	Data []models.RecoveryRequest `json:"data"`
}

type RequestDetailResponse struct {
	Data models.RecoveryRequest `json:"data"`
}

func (h *Handler) clientID(c *gin.Context) (string, bool) {
	value, exists := c.Get("client_id")
	clientID, ok := value.(string)
	if !ok {
		clientID = ""
	}
	clientID = strings.TrimSpace(clientID)

	if !exists || clientID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Identitas client pada token tidak valid."})
		return "", false
	}
	return clientID, true
}

func (h *Handler) userID(c *gin.Context) string {
	value, _ := c.Get("user_id")
	userID, _ := value.(string)
	return userID
}

func (h *Handler) clientOperator(c *gin.Context) bool {
	roleName, _ := c.Get("role")
	role, _ := roleName.(string)
	role = strings.TrimSpace(role)
	normalizedRole := strings.ToLower(role)
	if normalizedRole == "admin" || normalizedRole == "administrator" || strings.Contains(normalizedRole, "admin") || normalizedRole == "superuser" || normalizedRole == "platform_operator" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Recovery hanya dapat dijalankan oleh user client; admin platform tidak dapat mengeksekusi recovery."})
		return false
	}
	if strings.TrimSpace(h.userID(c)) == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Identitas user pada token tidak valid."})
		return false
	}
	return true
}

func (h *Handler) ListIncidents(c *gin.Context) {
	clientID, ok := h.clientID(c)
	if !ok {
		return
	}
	incidents, err := h.Service.ListIncidents(c.Request.Context(), clientID, IncidentFilter{Status: c.Query("status")})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal mengambil daftar tamper incident."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": incidents})
}

// @Summary Get tamper incident detail
// @Description Mengambil detail sebuah insiden tamper berdasarkan ID.
// @Tags Recovery
// @Produce json
// @Security BearerAuth
// @Param id path string true "Incident ID"
// @Success 200 {object} IncidentDetailResponse "Detail Insiden Tamper"
// @Failure 401 {object} ErrorResponse "Identitas client tidak valid"
// @Failure 404 {object} ErrorResponse "Tamper incident tidak ditemukan"
// @Failure 500 {object} ErrorResponse "Gagal mengambil detail tamper incident"
// @Router /recovery/incidents/{id} [get]
func (h *Handler) GetIncident(c *gin.Context) {
	clientID, ok := h.clientID(c)
	if !ok {
		return
	}
	incident, err := h.Service.GetIncidentDetail(c.Request.Context(), clientID, c.Param("id"))
	if serviceErrorCode(err, "incident_not_found") {
		c.JSON(http.StatusNotFound, gin.H{"error": "Tamper incident tidak ditemukan."})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal mengambil detail tamper incident."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": incident})
}

// @Summary List trusted client recovery references
// @Description Mengambil referensi event client yang valid terhadap Merkle proof dan Fabric untuk insiden tertentu.
// @Tags Recovery
// @Produce json
// @Security BearerAuth
// @Param id path string true "Incident ID"
// @Success 200 {object} map[string]interface{} "Daftar kandidat recovery"
// @Failure 401 {object} ErrorResponse "Identitas client tidak valid"
// @Failure 404 {object} ErrorResponse "Insiden tidak ditemukan"
// @Failure 500 {object} ErrorResponse "Gagal mengambil daftar kandidat"
// @Router /recovery/incidents/{id}/candidates [get]
func (h *Handler) ListCandidates(c *gin.Context) {
	clientID, ok := h.clientID(c)
	if !ok {
		return
	}
	candidates, err := h.Service.ListCandidates(c.Request.Context(), clientID, c.Param("id"))
	if err != nil {
		h.respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": candidates})
}

// @Summary Preflight check for recovery
// @Description Memeriksa referensi PostgreSQL/Fabric dan state live Agent sebelum recovery tanpa melakukan write.
// @Tags Recovery
// @Produce json
// @Security BearerAuth
// @Param id path string true "Incident ID"
// @Success 200 {object} map[string]interface{} "Hasil preflight check"
// @Failure 401 {object} ErrorResponse "Identitas client tidak valid"
// @Failure 409 {object} ErrorResponse "Konflik atau kondisi tidak valid untuk preflight"
// @Failure 500 {object} ErrorResponse "Gagal melakukan preflight"
// @Router /recovery/incidents/{id}/preflight [post]
func (h *Handler) Preflight(c *gin.Context) {
	clientID, ok := h.clientID(c)
	if !ok {
		return
	}
	result, err := h.Service.Preflight(c.Request.Context(), clientID, c.Param("id"))
	if err != nil {
		h.respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// @Summary List snapshot versions by resource
// @Description Mengambil histori event client ter-anchor dari sebuah resource (table).
// @Tags Recovery
// @Produce json
// @Security BearerAuth
// @Param resource path string true "Resource/Table Name"
// @Success 200 {object} map[string]interface{} "Histori event client ter-anchor"
// @Failure 401 {object} ErrorResponse "Identitas client tidak valid"
// @Failure 500 {object} ErrorResponse "Gagal mengambil histori recovery"
// @Router /recovery/resources/{resource}/versions [get]
func (h *Handler) ListVersions(c *gin.Context) {
	clientID, ok := h.clientID(c)
	if !ok {
		return
	}
	versions, err := h.Service.ListVersions(c.Request.Context(), clientID, c.Param("resource"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal mengambil histori recovery."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": versions})
}

// @Summary List recovery requests
// @Description Mengambil daftar permintaan (request) recovery yang diajukan.
// @Tags Recovery
// @Produce json
// @Security BearerAuth
// @Param status query string false "Filter Status Request"
// @Success 200 {object} RequestListResponse "Daftar recovery request"
// @Failure 401 {object} ErrorResponse "Identitas client tidak valid"
// @Failure 500 {object} ErrorResponse "Gagal mengambil daftar recovery request"
// @Router /recovery/requests [get]
func (h *Handler) ListRequests(c *gin.Context) {
	clientID, ok := h.clientID(c)
	if !ok {
		return
	}
	requests, err := h.Service.ListRequests(c.Request.Context(), clientID, c.Query("status"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal mengambil daftar recovery request."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": requests})
}

func (h *Handler) ListEvents(c *gin.Context) {
	clientID, ok := h.clientID(c)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	includeLegacy := !strings.EqualFold(c.DefaultQuery("include_legacy", "true"), "false")
	result, err := h.Service.ListEvents(c.Request.Context(), clientID, c.Query("result_status"), c.Query("resource"), page, pageSize, includeLegacy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal mengambil daftar recovery event."})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) GetEvent(c *gin.Context) {
	clientID, ok := h.clientID(c)
	if !ok {
		return
	}
	event, err := h.Service.GetEvent(c.Request.Context(), clientID, c.Param("id"))
	if serviceErrorCode(err, "recovery_event_not_found") {
		c.JSON(http.StatusNotFound, gin.H{"error": "Recovery event tidak ditemukan."})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal mengambil recovery event."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": event})
}

func (h *Handler) VerifyEvent(c *gin.Context) {
	clientID, ok := h.clientID(c)
	if !ok {
		return
	}
	result, err := h.Service.VerifyEvent(c.Request.Context(), clientID, c.Param("id"))
	if serviceErrorCode(err, "recovery_event_not_found") {
		c.JSON(http.StatusNotFound, gin.H{"error": "Recovery event tidak ditemukan."})
		return
	}
	if err != nil {
		c.JSON(http.StatusConflict, result)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) GetRequest(c *gin.Context) {
	clientID, ok := h.clientID(c)
	if !ok {
		return
	}
	request, err := h.Service.GetRequest(c.Request.Context(), clientID, c.Param("id"))
	if serviceErrorCode(err, "request_not_found") {
		c.JSON(http.StatusNotFound, gin.H{"error": "Recovery request tidak ditemukan."})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal mengambil detail recovery request."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": request})
}

// @Summary Create recovery request
// @Description Membekukan intent recovery state client melalui Agent setelah preflight Fabric dan state live lulus.
// @Tags Recovery
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body CreateRequestInput true "Data Request Recovery"
// @Success 201 {object} RequestDetailResponse "Recovery request berhasil dibuat"
// @Failure 400 {object} ErrorResponse "Format request tidak valid"
// @Failure 401 {object} ErrorResponse "Identitas client tidak valid"
// @Failure 409 {object} ErrorResponse "Konflik atau state tidak valid"
// @Failure 500 {object} ErrorResponse "Gagal membuat recovery request"
// @Router /recovery/requests [post]
func (h *Handler) CreateRequest(c *gin.Context) {
	clientID, ok := h.clientID(c)
	if !ok {
		return
	}
	if !h.clientOperator(c) {
		return
	}
	var input CreateRequestInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "incident_id, selected_log_id, reason, dan idempotency_key wajib diisi."})
		return
	}
	request, err := h.Service.CreateRequest(c.Request.Context(), clientID, h.userID(c), input)
	if err != nil {
		h.respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": request})
}

func (h *Handler) Approve(c *gin.Context) {
	clientID, ok := h.clientID(c)
	if !ok {
		return
	}
	request, err := h.Service.Approve(c.Request.Context(), clientID, c.Param("id"), h.userID(c))
	if err != nil {
		h.respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": request})
}

func (h *Handler) Reject(c *gin.Context) {
	clientID, ok := h.clientID(c)
	if !ok {
		return
	}
	request, err := h.Service.Reject(c.Request.Context(), clientID, c.Param("id"), h.userID(c))
	if err != nil {
		h.respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": request})
}

// @Summary Execute recovery request
// @Description Mengeksekusi intent recovery ke row client existing melalui Agent; tidak membuat tabel baru.
// @Tags Recovery
// @Produce json
// @Security BearerAuth
// @Param id path string true "Request ID"
// @Success 200 {object} RequestDetailResponse "Eksekusi recovery dimulai/berhasil"
// @Failure 401 {object} ErrorResponse "Identitas client tidak valid"
// @Failure 404 {object} ErrorResponse "Request tidak ditemukan"
// @Failure 409 {object} ErrorResponse "State request tidak sesuai"
// @Failure 500 {object} ErrorResponse "Gagal mengeksekusi recovery"
// @Router /recovery/requests/{id}/execute [post]
func (h *Handler) Execute(c *gin.Context) {
	clientID, ok := h.clientID(c)
	if !ok {
		return
	}
	if !h.clientOperator(c) {
		return
	}
	request, err := h.Service.Execute(c.Request.Context(), clientID, c.Param("id"), h.userID(c))
	if err != nil {
		h.respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": request})
}

func (h *Handler) respondServiceError(c *gin.Context, err error) {
	switch {
	case serviceErrorCode(err, "incident_not_found"), serviceErrorCode(err, "request_not_found"), serviceErrorCode(err, "snapshot_not_found"), serviceErrorCode(err, "recovery_event_not_found"):
		c.JSON(http.StatusNotFound, gin.H{"error": "Data recovery tidak ditemukan."})
	case serviceErrorCode(err, "invalid_request"), serviceErrorCode(err, "invalid_request_state"), serviceErrorCode(err, "incident_closed"), serviceErrorCode(err, "legacy_recovery_out_of_scope"), serviceErrorCode(err, "snapshot_belum_verified"), serviceErrorCode(err, "snapshot_reference_missing"), serviceErrorCode(err, "snapshot_checksum_missing"), serviceErrorCode(err, "snapshot_plaintext_hash_missing"), serviceErrorCode(err, "cross_log_recovery_not_allowed"), serviceErrorCode(err, "anchor_missing"), serviceErrorCode(err, "merkle_root_missing"), serviceErrorCode(err, "recovery_not_required"), serviceErrorCode(err, "idempotency_conflict"), serviceErrorCode(err, "recovery_active_conflict"), serviceErrorCode(err, "client_source_incident_required"), serviceErrorCode(err, "reference_resource_mismatch"), serviceErrorCode(err, "client_source_unreachable"), serviceErrorCode(err, "reference_changed"), serviceErrorCode(err, "source_state_changed"), serviceErrorCode(err, "execution_timeout_unknown"), serviceErrorCode(err, "cdc_start_time_missing"), serviceErrorCode(err, "request_execution_state_changed"), serviceErrorCode(err, "reference_not_latest_client_event"), serviceErrorCode(err, "reference_latest_client_event_missing"), serviceErrorCode(err, "reference_local_hash_mismatch"), serviceErrorCode(err, "reference_merkle_proof_mismatch"), serviceErrorCode(err, "reference_fabric_root_mismatch"), serviceErrorCode(err, "reference_metadata_invalid"):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case serviceErrorCode(err, "recovery_storage_unavailable"), serviceErrorCode(err, "fabric_unavailable"), serviceErrorCode(err, "fabric_anchor_unreachable"), serviceErrorCode(err, "reference_proof_read_failed"), serviceErrorCode(err, "agent_recovery_client_unavailable"), serviceErrorCode(err, "agent_not_configured"), serviceErrorCode(err, "agent_recovery_token_missing"), serviceErrorCode(err, "agent_recovery_unreachable"):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
	}
}

func serviceErrorCode(err error, code string) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return message == code || strings.HasPrefix(message, code+":")
}
