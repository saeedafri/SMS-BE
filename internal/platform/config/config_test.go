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

// RCS carrier credentials. Half a credential set is the failure worth catching
// at boot: it builds a carrier that refuses every call, and the endpoint then
// reports "no RCS carrier configured" on a deployment whose operator has just
// finished configuring one.

func setAirtelEnv(t *testing.T) {
	t.Helper()
	t.Setenv("RCS_AIRTEL_BASE_URL", "https://iqconversation.airtel.in/gateway/airtel-xchange")
	t.Setenv("RCS_AIRTEL_AUTH_TOKEN", "dGVzdDp0ZXN0")
	t.Setenv("RCS_AIRTEL_AGENT_ID", "relay_agent")
	// Airtel's account identifiers. Capability discovery does not need them,
	// but template registration and send both refuse without them, so a
	// deployment missing them can check reachability and never send.
	t.Setenv("RCS_AIRTEL_CUSTOMER_ID", "Profile_1")
	t.Setenv("RCS_AIRTEL_SUBACCOUNT_ID", "f5cb2399-0000-479c-0000-f11a3d710000")
}

func setViEnv(t *testing.T) {
	t.Helper()
	t.Setenv("RCS_VI_BASE_URL", "https://api.virbm.in")
	t.Setenv("RCS_VI_TOKEN_URL", "https://auth.virbm.in/auth/oauth/token")
	t.Setenv("RCS_VI_CLIENT_ID", "cid")
	t.Setenv("RCS_VI_CLIENT_SECRET", "secret")
	t.Setenv("RCS_VI_BOT_ID", "OsQ0GwNvUdLTV9Bd")
}

func TestNoRCSCredentialsIsAValidDeployment(t *testing.T) {
	setValidEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v — a deployment without an RCS agreement must still start", err)
	}
	if cfg.RCSCarrierName() != "" {
		t.Errorf("RCSCarrierName = %q, want empty", cfg.RCSCarrierName())
	}
}

func TestASingleCarriersCredentialsSelectItWithoutBeingNamed(t *testing.T) {
	setValidEnv(t)
	setAirtelEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RCSCarrierName() != "airtel" {
		t.Errorf("RCSCarrierName = %q, want airtel", cfg.RCSCarrierName())
	}
}

func TestHalfConfiguredCarrierRefusesToStart(t *testing.T) {
	setValidEnv(t)
	setAirtelEnv(t)
	t.Setenv("RCS_AIRTEL_AGENT_ID", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load accepted Airtel credentials with no agent id")
	}
	// The message has to name the field. "RCS misconfigured" sends someone
	// reading three env files instead of one line.
	if !contains(err.Error(), "RCS_AIRTEL_AGENT_ID") {
		t.Errorf("error = %q, want it to name the missing field", err)
	}
}

// The two carriers give different answers for the same handset, so choosing
// between them silently would make the product's answer depend on the order of
// fields in this package.
func TestBothCarriersConfiguredRequiresSayingWhichOne(t *testing.T) {
	setValidEnv(t)
	setAirtelEnv(t)
	setViEnv(t)

	if _, err := Load(); err == nil {
		t.Fatal("Load picked a carrier on its own when both were configured")
	}

	t.Setenv("RCS_VENDOR", "vi")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RCSCarrierName() != "vi" {
		t.Errorf("RCSCarrierName = %q, want vi", cfg.RCSCarrierName())
	}
}

func TestAnUnknownVendorNameRefusesToStart(t *testing.T) {
	setValidEnv(t)
	setAirtelEnv(t)
	t.Setenv("RCS_VENDOR", "jio")

	if _, err := Load(); err == nil {
		t.Fatal("Load accepted a carrier this build has no client for")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// The account identifiers arrived with the send path, after capability
// discovery had already shipped needing only three fields. A deployment left on
// the old three would answer reachability questions and refuse every send, with
// nothing at boot saying why.
func TestAirtelWithoutItsAccountIdentifiersRefusesToStart(t *testing.T) {
	setValidEnv(t)
	setAirtelEnv(t)
	t.Setenv("RCS_AIRTEL_CUSTOMER_ID", "")
	t.Setenv("RCS_AIRTEL_SUBACCOUNT_ID", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Airtel started with no customer or sub-account id")
	}
	for _, field := range []string{"RCS_AIRTEL_CUSTOMER_ID", "RCS_AIRTEL_SUBACCOUNT_ID"} {
		if !contains(err.Error(), field) {
			t.Errorf("error = %q, want it to name %s", err, field)
		}
	}
}
