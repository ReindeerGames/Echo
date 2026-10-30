package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

type Config struct {
	HTTP        HTTP
	Database    Database
	WhatsApp    WhatsApp
	GitHub      GitHub
	Monitor     Monitor
	AI          AI
	Assistant   Assistant
	Worker      Worker
	Security    Security
	Environment string
}

type HTTP struct {
	Address       string
	PublicBaseURL string
	ReadTimeout   time.Duration
	WriteTimeout  time.Duration
	IdleTimeout   time.Duration
}

type Database struct {
	CoreDSN     string
	WhatsAppDSN string
}

type WhatsApp struct {
	Enabled       bool
	OwnerJID      string
	DeviceName    string
	HistorySync   bool
	ContextWindow int
}

type GitHub struct {
	APIBaseURL    string
	WebhookSecret string
}

type Monitor struct {
	WebhookSecret    string
	WatchdogInterval time.Duration
}

type AI struct {
	Enabled bool
	BaseURL string
	APIKey  string
	Model   string
}

type Assistant struct {
	HistoryLimit int
	Retention    time.Duration
}

type Worker struct {
	Enabled           bool
	PollInterval      time.Duration
	JobLease          time.Duration
	HeartbeatInterval time.Duration
	HeartbeatTimeout  time.Duration
	QueueStallTimeout time.Duration
	WorkspaceRoot     string
	MaxRepoBytes      int64
	MaxFileBytes      int64
}

type Security struct {
	AdminToken    string
	SessionSecret string
	EncryptionKey string
	CookieSecure  bool
	ApprovalTTL   time.Duration
	DevMode       bool
}

func Load() (Config, error) {
	database, err := loadDatabase()
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Environment: env("ECHO_ENV", "production"),
		HTTP: HTTP{
			Address:       env("ECHO_HTTP_ADDRESS", ":8080"),
			PublicBaseURL: strings.TrimRight(env("ECHO_PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
			ReadTimeout:   duration("ECHO_HTTP_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:  duration("ECHO_HTTP_WRITE_TIMEOUT", 30*time.Second),
			IdleTimeout:   duration("ECHO_HTTP_IDLE_TIMEOUT", 60*time.Second),
		},
		Database: database,
		WhatsApp: WhatsApp{
			Enabled:       boolean("ECHO_WHATSAPP_ENABLED", true),
			OwnerJID:      strings.TrimSpace(os.Getenv("ECHO_OWNER_JID")),
			DeviceName:    env("ECHO_WHATSAPP_DEVICE_NAME", "Echo"),
			HistorySync:   boolean("ECHO_WHATSAPP_HISTORY_SYNC", false),
			ContextWindow: integer("ECHO_WHATSAPP_CONTEXT_WINDOW", 5),
		},
		GitHub: GitHub{
			APIBaseURL:    strings.TrimRight(env("ECHO_GITHUB_API_URL", "https://api.github.com"), "/"),
			WebhookSecret: strings.TrimSpace(os.Getenv("ECHO_GITHUB_WEBHOOK_SECRET")),
		},
		Monitor: Monitor{
			WebhookSecret:    strings.TrimSpace(os.Getenv("ECHO_MONITOR_WEBHOOK_SECRET")),
			WatchdogInterval: duration("ECHO_MONITOR_WATCHDOG_INTERVAL", 30*time.Second),
		},
		AI: AI{
			Enabled: boolean("ECHO_AI_ENABLED", false),
			BaseURL: strings.TrimRight(env("ECHO_AI_BASE_URL", "https://api.openai.com/v1"), "/"),
			APIKey:  strings.TrimSpace(os.Getenv("ECHO_AI_API_KEY")),
			Model:   env("ECHO_AI_MODEL", "gpt-5-mini"),
		},
		Assistant: Assistant{
			HistoryLimit: integer("ECHO_ASSISTANT_HISTORY_LIMIT", 12),
			Retention:    duration("ECHO_ASSISTANT_RETENTION", 30*24*time.Hour),
		},
		Worker: Worker{
			Enabled:           boolean("ECHO_WORKER_ENABLED", true),
			PollInterval:      duration("ECHO_WORKER_POLL_INTERVAL", 2*time.Second),
			JobLease:          duration("ECHO_WORKER_JOB_LEASE", 10*time.Minute),
			HeartbeatInterval: duration("ECHO_WORKER_HEARTBEAT_INTERVAL", 15*time.Second),
			HeartbeatTimeout:  duration("ECHO_WORKER_HEARTBEAT_TIMEOUT", time.Minute),
			QueueStallTimeout: duration("ECHO_WORKER_QUEUE_STALL_TIMEOUT", 5*time.Minute),
			WorkspaceRoot:     env("ECHO_WORKSPACE_ROOT", "/tmp/echo-workers"),
			MaxRepoBytes:      int64(integer("ECHO_MAX_REPO_MIB", 256)) * 1024 * 1024,
			MaxFileBytes:      int64(integer("ECHO_MAX_FILE_KIB", 256)) * 1024,
		},
		Security: Security{
			AdminToken:    strings.TrimSpace(os.Getenv("ECHO_ADMIN_TOKEN")),
			SessionSecret: strings.TrimSpace(os.Getenv("ECHO_SESSION_SECRET")),
			EncryptionKey: strings.TrimSpace(os.Getenv("ECHO_ENCRYPTION_KEY")),
			CookieSecure:  boolean("ECHO_COOKIE_SECURE", true),
			ApprovalTTL:   duration("ECHO_APPROVAL_TTL", 30*time.Minute),
			DevMode:       boolean("ECHO_DEV_MODE", false),
		},
	}

	if !cfg.Security.DevMode {
		if len(cfg.Security.AdminToken) < 20 {
			return Config{}, errors.New("ECHO_ADMIN_TOKEN must contain at least 20 characters")
		}
		if len(cfg.Security.SessionSecret) < 32 {
			return Config{}, errors.New("ECHO_SESSION_SECRET must contain at least 32 characters")
		}
	}
	if cfg.Security.EncryptionKey == "" && cfg.Environment == "development" {
		cfg.Security.EncryptionKey = cfg.Security.SessionSecret
	}
	if len(cfg.Security.EncryptionKey) < 32 {
		return Config{}, errors.New("ECHO_ENCRYPTION_KEY must contain at least 32 characters")
	}
	if cfg.Assistant.HistoryLimit < 4 || cfg.Assistant.HistoryLimit > 50 {
		return Config{}, errors.New("ECHO_ASSISTANT_HISTORY_LIMIT must be between 4 and 50")
	}
	if cfg.Assistant.Retention < 24*time.Hour || cfg.Assistant.Retention > 365*24*time.Hour {
		return Config{}, errors.New("ECHO_ASSISTANT_RETENTION must be between 24h and 8760h")
	}
	if cfg.Monitor.WatchdogInterval < 5*time.Second || cfg.Monitor.WatchdogInterval > 5*time.Minute {
		return Config{}, errors.New("ECHO_MONITOR_WATCHDOG_INTERVAL must be between 5s and 5m")
	}
	if cfg.Worker.PollInterval < 100*time.Millisecond || cfg.Worker.PollInterval > time.Minute {
		return Config{}, errors.New("ECHO_WORKER_POLL_INTERVAL must be between 100ms and 1m")
	}
	if cfg.Worker.JobLease < 5*time.Second || cfg.Worker.JobLease > time.Hour {
		return Config{}, errors.New("ECHO_WORKER_JOB_LEASE must be between 5s and 1h")
	}
	if cfg.Worker.HeartbeatInterval < 5*time.Second || cfg.Worker.HeartbeatInterval > time.Minute {
		return Config{}, errors.New("ECHO_WORKER_HEARTBEAT_INTERVAL must be between 5s and 1m")
	}
	minimumHeartbeatTimeout := 2 * cfg.Worker.HeartbeatInterval
	if pollMinimum := 2 * cfg.Worker.PollInterval; pollMinimum > minimumHeartbeatTimeout {
		minimumHeartbeatTimeout = pollMinimum
	}
	if cfg.Worker.HeartbeatTimeout < minimumHeartbeatTimeout || cfg.Worker.HeartbeatTimeout > 10*time.Minute {
		return Config{}, errors.New("ECHO_WORKER_HEARTBEAT_TIMEOUT must be at least twice the worker heartbeat and poll intervals, and no more than 10m")
	}
	if cfg.Worker.QueueStallTimeout < cfg.Worker.HeartbeatTimeout || cfg.Worker.QueueStallTimeout > time.Hour {
		return Config{}, errors.New("ECHO_WORKER_QUEUE_STALL_TIMEOUT must be at least the heartbeat timeout and no more than 1h")
	}
	publicURL, err := url.ParseRequestURI(cfg.HTTP.PublicBaseURL)
	if err != nil {
		return Config{}, fmt.Errorf("invalid ECHO_PUBLIC_BASE_URL: %w", err)
	}
	if cfg.Environment == "production" {
		if cfg.Security.DevMode {
			return Config{}, errors.New("ECHO_DEV_MODE cannot be enabled in production")
		}
		if !cfg.Security.CookieSecure || !strings.EqualFold(publicURL.Scheme, "https") {
			return Config{}, errors.New("production requires HTTPS ECHO_PUBLIC_BASE_URL and ECHO_COOKIE_SECURE=true")
		}
	}
	return cfg, nil
}

func loadDatabase() (Database, error) {
	host := strings.TrimSpace(os.Getenv("ECHO_MARIADB_HOST"))
	if host == "" {
		return Database{}, errors.New("ECHO_MARIADB_HOST is required")
	}

	port := 3306
	if value := strings.TrimSpace(os.Getenv("ECHO_MARIADB_PORT")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 65535 {
			return Database{}, errors.New("ECHO_MARIADB_PORT must be a number between 1 and 65535")
		}
		port = parsed
	}

	tlsMode := strings.ToLower(env("ECHO_MARIADB_TLS", "true"))
	switch tlsMode {
	case "false", "true", "skip-verify", "preferred":
	default:
		return Database{}, errors.New("ECHO_MARIADB_TLS must be one of false, true, skip-verify, or preferred")
	}

	coreName, err := required("ECHO_CORE_DB_NAME")
	if err != nil {
		return Database{}, err
	}
	coreUser, err := required("ECHO_CORE_DB_USER")
	if err != nil {
		return Database{}, err
	}
	corePassword, err := requiredSecret("ECHO_CORE_DB_PASSWORD")
	if err != nil {
		return Database{}, err
	}
	whatsAppName, err := required("ECHO_WHATSAPP_DB_NAME")
	if err != nil {
		return Database{}, err
	}
	whatsAppUser, err := required("ECHO_WHATSAPP_DB_USER")
	if err != nil {
		return Database{}, err
	}
	whatsAppPassword, err := requiredSecret("ECHO_WHATSAPP_DB_PASSWORD")
	if err != nil {
		return Database{}, err
	}

	address := net.JoinHostPort(host, strconv.Itoa(port))
	return Database{
		CoreDSN:     mariaDBDSN(address, tlsMode, coreName, coreUser, corePassword),
		WhatsAppDSN: mariaDBDSN(address, tlsMode, whatsAppName, whatsAppUser, whatsAppPassword),
	}, nil
}

func mariaDBDSN(address, tlsMode, database, user, password string) string {
	cfg := mysql.NewConfig()
	cfg.User = user
	cfg.Passwd = password
	cfg.Net = "tcp"
	cfg.Addr = address
	cfg.DBName = database
	cfg.Collation = "utf8mb4_bin"
	cfg.Loc = time.UTC
	cfg.ParseTime = true
	cfg.TLSConfig = tlsMode
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 30 * time.Second
	cfg.WriteTimeout = 30 * time.Second
	return cfg.FormatDSN()
}

func required(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func requiredSecret(name string) (string, error) {
	value, exists := os.LookupEnv(name)
	if !exists || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func boolean(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func integer(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func duration(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
