package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// tokenBucket menyimpan state rate limiter per klien
type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	maxToken float64
	refillHz float64 // token per detik
	lastTime time.Time
}

func (b *tokenBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastTime).Seconds()
	b.lastTime = now

	// Isi ulang token berdasarkan waktu yang berlalu
	b.tokens += elapsed * b.refillHz
	if b.tokens > b.maxToken {
		b.tokens = b.maxToken
	}

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

var (
	buckets   = make(map[string]*tokenBucket)
	bucketsMu sync.Mutex
)

// RateLimiter membatasi request berdasarkan client_id yang sudah di-inject middleware sebelumnya
// ratePerSec adalah fallback jika client_id tidak ditemukan di context
func RateLimiter(defaultRatePerSec int) gin.HandlerFunc {
	return func(c *gin.Context) {
		clientIDVal, exists := c.Get("client_id")
		if !exists {
			c.Next()
			return
		}

		clientID := clientIDVal.(string)

		bucketsMu.Lock()
		bucket, ok := buckets[clientID]
		if !ok {
			rate := float64(defaultRatePerSec)
			bucket = &tokenBucket{
				tokens:   rate,
				maxToken: rate,
				refillHz: rate,
				lastTime: time.Now(),
			}
			buckets[clientID] = bucket
		}
		bucketsMu.Unlock()

		if !bucket.allow() {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"status":  "error",
				"message": "Rate limit terlampaui. Coba lagi sebentar.",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}
