package middleware

import (
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// JWTAuth melindungi endpoint Dashboard — hanya user login yang boleh masuk
func JWTAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Akses ditolak. Token tidak ditemukan."})
			c.Abort()
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Format token salah. Gunakan format: Bearer <token>"})
			c.Abort()
			return
		}

		tokenString := parts[1]
		secret := os.Getenv("JWT_SECRET")

		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, http.ErrAbortHandler
			}
			return []byte(secret), nil
		})

		if err != nil || !token.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Token tidak valid atau sudah kedaluwarsa. Silakan login kembali.",
			})
			c.Abort()
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Klaim token tidak valid."})
			c.Abort()
			return
		}

		roleVal, hasRole := claims["role"]
		roleStr, okRole := roleVal.(string)
		isAdmin := hasRole && okRole && strings.EqualFold(roleStr, "admin")

		clientIDVal, ok := claims["client_id"]
		clientID, okString := clientIDVal.(string)
		if (!ok || !okString || clientID == "") && !isAdmin {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Token tidak memiliki identitas client yang valid."})
			c.Abort()
			return
		}

		if clientID != "" {
			c.Set("client_id", clientID)
		}
		if userIDVal, hasUserID := claims["user_id"]; hasUserID {
			if userIDStr, okUserID := userIDVal.(string); okUserID {
				c.Set("user_id", userIDStr)
			}
		}
		if usernameVal, hasUsername := claims["username"]; hasUsername {
			if usernameStr, okUsername := usernameVal.(string); okUsername {
				c.Set("username", usernameStr)
			}
		}
		if hasRole && okRole {
			c.Set("role", roleStr)
		}
		c.Next()
	}
}

// AdminAuth melindungi endpoint admin — memvalidasi token JWT dan memastikan role adalah "admin"
func AdminAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Akses ditolak. Token tidak ditemukan."})
			c.Abort()
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Format token salah. Gunakan format: Bearer <token>"})
			c.Abort()
			return
		}

		tokenString := parts[1]
		secret := os.Getenv("JWT_SECRET")

		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, http.ErrAbortHandler
			}
			return []byte(secret), nil
		})

		if err != nil || !token.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Token tidak valid atau sudah kedaluwarsa. Silakan login kembali.",
			})
			c.Abort()
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Klaim token tidak valid."})
			c.Abort()
			return
		}

		roleVal, hasRole := claims["role"]
		roleStr, okRole := roleVal.(string)
		if !hasRole || !okRole || strings.ToLower(roleStr) != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Akses ditolak. Endpoint ini hanya untuk Administrator."})
			c.Abort()
			return
		}

		clientIDVal, ok := claims["client_id"]
		clientID, okString := clientIDVal.(string)
		if ok && okString && clientID != "" {
			c.Set("client_id", clientID)
		}

		if userIDVal, hasUserID := claims["user_id"]; hasUserID {
			if userIDStr, okUserID := userIDVal.(string); okUserID {
				c.Set("user_id", userIDStr)
			}
		}
		if usernameVal, hasUsername := claims["username"]; hasUsername {
			if usernameStr, okUsername := usernameVal.(string); okUsername {
				c.Set("username", usernameStr)
			}
		}
		c.Set("role", roleStr)
		c.Next()
	}
}
