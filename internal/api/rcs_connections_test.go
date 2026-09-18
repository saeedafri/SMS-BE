package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/saeedafri/sms-be/internal/api"
	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/store"
)

// RCS operator accounts live in the database, like SMPP binds: several at
// once, enabled without a deploy, and every secret sealed.

// seedRCSConnection writes one account and returns it. Secrets are sealed with
// the harness's own key, the way the CLI seals them.
func (h *harness) seedRCSConnection(t *testing.T, vendor, status string,
	settings map[string]string, secrets api.RCSSecrets) store.RCSConnection {

	t.Helper()
	ctx := context.Background()
	plain, err := json.Marshal(secrets)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := h.server.Secrets.Encrypt(string(plain))
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateRCSConnection(ctx, h.operatorPool, store.RCSConnection{
		Label: vendor + " test account", Vendor: vendor, Environment: h.server.SMPPEnvironment,
		Settings: settings, SecretsSealed: &sealed,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = h.operatorPool.Exec(ctx, `DELETE FROM rcs_connections WHERE id = $1`, created.ID)
	})
	if status == "active" {
		if created, err = store.SetRCSConnectionStatus(ctx, h.operatorPool, created.ID, "active"); err != nil {
			t.Fatal(err)
		}
	}
	return created
}

// rcsHarness is a send harness with the router in place and no carrier named
// in the environment, which is what a deployment looks like now.
func rcsHarness(t *testing.T) *harness {
	t.Helper()
	h := newSendHarness(t)
	// The environment is a two-value enum, so these rows share "test" with any
	// other account in the test database; each test cleans its own up.
	h.server.SMPPEnvironment = "test"
	h.server.RCSCarrier = nil
	h.server.RCS = &connector.RCSRouter{}
	h.server.Carriers = connector.Registry{Default: h.server.Connector,
		ByChannel: map[string]connector.Connector{"RCS": h.server.RCS}}
	return h
}

func TestAnEnabledJioAccountBecomesAnRCSOperatorWithinAReload(t *testing.T) {
	h := rcsHarness(t)
	ctx := context.Background()

	// Disabled first: an account nobody enabled must not carry traffic.
	disabled := h.seedRCSConnection(t, "jio", "disabled", nil,
		api.RCSSecrets{Assistants: map[string]string{"assistant-1": "secret-1"}})
	if err := h.server.ReloadRCSConnections(ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.server.RCS.Carriers()) != 0 {
		t.Fatalf("a disabled account was loaded: %v", h.server.RCS.Carriers())
	}

	if _, err := store.SetRCSConnectionStatus(ctx, h.operatorPool, disabled.ID, "active"); err != nil {
		t.Fatal(err)
	}
	if err := h.server.ReloadRCSConnections(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(h.server.RCS.Carriers(), ",") != "JIO" {
		t.Fatalf("operators = %v, want JIO", h.server.RCS.Carriers())
	}
	gateway, ok := h.server.RCS.For("JIO")
	if !ok || gateway.Vendor() != "jio" {
		t.Fatalf("the JIO gateway is %v", gateway)
	}
	// The hosts default to Jio's published ones, so an account can be added
	// without copying two URLs out of the guide.
	jio, isJio := gateway.(*connector.JioRCS)
	if !isJio || jio.BaseURL != api.JioBaseURL || jio.TokenURL != api.JioTokenURL {
		t.Errorf("gateway = %#v, want Jio's production hosts by default", gateway)
	}

	reloaded, err := store.GetRCSConnection(ctx, h.operatorPool, disabled.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.HealthStatus != "ok" || reloaded.LastError != nil {
		t.Errorf("health = %s %v, want ok", reloaded.HealthStatus, reloaded.LastError)
	}
}

// An account whose configuration is incomplete is reported on the row rather
// than taking the whole reload down with it.
func TestAnUnusableAccountIsReportedAndLeftOut(t *testing.T) {
	h := rcsHarness(t)
	ctx := context.Background()
	broken := h.seedRCSConnection(t, "vi", "active",
		map[string]string{"baseUrl": "https://api.virbm.test", "tokenUrl": "https://auth.virbm.test"},
		api.RCSSecrets{}) // no clientId setting and no client secret
	working := h.seedRCSConnection(t, "jio", "active", nil,
		api.RCSSecrets{Assistants: map[string]string{"assistant-1": "secret-1"}})

	if err := h.server.ReloadRCSConnections(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(h.server.RCS.Carriers(), ",") != "JIO" {
		t.Fatalf("operators = %v, want the working one only", h.server.RCS.Carriers())
	}
	stored, err := store.GetRCSConnection(ctx, h.operatorPool, broken.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.HealthStatus != "error" || stored.LastError == nil ||
		!strings.Contains(*stored.LastError, "clientId") {
		t.Errorf("health = %s %v, want an error naming what is missing",
			stored.HealthStatus, stored.LastError)
	}
	if _, working := h.server.RCS.For(connector.RCSIntegrations[working.Vendor]); !working {
		t.Error("the working account was dropped along with the broken one")
	}
}

// The stored secrets never leave the server, and nothing but the box can read
// them back.
func TestRCSSecretsAreSealedAtRest(t *testing.T) {
	h := rcsHarness(t)
	ctx := context.Background()
	created := h.seedRCSConnection(t, "jio", "active", nil,
		api.RCSSecrets{Assistants: map[string]string{"assistant-1": "sIgnature-secret"}})

	var sealed string
	if err := h.operatorPool.QueryRow(ctx,
		`SELECT secrets_sealed FROM rcs_connections WHERE id = $1`, created.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "sIgnature-secret") || strings.Contains(sealed, "assistant-1") {
		t.Fatal("the stored secrets are readable in the database")
	}
	opened, err := h.server.OpenRCSSecrets(created)
	if err != nil || opened.Assistants["assistant-1"] != "sIgnature-secret" {
		t.Errorf("opened = %v, %v", opened.Assistants, err)
	}
}

// Two live accounts for one operator would leave "which account sent this" to
// chance, and a delivery report could not say.
func TestOnlyOneAccountPerOperatorCanBeActive(t *testing.T) {
	t.Parallel()
	h := rcsHarness(t)
	ctx := context.Background()
	h.seedRCSConnection(t, "jio", "active", nil,
		api.RCSSecrets{Assistants: map[string]string{"a": "b"}})
	second := h.seedRCSConnection(t, "jio", "disabled", nil,
		api.RCSSecrets{Assistants: map[string]string{"c": "d"}})

	if _, err := store.SetRCSConnectionStatus(ctx, h.operatorPool, second.ID, "active"); err != store.ErrConflict {
		t.Fatalf("enabling a second jio account = %v, want a conflict", err)
	}
}

// A carrier named in the environment still wins, so a deployment configured
// the old way is untouched by any of this.
func TestTheEnvironmentsCarrierStillWins(t *testing.T) {
	h := rcsHarness(t)
	h.server.RCSCarrier = &stubCarrier{vendor: "airtel"}
	h.seedRCSConnection(t, "jio", "active", nil, api.RCSSecrets{Assistants: map[string]string{"a": "b"}})
	if err := h.server.ReloadRCSConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	if operators := strings.Join(h.server.RCSOperators(), ","); operators != "AIRTEL" {
		t.Errorf("operators = %s, want only the environment's carrier", operators)
	}
}
