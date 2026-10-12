// Command gatewayapi is a loopback-only preview of the Mini PC client API.
// It never starts Kafka consumers or the Gateway anchoring worker.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"auditchain-agent/internal/modules/clientapi"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if strings.TrimSpace(os.Getenv("GATEWAY_CLIENT_API_PREVIEW_ENABLED")) != "true" {
		return errors.New("client API preview disabled; no HTTP listener started")
	}
	address := strings.TrimSpace(os.Getenv("GATEWAY_CLIENT_API_BIND"))
	if err := validatePreviewAddress(address); err != nil {
		return err
	}
	dsn := strings.TrimSpace(os.Getenv("CLIENT_API_DB_DSN"))
	if dsn == "" {
		return errors.New("CLIENT_API_DB_DSN is required (use a read-only Besu PostgreSQL account)")
	}
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		return errors.New("JWT_SECRET is required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("connect Besu PostgreSQL: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping Besu PostgreSQL: %w", err)
	}
	router, err := newRouter(db, []byte(secret))
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              address,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen client API preview: %w", err)
	}
	defer listener.Close()
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Serve(listener) }()
	log.Printf("Client API preview listening on %s (no Kafka workers)", listener.Addr())
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func validatePreviewAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return errors.New("GATEWAY_CLIENT_API_BIND must be an explicit loopback host:port")
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("client API preview must bind only to loopback")
	}
	if port == "0" {
		return errors.New("client API preview needs an explicit nonzero port")
	}
	return nil
}

func newRouter(db *gorm.DB, secret []byte) (*gin.Engine, error) {
	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/readyz", func(c *gin.Context) {
		sqlDB, err := db.DB()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready"})
			return
		}
		pingCtx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if sqlDB.PingContext(pingCtx) != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})
	if err := clientapi.RegisterRoutes(router.Group("/api"), db, secret); err != nil {
		return nil, err
	}
	return router, nil
}
