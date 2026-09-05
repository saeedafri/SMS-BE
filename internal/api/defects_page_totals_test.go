package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/saeedafri/sms-be/internal/store"
)

// A halt on an id that does not exist must answer 404, and it must answer the
// SAME 404 whichever decision is being made.
//
// Approve did not. It ran a readiness check — verified DNS for an email sender,
// a verified caller ID for voice — before the call that knows how to report a
// missing row, and that check let a bare "no rows" escape to the error
// middleware as a 500. Reject skipped the check entirely and answered 404.
//
// A 500 is the one status a client may retry blindly, and an approval is not
// safe to retry blindly. It also makes a real outage look exactly like the
// ordinary case this arises from: another operator actioning the row between
// the queue rendering and the click.
//
// Asserted as a pair so the two cannot drift apart again.
func TestDecidingASenderThatDoesNotExistIs404WhicheverDecisionItIs(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	missing := "00000000-0000-0000-0000-000000000000"

	approve := h.do(http.MethodPost,
		fmt.Sprintf("/v1/operator/senders/%s/approve", missing), operator, nil)
	if approve.Code != http.StatusNotFound {
		t.Fatalf("approve on a missing sender = %d, want 404\n%s", approve.Code, approve.Body)
	}

	reject := h.do(http.MethodPost,
		fmt.Sprintf("/v1/operator/senders/%s/reject", missing), operator,
		map[string]any{"reason": "not a real sender"})
	if reject.Code != http.StatusNotFound {
		t.Fatalf("reject on a missing sender = %d, want 404\n%s", reject.Code, reject.Body)
	}

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	approve.decode(t, &body)
	if body.Error.Code != "not_found" {
		t.Fatalf("error code = %q, want not_found", body.Error.Code)
	}
	if body.Error.Message != "No such sender." {
		t.Fatalf("message = %q, want %q — it must match its reject sibling verbatim",
			body.Error.Message, "No such sender.")
	}
}

// Both of these page correctly and neither could say how many rows there are,
// so the footer of both screens could not render "showing 50 of N". For the
// ledger that is the number a customer actually wants.

func TestTheWalletLedgerReportsATotalBeyondThePage(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	const topups = 5
	for range topups {
		h.appendTopup(acct, "INR", 1000)
	}

	page := h.do(http.MethodGet, "/v1/wallet/ledger?limit=2", acct.Token, nil)
	if page.Code != http.StatusOK {
		t.Fatalf("ledger = %d\n%s", page.Code, page.Body)
	}
	var body struct {
		Entries []map[string]any `json:"entries"`
		Total   int              `json:"total"`
	}
	page.decode(t, &body)

	if len(body.Entries) != 2 {
		t.Fatalf("entries on the page = %d, want 2", len(body.Entries))
	}
	if body.Total != topups {
		t.Fatalf("total = %d, want %d — the total is the whole ledger, not the page",
			body.Total, topups)
	}
	if body.Total <= len(body.Entries) {
		t.Fatalf("total = %d with %d rows on the page — nothing tells the client "+
			"there is a second page", body.Total, len(body.Entries))
	}
}

// The total must describe the FILTER, not the tenant. A ledger filtered to one
// currency that reports the count of every currency is a footer that disagrees
// with the rows above it.
func TestTheLedgerTotalFollowsTheCurrencyFilter(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	for range 3 {
		h.appendTopup(acct, "INR", 1000)
	}
	for range 2 {
		h.appendTopup(acct, "USD", 1000)
	}

	for currency, want := range map[string]int{"INR": 3, "USD": 2} {
		page := h.do(http.MethodGet, "/v1/wallet/ledger?currency="+currency, acct.Token, nil)
		if page.Code != http.StatusOK {
			t.Fatalf("ledger %s = %d\n%s", currency, page.Code, page.Body)
		}
		var body struct {
			Entries []map[string]any `json:"entries"`
			Total   int              `json:"total"`
		}
		page.decode(t, &body)
		if body.Total != want {
			t.Fatalf("total for %s = %d, want %d", currency, body.Total, want)
		}
		if len(body.Entries) != want {
			t.Fatalf("entries for %s = %d, want %d", currency, len(body.Entries), want)
		}
	}
}

func TestTheInvoiceListReportsATotal(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	page := h.do(http.MethodGet, "/v1/billing/invoices?limit=2", acct.Token, nil)
	if page.Code != http.StatusOK {
		t.Fatalf("invoices = %d\n%s", page.Code, page.Body)
	}
	var body struct {
		Invoices []map[string]any `json:"invoices"`
		Total    int              `json:"total"`
	}
	page.decode(t, &body)

	// A new tenant has none, and the field must still be present and zero
	// rather than absent — the footer renders "0 of 0", it does not disappear.
	if body.Total != len(body.Invoices) {
		t.Fatalf("total = %d with %d invoices on the page", body.Total, len(body.Invoices))
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(page.Body), &raw); err != nil {
		t.Fatalf("invoice page is not an object: %v", err)
	}
	if _, present := raw["total"]; !present {
		t.Fatal("total is absent from the invoice page — it is a required contract key")
	}
}

// appendTopup puts one topup straight into the ledger, because
// POST /v1/wallet/topup needs a saved payment method and these tests are about
// the ledger's paging, not about how the money arrives.
func (h *harness) appendTopup(acct account, currency string, amountMinor int64) {
	h.t.Helper()
	if _, err := store.AppendLedgerEntry(context.Background(), h.pool,
		store.Identity{TenantID: acct.TenantID},
		store.LedgerEntry{Currency: currency, Type: "topup", AmountMinor: amountMinor,
			Description: "Test funding"}); err != nil {
		h.t.Fatalf("fund wallet: %v", err)
	}
}
