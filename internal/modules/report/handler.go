package report

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	Service Service
}

func NewHandler(service Service) *Handler {
	return &Handler{Service: service}
}

func (h *Handler) getClientID(c *gin.Context) (string, bool) {
	clientIDVal, exists := c.Get("client_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Identitas client tidak ditemukan pada token."})
		return "", false
	}
	clientID, ok := clientIDVal.(string)
	if !ok || clientID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Identitas client pada token tidak valid."})
		return "", false
	}

	// Jika user adalah admin, izinkan override client_id via query param
	roleVal, hasRole := c.Get("role")
	if hasRole {
		roleStr, okRole := roleVal.(string)
		if okRole && strings.ToLower(roleStr) == "admin" {
			if queryClientID := c.Query("client_id"); queryClientID != "" {
				return queryClientID, true
			}
		}
	}

	return clientID, true
}

type ErrorResponse struct {
	Error string `json:"error" example:"Pesan kesalahan"`
}

type GenerateReportRequest struct {
	PeriodFrom string   `json:"period_from" binding:"required"`
	PeriodTo   string   `json:"period_to" binding:"required"`
	Format     string   `json:"format" binding:"required"`
	Sections   []string `json:"sections"`
}

func parseTimeRobust(timeStr string) (time.Time, error) {
	timeStr = strings.TrimSpace(timeStr)
	if timeStr == "" {
		return time.Time{}, fmt.Errorf("empty time string")
	}

	if len(timeStr) > 10 && timeStr[10] == ' ' {
		timeStr = timeStr[:10] + "T" + timeStr[11:]
	}

	normalized := strings.ReplaceAll(timeStr, " ", "+")

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05.999999999Z07",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}

	for _, l := range layouts {
		if t, err := time.Parse(l, normalized); err == nil {
			return t, nil
		}
		if t, err := time.Parse(l, timeStr); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse time: %s", timeStr)
}

// @Summary Generate audit report
// @Description Men-generate laporan audit PDF atau CSV berdasarkan periode waktu tertentu.
// @Tags Report
// @Accept json
// @Produce application/pdf
// @Produce text/csv
// @Security BearerAuth
// @Param request body GenerateReportRequest true "Parameter laporan"
// @Success 200 {file} file "File laporan PDF atau CSV"
// @Failure 400 {object} ErrorResponse "Format request tidak valid"
// @Failure 401 {object} ErrorResponse "Identitas client tidak valid"
// @Failure 500 {object} ErrorResponse "Gagal generate laporan"
// @Router /report/generate [post]
func (h *Handler) GenerateReport(c *gin.Context) {
	clientID, ok := h.getClientID(c)
	if !ok {
		return
	}

	var req GenerateReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Format request tidak valid."})
		return
	}

	if req.Format != "csv" && req.Format != "pdf" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Hanya format csv dan pdf yang didukung saat ini."})
		return
	}

	fromTime, err := parseTimeRobust(req.PeriodFrom)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Format period_from tidak valid."})
		return
	}

	toTime, err := parseTimeRobust(req.PeriodTo)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Format period_to tidak valid."})
		return
	}

	if toTime.Before(fromTime) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "period_to tidak boleh lebih awal dari period_from."})
		return
	}

	var data []byte
	var generateErr error
	var filename string
	var contentType string

	if req.Format == "pdf" {
		data, generateErr = h.Service.GeneratePDFReport(clientID, fromTime, toTime)
		filename = fmt.Sprintf("auditchain-report-%s.pdf", time.Now().Format("20060102-150405"))
		contentType = "application/pdf"
	} else {
		data, generateErr = h.Service.GenerateCSVReport(clientID, fromTime, toTime)
		filename = fmt.Sprintf("auditchain-report-%s.csv", time.Now().Format("20060102-150405"))
		contentType = "text/csv; charset=utf-8"
	}

	if generateErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal generate laporan " + strings.ToUpper(req.Format) + "."})
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Data(http.StatusOK, contentType, data)
}

// TODO: Phase 2 methods (history, download)
// TODO: Phase 3 methods (schedule)
