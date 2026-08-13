package config

import "testing"

func setValidEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/sms_test")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("JWT_SIGNING_KEY", "test")
}

func TestLoadRejectsMissingRequiredValue(t *testing.T) {
	setValidEnv(t)
	t.Setenv("DATABASE_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected an error when DATABASE_URL is unset, got nil")
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	setValidEnv(t)
	t.Setenv("CONTROL_API_ADDR", "")
	t.Setenv("LOG_LEVEL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned an unexpected error: %v", err)
	}
	if cfg.ControlAPIAddr != ":8080" {
		t.Fatalf("ControlAPIAddr = %q, want %q", cfg.ControlAPIAddr, ":8080")
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
}

// ClickHouse arrives in Stage 5. Until then its absence must not stop the
// process from starting.
func TestLoadDoesNotRequireClickHouse(t *testing.T) {
	setValidEnv(t)
	t.Setenv("CLICKHOUSE_URL", "")

	if _, err := Load(); err != nil {
		t.Fatalf("Load should tolerate an unset CLICKHOUSE_URL, got: %v", err)
	}
}
