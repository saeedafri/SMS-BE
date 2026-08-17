// Package config loads process configuration from the environment. It fails
// fast on anything missing rather than starting a half-configured process that
// only reveals the gap under load.
package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	DatabaseURL      string
	DatabaseAdminURL string
	ClickHouseURL    string
	RedisURL         string
	ControlAPIAddr   string
	JWTSigningKey    string
	LogLevel         string

	// Transactional email. All three are optional and travel together: with no
	// ResendAPIKey the mailer logs instead of sending, which is what local
	// development and the test suite want — neither should be able to put real
	// mail in a real inbox by accident.
	//
	// AppBaseURL is the FRONTEND's origin, not this API's. It builds the links
	// in those emails, and they must land on the pages that read ?token= —
	// pointing them at the API would send every recipient to a 404.
	ResendAPIKey string
	MailFrom     string
	AppBaseURL   string

	// EnableDevEndpoints exposes /v1/dev/*, the test hooks the browser suite
	// needs to force state a real carrier would take hours to produce —
	// approving a sender, ageing a campaign, emptying a wallet, demoting the
	// caller. They default to OFF and must stay off in production: one of them
	// changes the caller's own role, and another zeroes a balance.
	EnableDevEndpoints bool
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
		ResendAPIKey:     os.Getenv("RESEND_API_KEY"),
		MailFrom:         envOr("MAIL_FROM", "Relay <noreply@saqibsaeed.cloud>"),
		AppBaseURL:       strings.TrimRight(envOr("APP_BASE_URL", "http://localhost:3000"), "/"),
	}

	// Opt-in, and off unless asked for — these endpoints mutate roles and
	// balances, so the default must be the safe one.
	//
	// It used to accept the exact string "true" and nothing else, on the
	// reasoning that a typo should leave the hooks off. The safety was right;
	// the silence was not. Starting the API with ENABLE_DEV_ENDPOINTS=1 — the
	// most ordinary way anyone writes a boolean — disabled every hook without a
	// word, and the browser suite ran a full 26 minutes with all its state
	// resets quietly 404ing. It reported 27 fewer passes and looked exactly
	// like a code regression.
	//
	// So: accept what people actually type, and refuse to start on anything
	// else. An unrecognised value is a misconfiguration, and this package's
	// whole stance is to fail fast rather than run half-configured.
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ENABLE_DEV_ENDPOINTS"))) {
	case "":
		cfg.EnableDevEndpoints = false
	case "true", "1", "yes", "on":
		cfg.EnableDevEndpoints = true
	case "false", "0", "no", "off":
		cfg.EnableDevEndpoints = false
	default:
		return Config{}, fmt.Errorf(
			"config: ENABLE_DEV_ENDPOINTS=%q is not a boolean; use true or false",
			os.Getenv("ENABLE_DEV_ENDPOINTS"))
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
