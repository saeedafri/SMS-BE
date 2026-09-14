package api_test

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/saeedafri/sms-be/internal/api"
	"github.com/saeedafri/sms-be/internal/domain/billing"
)

// The operator console sees every customer, and it was reachable from the whole
// internet behind one password that had been a constant in the repository.
func TestTheOperatorConsoleIsUnreachableOffTheAllowlist(t *testing.T) {
	h := newHarness(t)
	allowlist, err := api.ParseIPAllowlist("198.51.100.0/24, 203.0.113.7")
	if err != nil {
		t.Fatalf("parse allowlist: %v", err)
	}
	h.router = api.NewRouter(&api.Server{
		DB: h.pool, OperatorDB: h.operatorPool, AdminDB: h.admin,
		EnableDevEndpoints: true, Gateway: billing.ManualGateway{},
		OperatorAllowlist: allowlist,
		Logger:            slog.New(slog.NewJSONHandler(h.logs, nil)),
	})
	credentials := map[string]any{"email": "ops@relay.internal", "password": "relay-ops-dev"}

	// 404 rather than 403: a 403 confirms an operator console lives here, which
	// is the fact worth withholding from a scan.
	off := h.doFrom("192.0.2.44:1234", http.MethodPost, "/v1/operator/login", "", credentials, nil)
	if off.Code != http.StatusNotFound {
		t.Fatalf("operator login from off-network = %d, want 404\n%s", off.Code, off.Body)
	}

	// Inside the range, the console behaves normally — the allowlist decides
	// who may knock, not who gets in.
	on := h.doFrom("198.51.100.7:1234", http.MethodPost, "/v1/operator/login", "", credentials, nil)
	if on.Code == http.StatusNotFound {
		t.Fatalf("operator login from an allowed address 404'd\n%s", on.Body)
	}

	// A single address in the list, not a range.
	single := h.doFrom("203.0.113.7:1234", http.MethodPost, "/v1/operator/login", "", credentials, nil)
	if single.Code == http.StatusNotFound {
		t.Fatalf("operator login from a listed address 404'd\n%s", single.Body)
	}

	// Customers reach the rest of the API from wherever they are.
	customer := h.doFrom("192.0.2.44:1234", http.MethodPost, "/v1/auth/login", "",
		map[string]any{"email": "nobody@example.test", "password": "wrong-password"}, nil)
	if customer.Code == http.StatusNotFound {
		t.Fatalf("a customer login was blocked by the operator allowlist\n%s", customer.Body)
	}
}

// An empty allowlist is no allowlist. That is the development default, and the
// process warns about it at startup rather than pretending it is configured.
func TestAnEmptyAllowlistRestrictsNothing(t *testing.T) {
	h := newHarness(t)
	allowlist, err := api.ParseIPAllowlist("")
	if err != nil {
		t.Fatalf("parse empty allowlist: %v", err)
	}
	h.router = api.NewRouter(&api.Server{
		DB: h.pool, OperatorDB: h.operatorPool, AdminDB: h.admin,
		EnableDevEndpoints: true, Gateway: billing.ManualGateway{},
		OperatorAllowlist: allowlist,
		Logger:            slog.New(slog.NewJSONHandler(h.logs, nil)),
	})
	res := h.doFrom("192.0.2.44:1234", http.MethodPost, "/v1/operator/login", "",
		map[string]any{"email": "ops@relay.internal", "password": "relay-ops-dev"}, nil)
	if res.Code == http.StatusNotFound {
		t.Fatalf("an empty allowlist blocked the console\n%s", res.Body)
	}
}

// A typo must stop the process, not quietly drop a range and leave a hole that
// looks closed.
func TestAMalformedAllowlistIsAnError(t *testing.T) {
	for _, raw := range []string{"not-an-address", "198.51.100.0/33", "198.51.100.1, oops"} {
		if _, err := api.ParseIPAllowlist(raw); err == nil {
			t.Errorf("ParseIPAllowlist(%q) accepted a malformed entry", raw)
		}
	}
}
