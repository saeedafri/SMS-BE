package api_test

import (
	"net/http"
	"testing"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func addCard(t *testing.T, h *harness, token, brand, last4 string) gen.PaymentMethod {
	t.Helper()
	res := h.do(http.MethodPost, "/v1/wallet/payment-methods", token,
		map[string]string{"brand": brand, "last4": last4})
	if res.Code != http.StatusCreated {
		t.Fatalf("add card: status = %d, want 201; body = %s", res.Code, res.Body)
	}
	var method gen.PaymentMethod
	res.decode(t, &method)
	return method
}

func topUp(t *testing.T, h *harness, token, currency string, amount int, methodID string) gen.LedgerEntry {
	t.Helper()
	res := h.do(http.MethodPost, "/v1/wallet/topup", token, map[string]any{
		"currency": currency, "amountMinor": amount, "paymentMethodId": methodID,
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("top-up: status = %d, want 201; body = %s", res.Code, res.Body)
	}
	var entry gen.LedgerEntry
	res.decode(t, &entry)
	return entry
}

func TestNewTenantHasNoBalancesOrLedger(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	balances := h.do(http.MethodGet, "/v1/wallet/balances", acct.Token, nil)
	var list []gen.WalletBalance
	balances.decode(t, &list)
	if len(list) != 0 {
		t.Fatalf("a new tenant has %d balances, want 0", len(list))
	}

	ledger := h.do(http.MethodGet, "/v1/wallet/ledger", acct.Token, nil)
	var page gen.LedgerPage
	ledger.decode(t, &page)
	if page.Entries == nil {
		t.Error("entries is null, want an empty array")
	}
}

func TestTopUpCreatesTheWalletAndCreditsIt(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	card := addCard(t, h, acct.Token, "visa", "4242")

	entry := topUp(t, h, acct.Token, "INR", 250_000, card.Id)

	if entry.Type != gen.LedgerEntryType("topup") {
		t.Errorf("type = %q, want topup", entry.Type)
	}
	// The contract says amountMinor is always positive; direction comes from type.
	if entry.AmountMinor != 250_000 {
		t.Errorf("amountMinor = %d, want a positive 250000", entry.AmountMinor)
	}
	if entry.BalanceAfterMinor != 250_000 {
		t.Errorf("balanceAfterMinor = %d, want 250000", entry.BalanceAfterMinor)
	}

	balances := h.do(http.MethodGet, "/v1/wallet/balances", acct.Token, nil)
	var list []gen.WalletBalance
	balances.decode(t, &list)
	if len(list) != 1 || list[0].BalanceMinor != 250_000 ||
		list[0].Currency != gen.CurrencyCode("INR") {
		t.Fatalf("balances = %+v, want a single INR balance of 250000", list)
	}
}

func TestTopUpValidatesAmountAndPaymentMethod(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	card := addCard(t, h, acct.Token, "visa", "4242")

	cases := []struct {
		name   string
		amount int
		method string
	}{
		{"zero", 0, card.Id},
		{"negative", -500, card.Id},
		{"above the single top-up limit", 200_000_000, card.Id},
		{"unknown payment method", 1000, "00000000-0000-0000-0000-000000000000"},
		{"malformed payment method", 1000, "not-a-uuid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := h.do(http.MethodPost, "/v1/wallet/topup", acct.Token, map[string]any{
				"currency": "INR", "amountMinor": tc.amount, "paymentMethodId": tc.method,
			})
			if res.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body = %s", res.Code, res.Body)
			}
		})
	}

	// None of those must have moved money.
	balances := h.do(http.MethodGet, "/v1/wallet/balances", acct.Token, nil)
	var list []gen.WalletBalance
	balances.decode(t, &list)
	if len(list) != 0 {
		t.Fatalf("balances = %+v after only rejected top-ups, want none", list)
	}
}

// A tenant with cards but no default breaks auto-recharge in a way nobody
// notices until a wallet runs dry, so exactly one default must hold at all
// times.
func TestExactlyOneDefaultPaymentMethodIsMaintained(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	first := addCard(t, h, acct.Token, "visa", "4242")
	if !first.IsDefault {
		t.Fatal("the first card is not the default")
	}

	second := addCard(t, h, acct.Token, "mastercard", "5555")
	if second.IsDefault {
		t.Fatal("adding a second card stole the default")
	}

	promoted := h.do(http.MethodPost,
		"/v1/wallet/payment-methods/"+second.Id+"/default", acct.Token, nil)
	if promoted.Code != http.StatusOK {
		t.Fatalf("set default: status = %d; body = %s", promoted.Code, promoted.Body)
	}
	assertExactlyOneDefault(t, h, acct.Token, second.Id)

	// Removing the default must promote the survivor rather than leave none.
	if res := h.do(http.MethodDelete,
		"/v1/wallet/payment-methods/"+second.Id, acct.Token, nil); res.Code != http.StatusNoContent {
		t.Fatalf("remove: status = %d; body = %s", res.Code, res.Body)
	}
	assertExactlyOneDefault(t, h, acct.Token, first.Id)

	// Removing the last one leaves none, which is fine.
	if res := h.do(http.MethodDelete,
		"/v1/wallet/payment-methods/"+first.Id, acct.Token, nil); res.Code != http.StatusNoContent {
		t.Fatalf("remove last: status = %d", res.Code)
	}
	listed := h.do(http.MethodGet, "/v1/wallet/payment-methods", acct.Token, nil)
	var methods []gen.PaymentMethod
	listed.decode(t, &methods)
	if len(methods) != 0 {
		t.Fatalf("got %d methods after removing both, want 0", len(methods))
	}
}

func assertExactlyOneDefault(t *testing.T, h *harness, token, wantDefault string) {
	t.Helper()
	res := h.do(http.MethodGet, "/v1/wallet/payment-methods", token, nil)
	var methods []gen.PaymentMethod
	res.decode(t, &methods)

	defaults := 0
	for _, method := range methods {
		if method.IsDefault {
			defaults++
			if method.Id != wantDefault {
				t.Errorf("default is %s, want %s", method.Id, wantDefault)
			}
		}
	}
	if defaults != 1 {
		t.Fatalf("%d methods marked default, want exactly 1", defaults)
	}
}

func TestAddPaymentMethodValidatesLast4(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	for _, last4 := range []string{"", "123", "12345", "abcd"} {
		res := h.do(http.MethodPost, "/v1/wallet/payment-methods", acct.Token,
			map[string]string{"brand": "visa", "last4": last4})
		if res.Code != http.StatusUnprocessableEntity {
			t.Errorf("last4 %q: status = %d, want 422; body = %s", last4, res.Code, res.Body)
		}
	}
}

// Enabling auto-recharge with nothing to charge would fail at the worst
// possible moment — when the wallet has just run dry mid-campaign.
func TestAutoRechargeRequiresAPaymentMethodWhenEnabled(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPut, "/v1/wallet/auto-recharge", acct.Token, map[string]any{
		"currency": "INR", "enabled": true,
		"thresholdMinor": 10_000, "topUpMinor": 50_000, "paymentMethodId": nil,
	})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", res.Code, res.Body)
	}

	card := addCard(t, h, acct.Token, "visa", "4242")
	ok := h.do(http.MethodPut, "/v1/wallet/auto-recharge", acct.Token, map[string]any{
		"currency": "INR", "enabled": true,
		"thresholdMinor": 10_000, "topUpMinor": 50_000, "paymentMethodId": card.Id,
	})
	if ok.Code != http.StatusOK {
		t.Fatalf("with a card: status = %d, want 200; body = %s", ok.Code, ok.Body)
	}
	var config gen.AutoRechargeConfig
	ok.decode(t, &config)
	if !config.Enabled || config.TopUpMinor != 50_000 {
		t.Fatalf("config = %+v, want enabled with a 50000 top-up", config)
	}

	listed := h.do(http.MethodGet, "/v1/wallet/auto-recharge", acct.Token, nil)
	var configs []gen.AutoRechargeConfig
	listed.decode(t, &configs)
	if len(configs) != 1 || !configs[0].Enabled {
		t.Fatalf("configs = %+v, want one enabled config", configs)
	}
}

func TestAutoRechargeCanBeDisabledWithoutAPaymentMethod(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPut, "/v1/wallet/auto-recharge", acct.Token, map[string]any{
		"currency": "INR", "enabled": false,
		"thresholdMinor": 0, "topUpMinor": 0, "paymentMethodId": nil,
	})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}
}

func TestWalletMutationsAreForbiddenForMembers(t *testing.T) {
	h := newHarness(t)
	owner := h.newAccount("owner")
	member := h.newAccount("member")
	card := addCard(t, h, owner.Token, "visa", "4242")

	mutations := []struct {
		name, method, path string
		body               any
	}{
		{"top-up", http.MethodPost, "/v1/wallet/topup",
			map[string]any{"currency": "INR", "amountMinor": 1000, "paymentMethodId": card.Id}},
		{"add card", http.MethodPost, "/v1/wallet/payment-methods",
			map[string]string{"brand": "visa", "last4": "1111"}},
		{"auto-recharge", http.MethodPut, "/v1/wallet/auto-recharge",
			map[string]any{"currency": "INR", "enabled": false,
				"thresholdMinor": 0, "topUpMinor": 0, "paymentMethodId": nil}},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			res := h.do(tc.method, tc.path, member.Token, tc.body)
			if res.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body = %s", res.Code, res.Body)
			}
		})
	}
}

func TestWalletIsTenantScoped(t *testing.T) {
	h := newHarness(t)
	owner := h.newAccount("owner")
	other := h.newAccount("owner")
	card := addCard(t, h, owner.Token, "visa", "4242")
	topUp(t, h, owner.Token, "INR", 100_000, card.Id)

	balances := h.do(http.MethodGet, "/v1/wallet/balances", other.Token, nil)
	var list []gen.WalletBalance
	balances.decode(t, &list)
	if len(list) != 0 {
		t.Fatalf("another tenant sees %d balances, want 0", len(list))
	}

	ledger := h.do(http.MethodGet, "/v1/wallet/ledger", other.Token, nil)
	var page gen.LedgerPage
	ledger.decode(t, &page)
	if len(page.Entries) != 0 {
		t.Fatalf("another tenant sees %d ledger entries, want 0", len(page.Entries))
	}

	// And another tenant cannot delete our card.
	if res := h.do(http.MethodDelete,
		"/v1/wallet/payment-methods/"+card.Id, other.Token, nil); res.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant delete: status = %d, want 404", res.Code)
	}
}

// Spending must show up as a debit whose running balance is right, since the
// ledger is what a customer reconciles their statement against.
func TestLedgerShowsRunningBalanceAcrossEntries(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	card := addCard(t, h, acct.Token, "visa", "4242")

	topUp(t, h, acct.Token, "INR", 100_000, card.Id)
	topUp(t, h, acct.Token, "INR", 50_000, card.Id)

	res := h.do(http.MethodGet, "/v1/wallet/ledger?currency=INR", acct.Token, nil)
	var page gen.LedgerPage
	res.decode(t, &page)
	if len(page.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(page.Entries))
	}
	// Newest first.
	if page.Entries[0].BalanceAfterMinor != 150_000 {
		t.Errorf("latest balanceAfter = %d, want 150000", page.Entries[0].BalanceAfterMinor)
	}
	if page.Entries[1].BalanceAfterMinor != 100_000 {
		t.Errorf("earlier balanceAfter = %d, want 100000", page.Entries[1].BalanceAfterMinor)
	}
}
