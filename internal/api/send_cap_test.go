package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// capTenant gives a tenant a daily send ceiling.
func capTenant(t *testing.T, h *harness, tenant uuid.UUID, perDay int) {
	t.Helper()
	if _, err := h.admin.Exec(context.Background(),
		`UPDATE tenants SET send_cap_per_day = $2 WHERE id = $1`, tenant, perDay); err != nil {
		t.Fatalf("set send cap: %v", err)
	}
}

// A ceiling that only covered campaigns would be lifted by a loop around
// POST /v1/messages — the first thing anybody who noticed it would try.
//
// And it must give nothing away. The refusal is the SAME 429 the send-rate
// budget already answers with, because a customer who can tell the two apart
// can work out that a ceiling exists, read its size from the point it starts
// refusing, and open an argument about a number we never published.
func TestTheSingleSendPathIsCappedToo(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)
	secret := h.apiKey(tenant, []string{"send:sms"})
	capTenant(t, h, tenant.TenantID, 1)

	send := func() response {
		return h.do(http.MethodPost, "/v1/messages", secret, map[string]any{
			"senderId": sender, "templateId": template,
			"to": "9876543210", "body": "Your order has shipped.",
		})
	}

	if first := send(); first.Code != http.StatusAccepted {
		t.Fatalf("the first send = %d, want 202 — it is inside the ceiling\n%s",
			first.Code, string(first.Body))
	}
	refused := send()
	if refused.Code != http.StatusTooManyRequests {
		t.Fatalf("the second send = %d, want 429: the ceiling of 1 is spent\n%s",
			refused.Code, string(refused.Body))
	}

	// Nothing in the answer may name the ceiling, its size, or the fact that
	// this tenant has one at all.
	body := strings.ToLower(string(refused.Body))
	for _, giveaway := range []string{"cap", "ceiling", "daily", "per day", "quota", "allowance"} {
		if strings.Contains(body, giveaway) {
			t.Errorf("the refusal says %q, which tells the customer a ceiling exists:\n%s",
				giveaway, string(refused.Body))
		}
	}
	// And it must not point them at a budget page that will disagree with it.
	if strings.Contains(body, "rate-limit") {
		t.Errorf("the refusal sends them to a budget they have not exceeded:\n%s", string(refused.Body))
	}
}

// An uncapped tenant — the default, and almost everybody — sends as before.
func TestAnUncappedTenantSendsAsBefore(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)
	secret := h.apiKey(tenant, []string{"send:sms"})

	for i := range 3 {
		sent := h.do(http.MethodPost, "/v1/messages", secret, map[string]any{
			"senderId": sender, "templateId": template,
			"to": "9876543210", "body": "Your order has shipped.",
		})
		if sent.Code != http.StatusAccepted {
			t.Fatalf("send %d = %d, want 202 — this tenant has no ceiling\n%s",
				i+1, sent.Code, string(sent.Body))
		}
	}
}
