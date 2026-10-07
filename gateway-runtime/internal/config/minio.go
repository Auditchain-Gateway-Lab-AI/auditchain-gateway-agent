package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// MinIOConfig contains only the runtime settings needed by the Gateway.
// Bootstrap/root credentials intentionally do not belong here.
type MinIOConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseTLS    bool
}

func LoadMinIOConfig() (MinIOConfig, error) {
	cfg := MinIOConfig{
		Endpoint:  strings.TrimSpace(os.Getenv("MINIO_ENDPOINT")),
		AccessKey: strings.TrimSpace(os.Getenv("MINIO_ACCESS_KEY")),
		SecretKey: os.Getenv("MINIO_SECRET_KEY"),
		Bucket:    strings.TrimSpace(os.Getenv("MINIO_BUCKET")),
	}

	if raw := strings.TrimSpace(os.Getenv("MINIO_USE_TLS")); raw != "" {
		useTLS, err := strconv.ParseBool(raw)
		if err != nil {
			return MinIOConfig{}, fmt.Errorf("MINIO_USE_TLS tidak valid: %w", err)
		}
		cfg.UseTLS = useTLS
	}

	missing := make([]string, 0, 4)
	if cfg.Endpoint == "" {
		missing = append(missing, "MINIO_ENDPOINT")
	}
	if cfg.AccessKey == "" {
		missing = append(missing, "MINIO_ACCESS_KEY")
	}
	if cfg.SecretKey == "" {
		missing = append(missing, "MINIO_SECRET_KEY")
	}
	if cfg.Bucket == "" {
		missing = append(missing, "MINIO_BUCKET")
	}
	if len(missing) > 0 {
		return MinIOConfig{}, fmt.Errorf("konfigurasi MinIO belum lengkap: %s", strings.Join(missing, ", "))
	}

	return cfg, nil
}
