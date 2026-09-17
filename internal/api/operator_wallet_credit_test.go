package api_test

import (
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"
)

// POST /v1/operator/tenants/{id}/wallet/credit: an operator puts money a tenant
// paid outside the product into their wallet.

func creditPath(tenantID string) string {
	return "/v1/operator/tenants/" + tenantID + "/wallet/credit"
}

func inrBalance(t *testing.T, h *harness, token string) int64 {
	t.Helper()
	var balances []struct {
		Currency     string `json:"currency"`
		BalanceMinor int64  `json:"balanceMinor"`
	}
	h.do(http.MethodGet, "/v1/wallet/balances", token, nil).decode(t, &balances)
	for _, b := range balances {
		if b.Currency == "INR" {
			return b.BalanceMinor
		}
	}
	return 0
}

func TestAnOperatorCreditsATenantAndTheTenantSeesTheMoney(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()
	reference := fmt.Sprintf("UTR%d", rand.Int63())
	before := inrBalance(t, h, tenant.Token)

	res := h.do(http.MethodPost, creditPath(tenant.TenantID.String()), operator, map[string]any{
		"currency": "INR", "amountMinor": 5000000, "reference": reference, "note": "Paid on invoice 42",
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("credit = %d %s, want 201", res.Code, res.Body)
	}
	var entry struct {
		Type              string `json:"type"`
		AmountMinor       int64  `json:"amountMinor"`
		Currency          string `json:"currency"`
		BalanceAfterMinor int64  `json:"balanceAfterMinor"`
		Description       string `json:"description"`
	}
	res.decode(t, &entry)
	if entry.Type != "topup" || entry.AmountMinor != 5000000 || entry.Currency != "INR" ||
		entry.BalanceAfterMinor != before+5000000 || entry.Description != "Bank transfer "+reference {
		t.Errorf("entry = %+v, want a 5000000 INR topup described by the reference", entry)
	}
	if after := inrBalance(t, h, tenant.Token); after != before+5000000 {
		t.Errorf("tenant balance = %d, want %d", after, before+5000000)
	}

	audit := h.do(http.MethodGet, "/v1/operator/audit-log?range=90d&limit=50&action=wallet.credit", operator, nil)
	if audit.Code != http.StatusOK || !strings.Contains(string(audit.Body), reference) {
		t.Errorf("no wallet.credit audit row naming %s: %d %s", reference, audit.Code, audit.Body)
	}
}

// The reference is what makes the button safe to press twice.
func TestTheSameReferenceIsCreditedOnlyOnce(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()
	body := map[string]any{"currency": "INR", "amountMinor": 1000, "reference": fmt.Sprintf("UTR%d", rand.Int63())}

	if first := h.do(http.MethodPost, creditPath(tenant.TenantID.String()), operator, body); first.Code != http.StatusCreated {
		t.Fatalf("first credit = %d %s", first.Code, first.Body)
	}
	balance := inrBalance(t, h, tenant.Token)
	second := h.do(http.MethodPost, creditPath(tenant.TenantID.String()), operator, body)
	if second.Code != http.StatusConflict {
		t.Errorf("same reference again = %d %s, want 409", second.Code, second.Body)
	}
	if after := inrBalance(t, h, tenant.Token); after != balance {
		t.Errorf("a refused duplicate moved the balance from %d to %d", balance, after)
	}
}

func TestACreditOutsideTheRulesIsRefusedAndMovesNothing(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()
	valid := func(change map[string]any) map[string]any {
		body := map[string]any{"currency": "INR", "amountMinor": 1000, "reference": fmt.Sprintf("UTR%d", rand.Int63())}
		for k, v := range change {
			if v == nil {
				delete(body, k)
			} else {
				body[k] = v
			}
		}
		return body
	}
	for name, body := range map[string]map[string]any{
		"zero":             valid(map[string]any{"amountMinor": 0}),
		"negative":         valid(map[string]any{"amountMinor": -5}),
		"over one crore":   valid(map[string]any{"amountMinor": 1000000001}),
		"no currency":      valid(map[string]any{"currency": nil}),
		"unknown currency": valid(map[string]any{"currency": "EUR"}),
		"short reference":  valid(map[string]any{"reference": "UTR1"}),
		"no reference":     valid(map[string]any{"reference": nil}),
		"an unknown field": valid(map[string]any{"type": "refund"}),
	} {
		if res := h.do(http.MethodPost, creditPath(tenant.TenantID.String()), operator, body); res.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d %s, want 422", name, res.Code, res.Body)
		}
	}
	if balance := inrBalance(t, h, tenant.Token); balance != 0 {
		t.Errorf("refused credits left a balance of %d", balance)
	}
}

func TestOnlyAnOperatorCanCreditAndOnlyATenantThatExists(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	body := map[string]any{"currency": "INR", "amountMinor": 1000, "reference": fmt.Sprintf("UTR%d", rand.Int63())}

	if res := h.do(http.MethodPost, creditPath(tenant.TenantID.String()), tenant.Token, body); res.Code != http.StatusUnauthorized {
		t.Errorf("tenant crediting itself = %d, want 401", res.Code)
	}
	if res := h.do(http.MethodPost, creditPath("6f1d1f6a-0000-4000-8000-000000000000"), h.operatorToken(), body); res.Code != http.StatusNotFound {
		t.Errorf("unknown tenant = %d, want 404", res.Code)
	}
	if balance := inrBalance(t, h, tenant.Token); balance != 0 {
		t.Errorf("a refused credit left a balance of %d", balance)
	}
}

// An operator has to see the balance before deciding to credit, and after.
func TestAnOperatorReadsATenantsBalances(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()
	path := "/v1/operator/tenants/" + tenant.TenantID.String() + "/wallet"

	// A tenant that has never held money has no balances — an empty list, not
	// a 404: the tenant exists and holds nothing.
	empty := h.do(http.MethodGet, path, operator, nil)
	if empty.Code != http.StatusOK || strings.TrimSpace(string(empty.Body)) != `{"balances":[]}` {
		t.Fatalf("a new tenant's wallet = %d %s, want 200 {\"balances\":[]}", empty.Code, empty.Body)
	}

	h.do(http.MethodPost, creditPath(tenant.TenantID.String()), operator, map[string]any{
		"currency": "INR", "amountMinor": 250000, "reference": fmt.Sprintf("UTR%d", rand.Int63()),
	})
	var wallet struct {
		Balances []struct {
			Currency     string `json:"currency"`
			BalanceMinor int64  `json:"balanceMinor"`
		} `json:"balances"`
	}
	after := h.do(http.MethodGet, path, operator, nil)
	after.decode(t, &wallet)
	if len(wallet.Balances) != 1 || wallet.Balances[0].Currency != "INR" ||
		wallet.Balances[0].BalanceMinor != 250000 {
		t.Errorf("balances after crediting = %+v, want one INR balance of 250000", wallet.Balances)
	}
	// The operator's view and the tenant's own view are the same number.
	if own := inrBalance(t, h, tenant.Token); own != wallet.Balances[0].BalanceMinor {
		t.Errorf("operator sees %d, tenant sees %d", wallet.Balances[0].BalanceMinor, own)
	}

	if res := h.do(http.MethodGet, path, tenant.Token, nil); res.Code != http.StatusUnauthorized {
		t.Errorf("tenant reading through the operator route = %d, want 401", res.Code)
	}
	if res := h.do(http.MethodGet, "/v1/operator/tenants/6f1d1f6a-0000-4000-8000-000000000000/wallet",
		operator, nil); res.Code != http.StatusNotFound {
		t.Errorf("unknown tenant = %d, want 404", res.Code)
	}
}
