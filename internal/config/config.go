package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

type Config struct {
	JWTSecret string
	Client   ClientConfig
	SourceDB SourceDBConfig
	Gateway  GatewayConfig
	Tables   []TableConfig `yaml:"tables"`
	Recovery RecoveryConfig
	Audit    AuditConfig
}

type AuditConfig struct {
	Enabled         bool
	IntervalMinutes int
	WindowMinutes   int
	AutoRecover     bool
}

type ClientConfig struct {
	APIKey string
}

type SourceDBConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
}

type GatewayConfig struct {
	URL            string
	Token          string
	TimeoutSeconds int
}

type TableConfig struct {
	Name         string `yaml:"name"`
	SourceSystem string `yaml:"source_system"`
}

type RecoveryConfig struct {
	Enabled  bool
	ClientID string
	Token    string
}

func Load(configPath string) (*Config, error) {
	godotenv.Load()

	f, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("gagal buka config file: %w", err)
	}
	defer f.Close()

	var cfg Config
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("gagal parse config file: %w", err)
	}

	cfg.Client.APIKey = getEnv("AUDITCHAIN_API_KEY", "")
	cfg.Gateway.URL = getEnv("GATEWAY_URL", "")
	cfg.Gateway.Token = getEnv("GATEWAY_TOKEN", getEnv("AUDITCHAIN_API_KEY", ""))
	cfg.Gateway.TimeoutSeconds = getEnvInt("GATEWAY_TIMEOUT_SECONDS", 10)

	auditEnabled, _ := getEnvBool("AGENT_AUDIT_ENABLED", false)
	cfg.Audit.Enabled = auditEnabled
	cfg.Audit.IntervalMinutes = getEnvInt("AGENT_AUDIT_INTERVAL_MINUTES", 60)
	cfg.Audit.WindowMinutes = getEnvInt("AGENT_AUDIT_WINDOW_MINUTES", 1440)
	auditAutoRecover, _ := getEnvBool("AGENT_AUDIT_AUTO_RECOVER", false)
	cfg.Audit.AutoRecover = auditAutoRecover

	cfg.SourceDB.Host = getEnv("DB_HOST", "localhost")
	cfg.SourceDB.Port = getEnvInt("DB_PORT", 1521)
	cfg.SourceDB.User = getEnv("DB_USER", "")
	cfg.SourceDB.Password = getEnv("DB_PASSWORD", "")
	cfg.SourceDB.DBName = getEnv("DB_NAME", "ORCLCDB")

	recoveryEnabled, _ := getEnvBool("AGENT_RECOVERY_ENABLED", true)
	cfg.Recovery.Enabled = recoveryEnabled
	cfg.Recovery.ClientID = getEnv("AGENT_CLIENT_ID", "")
	cfg.Recovery.Token = getEnv("AGENT_RECOVERY_TOKEN", "")
	cfg.JWTSecret = getEnv("JWT_SECRET", "")

	return &cfg, nil
}

// VerifyToken returns the read-only token without duplicating its storage.
func (c *Config) VerifyToken() string {
	return getEnv("AGENT_VERIFY_TOKEN", "")
}

func requireEnv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		panic(fmt.Sprintf("environment variable %s wajib diisi", key))
	}
	return val
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getEnvBool(key string, def bool) (bool, error) {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return def, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, err
	}
	return parsed, nil
}
