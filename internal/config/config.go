package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Client   ClientConfig
	SourceDB SourceDBConfig
	Gateway  GatewayConfig
	Kafka    KafkaConfig
	Tables   []TableConfig `yaml:"tables"`
	Recovery RecoveryConfig
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
	TimeoutSeconds int
}

type KafkaConfig struct {
	Brokers      string
	TopicPrefix  string
	GroupID      string
	SourceSystem string
}

type TableConfig struct {
	Name         string `yaml:"name"`
	SourceSystem string `yaml:"source_system"`
}

// RecoveryConfig is intentionally environment-driven. Recovery credentials and
// the client identity must not be committed to config.yml or a policy file.
type RecoveryConfig struct {
	Enabled               bool
	ClientID              string
	Token                 string
	StatePath             string
	MaxBodyBytes          int64
	MaxColumns            int
	MaxClockSkewSeconds   int
	RequestTimeoutSeconds int
	RetentionDays         int
	PolicyPath            string
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
	cfg.Gateway.TimeoutSeconds = getEnvInt("GATEWAY_TIMEOUT_SECONDS", 10)

	cfg.SourceDB.Host = getEnv("DB_HOST", "localhost")
	cfg.SourceDB.Port = getEnvInt("DB_PORT", 1521)
	cfg.SourceDB.User = getEnv("DB_USER", "")
	cfg.SourceDB.Password = getEnv("DB_PASSWORD", "")
	cfg.SourceDB.DBName = getEnv("DB_NAME", "ORCLCDB")

	cfg.Kafka.Brokers = getEnv("KAFKA_BROKERS", "localhost:9092")
	cfg.Kafka.TopicPrefix = getEnv("KAFKA_TOPIC_PREFIX", "satu_peta.public.")
	cfg.Kafka.GroupID = getEnv("KAFKA_GROUP_ID", "auditchain-agent-group")
	cfg.Kafka.SourceSystem = getEnv("KAFKA_SOURCE_SYSTEM", "SATU-PETA")

	recoveryEnabled, err := getEnvBool("AGENT_RECOVERY_ENABLED", false)
	if err != nil {
		return nil, fmt.Errorf("invalid AGENT_RECOVERY_ENABLED: %w", err)
	}
	cfg.Recovery.Enabled = recoveryEnabled
	cfg.Recovery.ClientID = getEnv("AGENT_CLIENT_ID", "")
	cfg.Recovery.Token = getEnv("AGENT_RECOVERY_TOKEN", "")
	cfg.Recovery.StatePath = getEnv("AGENT_RECOVERY_STATE_PATH", "./recovery-state.db")
	cfg.Recovery.MaxBodyBytes = int64(getEnvInt("AGENT_RECOVERY_MAX_BODY_BYTES", 1024*1024))
	cfg.Recovery.MaxColumns = getEnvInt("AGENT_RECOVERY_MAX_COLUMNS", 64)
	cfg.Recovery.MaxClockSkewSeconds = getEnvInt("AGENT_RECOVERY_MAX_CLOCK_SKEW_SECONDS", 300)
	cfg.Recovery.RequestTimeoutSeconds = getEnvInt("AGENT_RECOVERY_REQUEST_TIMEOUT_SECONDS", 15)
	cfg.Recovery.RetentionDays = getEnvInt("AGENT_RECOVERY_RETENTION_DAYS", 90)
	cfg.Recovery.PolicyPath = getEnv("AGENT_RECOVERY_POLICY_PATH", "./recovery-policy.yml")
	if err := cfg.Recovery.Validate(cfg.VerifyToken()); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// VerifyToken returns the read-only token without duplicating its storage in
// the recovery package.
func (c *Config) VerifyToken() string {
	return getEnv("AGENT_VERIFY_TOKEN", "")
}

func (c RecoveryConfig) Validate(verifyToken string) error {
	if !c.Enabled {
		return nil
	}
	if c.ClientID == "" {
		return fmt.Errorf("AGENT_CLIENT_ID wajib diisi saat recovery aktif")
	}
	if len([]byte(c.Token)) < 32 {
		return fmt.Errorf("AGENT_RECOVERY_TOKEN harus memiliki minimal 32 byte")
	}
	if verifyToken == "" || c.Token == verifyToken {
		return fmt.Errorf("AGENT_VERIFY_TOKEN dan AGENT_RECOVERY_TOKEN wajib berbeda dan tidak kosong")
	}
	if c.StatePath == "" || c.PolicyPath == "" {
		return fmt.Errorf("AGENT_RECOVERY_STATE_PATH dan AGENT_RECOVERY_POLICY_PATH wajib diisi")
	}
	if c.MaxBodyBytes <= 0 || c.MaxBodyBytes > 16*1024*1024 {
		return fmt.Errorf("AGENT_RECOVERY_MAX_BODY_BYTES berada di luar batas")
	}
	if c.MaxColumns <= 0 || c.MaxColumns > 256 {
		return fmt.Errorf("AGENT_RECOVERY_MAX_COLUMNS berada di luar batas")
	}
	if c.MaxClockSkewSeconds < 0 || c.MaxClockSkewSeconds > 24*60*60 {
		return fmt.Errorf("AGENT_RECOVERY_MAX_CLOCK_SKEW_SECONDS berada di luar batas")
	}
	if c.RequestTimeoutSeconds <= 0 || c.RequestTimeoutSeconds > 10*60 {
		return fmt.Errorf("AGENT_RECOVERY_REQUEST_TIMEOUT_SECONDS berada di luar batas")
	}
	if c.RetentionDays <= 0 {
		return fmt.Errorf("AGENT_RECOVERY_RETENTION_DAYS harus positif")
	}
	return nil
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
