package agentverifier

import (
	"net/http"
	"strings"

	"go-blockchain-api/internal/middleware"
	"go-blockchain-api/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Handler mengelola endpoint konfigurasi Agent per klien
type Handler struct {
	DB *gorm.DB
}

func NewHandler(db *gorm.DB) *Handler {
	return &Handler{DB: db}
}

type RegisterAgentRequest struct {
	AgentURL       string `json:"agent_url" binding:"required" example:"http://192.168.11.50:9090"`
	VerifyToken    string `json:"verify_token" binding:"required" example:"token-read-rahasia"`
	RecoveryToken  string `json:"recovery_token,omitempty" example:"token-write-rahasia"`
	TimeoutSeconds int    `json:"timeout_seconds" example:"5"`
}

// RegisterAgent mendaftarkan atau memperbarui URL Agent untuk klien yang sedang login.
// POST /api/dashboard/agent/config
func (h *Handler) RegisterAgent(c *gin.Context) {
	clientIDVal, _ := c.Get("client_id")
	clientID := clientIDVal.(string)

	var req RegisterAgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Format tidak valid: " + err.Error()})
		return
	}

	timeout := req.TimeoutSeconds
	if timeout <= 0 {
		timeout = 5
	}

	var existing models.AgentConfig
	err := h.DB.Where("client_id = ?", clientID).First(&existing).Error
	if err == nil {
		// Update
		updates := map[string]interface{}{
			"agent_url":       req.AgentURL,
			"verify_token":    req.VerifyToken,
			"timeout_seconds": timeout,
			"is_active":       true,
		}
		if strings.TrimSpace(req.RecoveryToken) != "" {
			updates["recovery_token"] = req.RecoveryToken
		}
		h.DB.Model(&existing).Updates(updates)
		c.JSON(http.StatusOK, gin.H{
			"message":   "Konfigurasi Agent berhasil diperbarui",
			"agent_url": req.AgentURL,
		})
		return
	}

	cfg := models.AgentConfig{
		ClientID:       clientID,
		AgentURL:       req.AgentURL,
		VerifyToken:    req.VerifyToken,
		RecoveryToken:  req.RecoveryToken,
		TimeoutSeconds: timeout,
		IsActive:       true,
	}
	if err := h.DB.Create(&cfg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal menyimpan konfigurasi Agent"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":   "Agent berhasil didaftarkan. Verifikasi Lapis 3 aktif.",
		"agent_url": req.AgentURL,
	})
}

// GetAgentConfig menampilkan konfigurasi Agent tanpa verify_token.
// GET /api/dashboard/agent/config
func (h *Handler) GetAgentConfig(c *gin.Context) {
	clientIDVal, _ := c.Get("client_id")
	clientID := clientIDVal.(string)

	var cfg models.AgentConfig
	if err := h.DB.Where("client_id = ?", clientID).First(&cfg).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Agent belum terdaftar untuk klien ini"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"agent_url":       cfg.AgentURL,
		"timeout_seconds": cfg.TimeoutSeconds,
		"is_active":       cfg.IsActive,
		"updated_at":      cfg.UpdatedAt,
	})
}

// DeleteAgentConfig menonaktifkan konfigurasi Agent (soft delete).
// DELETE /api/dashboard/agent/config
func (h *Handler) DeleteAgentConfig(c *gin.Context) {
	clientIDVal, _ := c.Get("client_id")
	clientID := clientIDVal.(string)

	if err := h.DB.Where("client_id = ?", clientID).Delete(&models.AgentConfig{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal menghapus konfigurasi Agent"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Konfigurasi Agent dihapus. Verifikasi Lapis 3 dinonaktifkan."})
}

// PingAgent memeriksa apakah Agent klien dapat dihubungi dari Gateway.
// GET /api/dashboard/agent/ping
func (h *Handler) PingAgent(c *gin.Context) {
	clientIDVal, _ := c.Get("client_id")
	clientID := clientIDVal.(string)

	var cfg models.AgentConfig
	if err := h.DB.Where("client_id = ? AND is_active = true", clientID).First(&cfg).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Agent belum terdaftar"})
		return
	}

	client := &http.Client{Timeout: 3e9}
	resp, err := client.Get(strings.TrimRight(cfg.AgentURL, "/") + "/health")
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"reachable": false,
			"agent_url": cfg.AgentURL,
			"error":     err.Error(),
		})
		return
	}
	resp.Body.Close()

	c.JSON(http.StatusOK, gin.H{
		"reachable":   true,
		"agent_url":   cfg.AgentURL,
		"http_status": resp.StatusCode,
	})
}

// RegisterRoutes mendaftarkan semua endpoint manajemen Agent
func RegisterRoutes(rg *gin.RouterGroup, h *Handler) {
	agent := rg.Group("/agent")
	agent.Use(middleware.JWTAuth())
	{
		agent.POST("/config", h.RegisterAgent)
		agent.GET("/config", h.GetAgentConfig)
		agent.DELETE("/config", h.DeleteAgentConfig)
		agent.GET("/ping", h.PingAgent)
	}
}
