package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	Service Service
}

type AuthRequest struct {
	Username string `json:"username" binding:"required" example:"auditor_senior"`
	Password string `json:"password" binding:"required,min=6" example:"rahasia1234"`
}

type RegisterRequest struct {
	ClientID string `json:"client_id" binding:"required" example:"a1b2c3d4-e5f6-7890-1234-56789abcdef0"`
	Username string `json:"username" binding:"required" example:"auditor_senior"`
	Password string `json:"password" binding:"required,min=6" example:"rahasia1234"`
}

type ProfileUpdateRequest struct {
	FullName        string `json:"full_name"`
	Username        string `json:"username" binding:"required,min=4"`
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type UserResponseData struct {
	ID       string `json:"id"`
	ClientID string `json:"client_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type ProfileResponseData struct {
	ID          string `json:"id"`
	FullName    string `json:"full_name"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	ClientID    string `json:"client_id"`
	CompanyName string `json:"company_name"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type RegisterResponse struct {
	Message string           `json:"message"`
	User    UserResponseData `json:"user"`
}

type LoginResponse struct {
	Message string `json:"message"`
	Token   string `json:"token"`
}

type UpdateProfileResponse struct {
	Message string              `json:"message"`
	Token   string              `json:"token"`
	User    ProfileResponseData `json:"user"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

// @Summary Register a new user
// @Description Mendaftarkan pengguna baru (auditor/admin) ke dalam perusahaan (client) tertentu.
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body RegisterRequest true "Data Pendaftaran"
// @Success 201 {object} RegisterResponse "Pengguna berhasil didaftarkan"
// @Failure 400 {object} ErrorResponse "Format tidak valid atau client_id belum diisi"
// @Failure 404 {object} ErrorResponse "Perusahaan (Client ID) tidak terdaftar di sistem"
// @Failure 409 {object} ErrorResponse "Username sudah digunakan"
// @Failure 500 {object} ErrorResponse "Gagal memproses pendaftaran"
// @Router /auth/register [post]
func (h *Handler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Format tidak valid atau client_id belum diisi."})
		return
	}

	user, client, err := h.Service.Register(req.ClientID, req.Username, req.Password)
	if err != nil {
		switch err.Error() {
		case "client_not_found":
			c.JSON(http.StatusNotFound, gin.H{"error": "Perusahaan (Client ID) tidak terdaftar di sistem"})
		case "username_used":
			c.JSON(http.StatusConflict, gin.H{"error": "Username sudah digunakan"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal memproses pendaftaran"})
		}
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message": "Pengguna berhasil didaftarkan ke perusahaan " + client.CompanyName,
		"user": map[string]interface{}{
			"id":        user.ID,
			"client_id": user.ClientID,
			"username":  user.Username,
			"role":      user.Role,
		},
	})
}

// @Summary User login
// @Description Autentikasi pengguna dan mendapatkan token JWT untuk akses API.
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body AuthRequest true "Kredensial Login"
// @Success 200 {object} LoginResponse "Login berhasil dan mengembalikan token"
// @Failure 400 {object} ErrorResponse "Format request tidak valid"
// @Failure 401 {object} ErrorResponse "Username atau Password salah!"
// @Failure 403 {object} ErrorResponse "Akun dinonaktifkan karena client tidak aktif"
// @Failure 500 {object} ErrorResponse "Gagal mencetak token keamanan"
// @Router /auth/login [post]
func (h *Handler) Login(c *gin.Context) {
	var req AuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Format request tidak valid"})
		return
	}

	token, err := h.Service.Login(req.Username, req.Password)
	if err != nil {
		switch err.Error() {
		case "invalid_credentials":
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Username atau Password salah!"})
		case "client_inactive":
			c.JSON(http.StatusForbidden, gin.H{"error": "Account is disabled because the associated client is inactive."})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal mencetak token keamanan"})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Login berhasil",
		"token":   token,
	})
}

// @Summary Get user profile
// @Description Mendapatkan informasi profil dari pengguna yang sedang login (berdasarkan JWT).
// @Tags Auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} ProfileResponseData "Data profil user"
// @Failure 401 {object} ErrorResponse "Token tidak memiliki identitas user yang valid"
// @Failure 404 {object} ErrorResponse "Profil user tidak ditemukan"
// @Router /auth/me [get]
func (h *Handler) GetProfile(c *gin.Context) {
	userID, ok := c.Get("user_id")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Token tidak memiliki identitas user yang valid."})
		return
	}

	user, client, err := h.Service.GetProfile(userID.(string))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Profil user tidak ditemukan."})
		return
	}

	companyName := ""
	if client != nil {
		companyName = client.CompanyName
	}

	c.JSON(http.StatusOK, gin.H{
		"id":           user.ID,
		"full_name":    user.FullName,
		"username":     user.Username,
		"role":         user.Role,
		"client_id":    user.ClientID,
		"company_name": companyName,
		"created_at":   user.CreatedAt,
		"updated_at":   user.UpdatedAt,
	})
}

// @Summary Update user profile
// @Description Memperbarui informasi profil pengguna yang sedang login. Bisa juga untuk mengubah password.
// @Tags Auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body ProfileUpdateRequest true "Data Profil Baru"
// @Success 200 {object} UpdateProfileResponse "Profil berhasil diperbarui dan mengembalikan token baru jika password diubah"
// @Failure 400 {object} ErrorResponse "Validasi input gagal (misal username terlalu pendek)"
// @Failure 401 {object} ErrorResponse "Token tidak valid atau Password saat ini tidak sesuai"
// @Failure 409 {object} ErrorResponse "Username sudah digunakan"
// @Failure 500 {object} ErrorResponse "Gagal memperbarui profil"
// @Router /auth/me [put]
func (h *Handler) UpdateProfile(c *gin.Context) {
	userID, ok := c.Get("user_id")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Token tidak memiliki identitas user yang valid."})
		return
	}

	var req ProfileUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Nama pengguna minimal 4 karakter."})
		return
	}

	user, client, token, err := h.Service.UpdateProfile(
		userID.(string),
		req.FullName,
		req.Username,
		req.CurrentPassword,
		req.NewPassword,
	)
	if err != nil {
		switch err.Error() {
		case "username_used":
			c.JSON(http.StatusConflict, gin.H{"error": "Username sudah digunakan."})
		case "invalid_current_password":
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Password saat ini tidak sesuai."})
		case "password_too_short":
			c.JSON(http.StatusBadRequest, gin.H{"error": "Password baru minimal 6 karakter."})
		case "username_too_short", "username_required":
			c.JSON(http.StatusBadRequest, gin.H{"error": "Username minimal 4 karakter."})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal memperbarui profil."})
		}
		return
	}

	companyName := ""
	if client != nil {
		companyName = client.CompanyName
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Profil berhasil diperbarui.",
		"token":   token,
		"user": gin.H{
			"id":           user.ID,
			"full_name":    user.FullName,
			"username":     user.Username,
			"role":         user.Role,
			"client_id":    user.ClientID,
			"company_name": companyName,
		},
	})
}
