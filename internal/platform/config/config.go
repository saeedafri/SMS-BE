// Package config loads process configuration from the environment. It fails
// fast on anything missing rather than starting a half-configured process that
// only reveals the gap under load.
package config

import (
	"fmt"
	"os"
)

type Config struct {
	DatabaseURL      string
	DatabaseAdminURL string
	ClickHouseURL    string
	RedisURL         string
	ControlAPIAddr   string
	JWTSigningKey    string
	LogLevel         string
}

// ClickHouseURL is deliberately not required: message logs arrive in Stage 5,
// and until then the process runs correctly without it.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		DatabaseAdminURL: os.Getenv("DATABASE_ADMIN_URL"),
		ClickHouseURL:    os.Getenv("CLICKHOUSE_URL"),
		RedisURL:         os.Getenv("REDIS_URL"),
		ControlAPIAddr:   envOr("CONTROL_API_ADDR", ":8080"),
		JWTSigningKey:    os.Getenv("JWT_SIGNING_KEY"),
		LogLevel:         envOr("LOG_LEVEL", "info"),
	}
	required := map[string]string{
		"DATABASE_URL":    cfg.DatabaseURL,
		"REDIS_URL":       cfg.RedisURL,
		"JWT_SIGNING_KEY": cfg.JWTSigningKey,
	}
	for name, value := range required {
		if value == "" {
			return Config{}, fmt.Errorf("config: %s is required", name)
		}
	}
	return cfg, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
