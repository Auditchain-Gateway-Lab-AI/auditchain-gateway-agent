// Package clientapi contains only client-facing Gateway endpoints for Mini PC.
package clientapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type authUser struct {
	ID        string
	ClientID  *string
	FullName  string
	Username  string
	Password  string
	Role      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (authUser) TableName() string { return "users" }

type authClient struct {
	ID          string
	CompanyName string
	Status      string
}

func (authClient) TableName() string { return "clients" }

// RegisterAuth deliberately omits register, profile mutation, and /api/admin.
// The administrator's existing API remains on Besu.
func RegisterAuth(api *gin.RouterGroup, db *gorm.DB, jwtSecret []byte) {
	auth := api.Group("/auth")
	auth.POST("/login", func(c *gin.Context) { login(c, db, jwtSecret) })
	auth.GET("/me", requireClient(jwtSecret), func(c *gin.Context) { profile(c, db) })
}

func requireClient(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		parts := strings.Fields(c.GetHeader("Authorization"))
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Akses ditolak. Token tidak ditemukan."})
			return
		}
		token, err := jwt.Parse(parts[1], func(token *jwt.Token) (any, error) {
			if token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
				return nil, http.ErrAbortHandler
			}
			return secret, nil
		})
		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Token tidak valid atau sudah kedaluwarsa."})
			return
		}
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Klaim token tidak valid."})
			return
		}
		clientID, _ := claims["client_id"].(string)
		userID, _ := claims["user_id"].(string)
		role, _ := claims["role"].(string)
		if clientID == "" || userID == "" || strings.EqualFold(role, "admin") {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Endpoint ini hanya untuk pengguna klien."})
			return
		}
		c.Set("client_id", clientID)
		c.Set("user_id", userID)
		c.Next()
	}
}

func login(c *gin.Context, db *gorm.DB, secret []byte) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required,min=6"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Format request tidak valid"})
		return
	}
	var user authUser
	if err := db.Where("username = ? AND deleted_at IS NULL", req.Username).First(&user).Error; err != nil ||
		bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)) != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Username atau Password salah!"})
		return
	}
	clientID := ""
	if user.ClientID != nil {
		clientID = strings.TrimSpace(*user.ClientID)
	}
	if strings.EqualFold(user.Role, "admin") || clientID == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Gunakan Admin Portal di Besu untuk akun administrator."})
		return
	}
	var client authClient
	if err := db.Where("id = ? AND deleted_at IS NULL", clientID).First(&client).Error; err != nil || client.Status != "active" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Account is disabled because the associated client is inactive."})
		return
	}
	claims := jwt.MapClaims{
		"user_id": user.ID, "client_id": clientID, "full_name": user.FullName,
		"username": user.Username, "role": user.Role,
		"exp": time.Now().Add(2 * time.Hour).Unix(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal mencetak token keamanan"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Login berhasil", "token": token})
}

func profile(c *gin.Context, db *gorm.DB) {
	userID := c.GetString("user_id")
	clientID := c.GetString("client_id")
	var user authUser
	if err := db.Where("id = ? AND client_id = ? AND deleted_at IS NULL", userID, clientID).First(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Profil user tidak ditemukan."})
		return
	}
	var client authClient
	if err := db.Where("id = ? AND deleted_at IS NULL", clientID).First(&client).Error; err != nil || client.Status != "active" {
		c.JSON(http.StatusNotFound, gin.H{"error": "Profil user tidak ditemukan."})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id": user.ID, "full_name": user.FullName, "username": user.Username,
		"role": user.Role, "client_id": clientID, "company_name": client.CompanyName,
		"created_at": user.CreatedAt, "updated_at": user.UpdatedAt,
	})
}
