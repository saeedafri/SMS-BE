package api_test

import (
	"net/http"
	"testing"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// The dashboard layout fetches all three of these on every render and throws
// on any non-2xx, so these three passing is what makes any screen render.
func TestDashboardLayoutEndpointsAnswerForANewTenant(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	t.Run("wallet balances", func(t *testing.T) {
		res := h.do(http.MethodGet, "/v1/wallet/balances", acct.Token, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
		}
		var balances []gen.WalletBalance
		res.decode(t, &balances)
		if len(balances) != 0 {
			t.Fatalf("got %d balances for a new tenant, want 0", len(balances))
		}
		// It must serialise as [] not null: the UI maps over it.
		if string(res.Body) != "[]\n" && string(res.Body) != "[]" {
			t.Fatalf("body = %q, want an empty JSON array", res.Body)
		}
	})

	t.Run("alerts", func(t *testing.T) {
		res := h.do(http.MethodGet, "/v1/alerts", acct.Token, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
		}
		var rules gen.AlertRules
		res.decode(t, &rules)
		if rules.LowBalance == nil {
			t.Error("lowBalance is null, want an empty array")
		}
		if rules.DeliveryFloor.Enabled || rules.SpendCeiling.Enabled || rules.VolumeCeiling.Enabled {
			t.Error("a rule is enabled on an account that has configured nothing")
		}
		if rules.DeliveryFloor.Recipients == nil || rules.SpendCeiling.Recipients == nil ||
			rules.VolumeCeiling.Recipients == nil {
			t.Error("a recipients list is null, want an empty array")
		}
	})

	t.Run("conversations", func(t *testing.T) {
		res := h.do(http.MethodGet, "/v1/conversations", acct.Token, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
		}
		var page gen.ConversationPage
		res.decode(t, &page)
		if page.Conversations == nil {
			t.Error("conversations is null, want an empty array")
		}
		if page.Total != 0 {
			t.Errorf("total = %d, want 0", page.Total)
		}
		if page.NextCursor != nil {
			t.Errorf("nextCursor = %v, want null", *page.NextCursor)
		}
	})
}

func TestDashboardLayoutEndpointsRequireAuthentication(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/v1/wallet/balances", "/v1/alerts", "/v1/conversations"} {
		if res := h.do(http.MethodGet, path, "", nil); res.Code != http.StatusUnauthorized {
			t.Errorf("%s unauthenticated: status = %d, want 401", path, res.Code)
		}
	}
}
