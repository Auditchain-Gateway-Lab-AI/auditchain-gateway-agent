package middleware

import (
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const RequestIDHeader = "X-Request-ID"

var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// RequestID attaches a correlation ID to every request and response. A caller
// supplied ID is preserved only when it is short and log-safe; otherwise the
// server generates a UUID. The completion log is emitted after downstream
// middleware so authenticated client/user context is available.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader(RequestIDHeader))
		if !safeRequestID.MatchString(requestID) {
			requestID = uuid.NewString()
		}

		started := time.Now()
		c.Set("request_id", requestID)
		c.Header(RequestIDHeader, requestID)
		c.Next()

		clientID, _ := c.Get("client_id")
		log.Printf(
			"[HTTP] request_id=%s method=%s path=%s status=%d duration_ms=%d client_id=%v",
			requestID,
			c.Request.Method,
			c.Request.URL.Path,
			c.Writer.Status(),
			time.Since(started).Milliseconds(),
			clientID,
		)
	}
}

func GetRequestID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return c.GetString("request_id")
}
