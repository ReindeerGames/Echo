package config

import (
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestLoadDevelopmentConfig(t *testing.T) {
	t.Setenv("ECHO_ENV", "development")
	setDatabaseEnv(t)
	t.Setenv("ECHO_WHATSAPP_ENABLED", "false")
	t.Setenv("ECHO_DEV_MODE", "true")
	t.Setenv("ECHO_AI_ENABLED", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.CoreDSN == "" || cfg.WhatsApp.Enabled {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestProductionRequiresHTTPS(t *testing.T) {
	t.Setenv("ECHO_ENV", "production")
	setDatabaseEnv(t)
	t.Setenv("ECHO_WHATSAPP_ENABLED", "false")
	t.Setenv("ECHO_ADMIN_TOKEN", "a-very-long-administrator-token")
	t.Setenv("ECHO_SESSION_SECRET", "a-session-secret-with-at-least-thirty-two-characters")
	t.Setenv("ECHO_PUBLIC_BASE_URL", "http://echo.example.test")
	t.Setenv("ECHO_COOKIE_SECURE", "true")
	if _, err := Load(); err == nil {
		t.Fatal("expected production HTTP URL to be rejected")
	}
}

func TestLoadFailsClosedWithoutAdminSecrets(t *testing.T) {
	setDatabaseEnv(t)
	t.Setenv("ECHO_WHATSAPP_ENABLED", "false")
	t.Setenv("ECHO_DEV_MODE", "false")
	t.Setenv("ECHO_ADMIN_TOKEN", "")
	t.Setenv("ECHO_SESSION_SECRET", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected production secret validation failure")
	}
}

func TestLoadBuildsEscapedDatabaseDSNs(t *testing.T) {
	setDatabaseEnv(t)
	t.Setenv("ECHO_ENV", "development")
	t.Setenv("ECHO_CORE_DB_PASSWORD", " p@ss:/?#[] with spaces ")
	t.Setenv("ECHO_WHATSAPP_ENABLED", "false")
	t.Setenv("ECHO_DEV_MODE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mysql.ParseDSN(cfg.Database.CoreDSN)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Addr != "127.0.0.1:3307" || parsed.DBName != "echo_core" {
		t.Fatalf("unexpected database target: %#v", parsed)
	}
	if parsed.User != "echo_core_user" || parsed.Passwd != " p@ss:/?#[] with spaces " {
		t.Fatal("database credentials were not preserved in the generated DSN")
	}
}

func TestLoadRejectsInvalidMariaDBTLSMode(t *testing.T) {
	setDatabaseEnv(t)
	t.Setenv("ECHO_MARIADB_TLS", "maybe")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid MariaDB TLS mode to be rejected")
	}
}

func TestDevelopmentAllowsPairingBeforeOwnerIsConfigured(t *testing.T) {
	setDatabaseEnv(t)
	t.Setenv("ECHO_ENV", "development")
	t.Setenv("ECHO_WHATSAPP_ENABLED", "true")
	t.Setenv("ECHO_OWNER_JID", "")
	t.Setenv("ECHO_DEV_MODE", "true")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

func TestAssistantRetentionIsBounded(t *testing.T) {
	setDatabaseEnv(t)
	t.Setenv("ECHO_ENV", "development")
	t.Setenv("ECHO_WHATSAPP_ENABLED", "false")
	t.Setenv("ECHO_DEV_MODE", "true")
	t.Setenv("ECHO_ASSISTANT_RETENTION", "1h")
	if _, err := Load(); err == nil {
		t.Fatal("expected unsafe short assistant retention to be rejected")
	}
	t.Setenv("ECHO_ASSISTANT_RETENTION", "168h")
	t.Setenv("ECHO_ASSISTANT_HISTORY_LIMIT", "51")
	if _, err := Load(); err == nil {
		t.Fatal("expected oversized assistant history to be rejected")
	}
}

func TestProductionAllowsOwnerToBeConfiguredInApplication(t *testing.T) {
	setDatabaseEnv(t)
	t.Setenv("ECHO_ENV", "production")
	t.Setenv("ECHO_WHATSAPP_ENABLED", "true")
	t.Setenv("ECHO_OWNER_JID", "")
	t.Setenv("ECHO_ADMIN_TOKEN", "a-very-long-administrator-token")
	t.Setenv("ECHO_SESSION_SECRET", "a-session-secret-with-at-least-thirty-two-characters")
	t.Setenv("ECHO_PUBLIC_BASE_URL", "https://echo.example.test")
	t.Setenv("ECHO_COOKIE_SECURE", "true")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

func setDatabaseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ECHO_MARIADB_HOST", "127.0.0.1")
	t.Setenv("ECHO_MARIADB_PORT", "3307")
	t.Setenv("ECHO_MARIADB_TLS", "false")
	t.Setenv("ECHO_CORE_DB_NAME", "echo_core")
	t.Setenv("ECHO_CORE_DB_USER", "echo_core_user")
	t.Setenv("ECHO_CORE_DB_PASSWORD", "core-password")
	t.Setenv("ECHO_WHATSAPP_DB_NAME", "echo_whatsapp")
	t.Setenv("ECHO_WHATSAPP_DB_USER", "echo_whatsapp_user")
	t.Setenv("ECHO_WHATSAPP_DB_PASSWORD", "whatsapp-password")
	t.Setenv("ECHO_ENCRYPTION_KEY", "test-encryption-key-with-at-least-thirty-two-characters")
}
