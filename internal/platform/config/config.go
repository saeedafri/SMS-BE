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

	// SignupInviteCode gates public self-registration.
	//
	// Empty means signup is open to anyone, which is the right default for a
	// development instance and the wrong one for a deployment on the public
	// internet: a stranger self-registering was one half of a go-live blocker
	// reported on 2026-08-21 (the other half being that they could then fund
	// their own wallet for nothing). Set it and callers must present the same
	// value to create an account.
	SignupInviteCode string

	// AllowGreyRoutes permits enabling a route whose compliance standing is
	// grey — traffic that reaches handsets without being registered with the
	// operator behind it.
	//
	// Off by default, and it must stay off anywhere real traffic runs. A grey
	// route delivers until the carrier notices, and then it does not: messages
	// are filtered without a report, the sender id is blocked, and in India
	// the penalty lands on the principal entity, not on us. Two of them were
	// found ACTIVE on the production deployment on 2026-08-21 with registered
	// alternatives sitting right beside them in the same corridor.
	AllowGreyRoutes bool

	// OperatorIPAllowlist restricts /v1/operator to known networks: addresses
	// or CIDRs, comma separated. Empty means no restriction, which is right for
	// development and wrong on the public internet — an operator account sees
	// every customer, so that surface should not be reachable from everywhere.
	OperatorIPAllowlist string
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

	// Both opt-in, and off unless asked for: one mutates roles and balances,
	// the other permits unregistered carrier traffic. The default has to be the
	// safe one for each.
	var err error
	if cfg.EnableDevEndpoints, err = boolEnv("ENABLE_DEV_ENDPOINTS"); err != nil {
		return Config{}, err
	}
	if cfg.AllowGreyRoutes, err = boolEnv("ALLOW_GREY_ROUTES"); err != nil {
		return Config{}, err
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
	cfg.SignupInviteCode = strings.TrimSpace(os.Getenv("SIGNUP_INVITE_CODE"))
	cfg.OperatorIPAllowlist = strings.TrimSpace(os.Getenv("OPERATOR_IP_ALLOWLIST"))

	return cfg, nil
}

// boolEnv reads an opt-in flag, defaulting to off and refusing anything that is
// not a boolean.
//
// It used to accept the exact string "true" and nothing else, on the reasoning
// that a typo should leave the feature off. The safety was right; the silence
// was not. Starting the API with ENABLE_DEV_ENDPOINTS=1 — the most ordinary way
// anyone writes a boolean — disabled every dev hook without a word, and the
// browser suite ran a full 26 minutes with all its state resets quietly 404ing.
// It reported 27 fewer passes and looked exactly like a code regression.
//
// So: accept what people actually type, and refuse to start on anything else.
// An unrecognised value is a misconfiguration, and this package's whole stance
// is to fail fast rather than run half-configured.
func boolEnv(name string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "":
		return false, nil
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("config: %s=%q is not a boolean; use true or false",
			name, os.Getenv(name))
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
