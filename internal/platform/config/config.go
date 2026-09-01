// Package config loads process configuration from the environment. It fails
// fast on anything missing rather than starting a half-configured process that
// only reveals the gap under load.
package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
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

	// ConnectionEncryptionKey is a base64-encoded 32-byte key for the operator
	// SMPP bind passwords. Empty means no bind password can be stored — the
	// deployment can describe its connections and cannot enable one, which is a
	// safer failure than storing a carrier credential in plaintext.
	ConnectionEncryptionKey string

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

	// RCS carrier credentials. All optional and per-vendor: a deployment
	// configures Airtel IQ or Vi RBM, or neither, in which case capability
	// discovery answers "not configured" instead of guessing.
	//
	// RCSVendor picks between them and is only needed when both are present.
	// The two are not interchangeable at the credential level — Airtel issues a
	// static Basic blob that never expires, Vi issues OAuth client credentials
	// that mint a rate-limited token — so there is no single set of fields that
	// could serve both.
	RCSVendor string

	RCSAirtelBaseURL   string
	RCSAirtelAuthToken string
	RCSAirtelAgentID   string
	// The account the agent hangs off. Capability discovery does not want
	// these; template registration and send both refuse without them.
	RCSAirtelCustomerID   string
	RCSAirtelSubAccountID string

	RCSViBaseURL      string
	RCSViTokenURL     string
	RCSViClientID     string
	RCSViClientSecret string
	RCSViBotID        string

	// CarrierWebhookToken authenticates the RCS delivery and template
	// callbacks. Empty leaves those routes unmounted, which is right for any
	// deployment no carrier is calling. See internal/api/carrier_webhooks.go
	// for why the secret travels in the path.
	CarrierWebhookToken string

	// CarrierWebhookIPAllowlist restricts who may post one. Airtel documents IP
	// whitelisting in both directions; this is our half.
	CarrierWebhookIPAllowlist string

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
	cfg.RCSVendor = strings.ToLower(strings.TrimSpace(os.Getenv("RCS_VENDOR")))
	cfg.RCSAirtelBaseURL = strings.TrimRight(strings.TrimSpace(os.Getenv("RCS_AIRTEL_BASE_URL")), "/")
	cfg.RCSAirtelAuthToken = strings.TrimSpace(os.Getenv("RCS_AIRTEL_AUTH_TOKEN"))
	cfg.RCSAirtelAgentID = strings.TrimSpace(os.Getenv("RCS_AIRTEL_AGENT_ID"))
	cfg.RCSAirtelCustomerID = strings.TrimSpace(os.Getenv("RCS_AIRTEL_CUSTOMER_ID"))
	cfg.RCSAirtelSubAccountID = strings.TrimSpace(os.Getenv("RCS_AIRTEL_SUBACCOUNT_ID"))
	cfg.RCSViBaseURL = strings.TrimRight(strings.TrimSpace(os.Getenv("RCS_VI_BASE_URL")), "/")
	cfg.RCSViTokenURL = strings.TrimSpace(os.Getenv("RCS_VI_TOKEN_URL"))
	cfg.RCSViClientID = strings.TrimSpace(os.Getenv("RCS_VI_CLIENT_ID"))
	cfg.RCSViClientSecret = strings.TrimSpace(os.Getenv("RCS_VI_CLIENT_SECRET"))
	cfg.RCSViBotID = strings.TrimSpace(os.Getenv("RCS_VI_BOT_ID"))
	cfg.CarrierWebhookToken = strings.TrimSpace(os.Getenv("RCS_WEBHOOK_TOKEN"))
	cfg.CarrierWebhookIPAllowlist = strings.TrimSpace(os.Getenv("RCS_WEBHOOK_IP_ALLOWLIST"))
	if err := cfg.validateRCS(); err != nil {
		return Config{}, err
	}

	cfg.SignupInviteCode = strings.TrimSpace(os.Getenv("SIGNUP_INVITE_CODE"))
	cfg.ConnectionEncryptionKey = strings.TrimSpace(os.Getenv("CONNECTION_ENCRYPTION_KEY"))
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

// RCSCarrierName is the vendor whose capability API this deployment will use,
// or "" when none is configured.
//
// Credentials arriving one field short is the failure this exists to catch. A
// half-configured Airtel — base URL and token but no agent id — would build a
// checker that returns ErrRCSNotConfigured on every call, and the endpoint
// would report "no RCS carrier" on a deployment whose operator had just
// finished setting one up. Refusing to start says which field is missing.
func (c Config) RCSCarrierName() string {
	airtel := c.airtelConfigured()
	vi := c.RCSViBaseURL != "" || c.RCSViTokenURL != "" || c.RCSViClientID != "" ||
		c.RCSViClientSecret != "" || c.RCSViBotID != ""

	switch {
	case c.RCSVendor != "":
		return c.RCSVendor
	case airtel && !vi:
		return "airtel"
	case vi && !airtel:
		return "vi"
	default:
		return ""
	}
}

func (c Config) validateRCS() error {
	airtelPresent := c.airtelConfigured()
	viPresent := c.RCSViBaseURL != "" || c.RCSViTokenURL != "" || c.RCSViClientID != "" ||
		c.RCSViClientSecret != "" || c.RCSViBotID != ""

	switch c.RCSVendor {
	case "", "airtel", "vi":
	default:
		return fmt.Errorf("config: RCS_VENDOR=%q is not a carrier; use airtel or vi", c.RCSVendor)
	}
	// Both sets of credentials with nothing to choose between them is not a
	// default worth guessing: the two carriers answer the same question
	// differently for the same handset, and picking silently would make the
	// product's answer depend on field order in this file.
	if c.RCSVendor == "" && airtelPresent && viPresent {
		return errors.New("config: both Airtel and Vi RCS credentials are set; " +
			"set RCS_VENDOR to airtel or vi to say which one answers capability checks")
	}

	if c.RCSCarrierName() == "airtel" || (c.RCSVendor == "" && airtelPresent) {
		missing := missingFields(map[string]string{
			"RCS_AIRTEL_BASE_URL":      c.RCSAirtelBaseURL,
			"RCS_AIRTEL_AUTH_TOKEN":    c.RCSAirtelAuthToken,
			"RCS_AIRTEL_AGENT_ID":      c.RCSAirtelAgentID,
			"RCS_AIRTEL_CUSTOMER_ID":   c.RCSAirtelCustomerID,
			"RCS_AIRTEL_SUBACCOUNT_ID": c.RCSAirtelSubAccountID,
		})
		if len(missing) > 0 {
			return fmt.Errorf("config: Airtel RCS is selected but %s missing",
				strings.Join(missing, ", ")+" is")
		}
	}
	if c.RCSCarrierName() == "vi" || (c.RCSVendor == "" && viPresent) {
		missing := missingFields(map[string]string{
			"RCS_VI_BASE_URL":      c.RCSViBaseURL,
			"RCS_VI_TOKEN_URL":     c.RCSViTokenURL,
			"RCS_VI_CLIENT_ID":     c.RCSViClientID,
			"RCS_VI_CLIENT_SECRET": c.RCSViClientSecret,
			"RCS_VI_BOT_ID":        c.RCSViBotID,
		})
		if len(missing) > 0 {
			return fmt.Errorf("config: Vi RCS is selected but %s missing",
				strings.Join(missing, ", ")+" is")
		}
	}
	return nil
}

// missingFields returns the empty ones by name, sorted so the message reads the
// same on every boot rather than reshuffling with Go's map iteration.
func missingFields(fields map[string]string) []string {
	missing := make([]string, 0, len(fields))
	for name, value := range fields {
		if value == "" {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

// airtelConfigured reports whether ANY Airtel field is set, which is what makes
// a half-configured carrier detectable at boot rather than at first send.
func (c Config) airtelConfigured() bool {
	return c.RCSAirtelBaseURL != "" || c.RCSAirtelAuthToken != "" ||
		c.RCSAirtelAgentID != "" || c.RCSAirtelCustomerID != "" ||
		c.RCSAirtelSubAccountID != ""
}
