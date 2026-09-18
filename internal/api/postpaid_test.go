package api_test

import (
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Postpaid credit on account, over HTTP.
//
// The arithmetic itself is tested in internal/domain/billing, one test per rule.
// What these cover is the surface: which route serves what, which refusals come
// in which order, and the two places a rule can be right in the domain and
// still wrong on the wire — the customer's projection, and the export.

type accountInvoicePage struct {
	Entries []accountInvoiceBody `json:"entries"`
	Total   int                  `json:"total"`
	Totals  []struct {
		Currency         string `json:"currency"`
		InvoicedMinor    int64  `json:"invoicedMinor"`
		ReceivedMinor    int64  `json:"receivedMinor"`
		OutstandingMinor int64  `json:"outstandingMinor"`
		OverdueMinor     int64  `json:"overdueMinor"`
	} `json:"totals"`
}

type tenantPaymentBody struct {
	ID          string `json:"id"`
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amountMinor"`
	Reference   string `json:"reference"`
	Allocations []struct {
		InvoiceID     string `json:"invoiceId"`
		InvoiceNumber string `json:"invoiceNumber"`
		AmountMinor   int64  `json:"amountMinor"`
	} `json:"allocations"`
	UnappliedMinor int64   `json:"unappliedMinor"`
	VoidedAt       *string `json:"voidedAt"`
	VoidReason     *string `json:"voidReason"`
	VoidedBy       *string `json:"voidedBy"`
}

type tenantPaymentPage struct {
	Entries []tenantPaymentBody `json:"entries"`
	Total   int                 `json:"total"`
}

func utr() string { return fmt.Sprintf("UTR%d", rand.Int63()) }

// issueCredit loads a tenant's wallet on account and returns the invoice raised.
func issueCredit(t *testing.T, h *harness, operator, tenantID string,
	body map[string]any) accountInvoiceBody {
	t.Helper()
	res := h.do(http.MethodPost, creditPath(tenantID), operator, body)
	if res.Code != http.StatusCreated {
		t.Fatalf("credit = %d %s, want 201", res.Code, res.Body)
	}
	var result issueCreditResult
	res.decode(t, &result)
	return result.Invoice
}

func recordPayment(t *testing.T, h *harness, operator, tenantID string,
	body map[string]any) tenantPaymentBody {
	t.Helper()
	res := h.do(http.MethodPost, "/v1/operator/tenants/"+tenantID+"/payments", operator, body)
	if res.Code != http.StatusCreated {
		t.Fatalf("record payment = %d %s, want 201", res.Code, res.Body)
	}
	var payment tenantPaymentBody
	res.decode(t, &payment)
	return payment
}

// An invoice is raised for every credit, and it falls due at the end of the
// month it was issued in — in the tenant's own time, not UTC.
func TestIssuingCreditRaisesAnInvoiceDueAtTheEndOfTheMonth(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()

	invoice := issueCredit(t, h, operator, tenant.TenantID.String(), map[string]any{
		"currency": "INR", "amountMinor": 5000000, "reference": utr(),
	})

	if invoice.TotalMinor != 5900000 || invoice.TaxMinor != 900000 {
		t.Errorf("invoice = %+v, want 18%% on top of 5000000", invoice)
	}
	if !strings.HasPrefix(invoice.Number, "INV-") {
		t.Errorf("invoice number %q, want INV-YYYY-MM-NNN", invoice.Number)
	}
	due, err := time.Parse(time.RFC3339, invoice.DueAt)
	if err != nil {
		t.Fatalf("dueAt %q does not parse: %v", invoice.DueAt, err)
	}
	// The end of the month it was issued in, read in the tenant's own zone.
	// A UTC month end would put an invoice issued in the small hours of the 1st
	// into the previous month, due within hours.
	india, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatalf("no tzdata: %v", err)
	}
	local := due.In(india)
	if local.Day() != daysIn(local.Year(), local.Month()) || local.Hour() != 23 {
		t.Errorf("dueAt in IST = %s, want the last instant of the month", local)
	}
	if !due.After(time.Now()) {
		t.Errorf("a credit issued now is already due at %s", due)
	}

	// A caller may override it — an invoice issued on the 28th would otherwise
	// fall due in two days.
	chosen := time.Now().AddDate(0, 3, 0).UTC().Truncate(time.Second)
	override := issueCredit(t, h, operator, tenant.TenantID.String(), map[string]any{
		"currency": "INR", "amountMinor": 1000, "reference": utr(),
		"dueAt": chosen.Format(time.RFC3339),
	})
	if got, _ := time.Parse(time.RFC3339, override.DueAt); !got.Equal(chosen) {
		t.Errorf("dueAt = %s, want the %s the caller asked for", got, chosen)
	}
}

func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// An absent rate means "the tenant country's own"; a supplied 0 means "no tax
// on this credit". They are different answers and must not be conflated.
func TestAnAbsentTaxRateIsNotTheSameAsAZeroOne(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()

	absent := issueCredit(t, h, operator, tenant.TenantID.String(), map[string]any{
		"currency": "INR", "amountMinor": 100000, "reference": utr(),
	})
	if absent.TaxRatePercent != 18 || absent.TaxMinor != 18000 {
		t.Errorf("an absent rate gave %+v, want India's own 18%%", absent)
	}

	zero := issueCredit(t, h, operator, tenant.TenantID.String(), map[string]any{
		"currency": "INR", "amountMinor": 100000, "reference": utr(),
		"taxRatePercent": 0,
	})
	if zero.TaxRatePercent != 0 || zero.TaxMinor != 0 || zero.TotalMinor != 100000 {
		t.Errorf("an explicit 0 gave %+v, want no tax at all", zero)
	}
}

// One transfer, recorded once, settling several invoices oldest-first.
func TestOnePaymentSettlesSeveralInvoicesOldestFirst(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()
	id := tenant.TenantID.String()

	// Two invoices, the first due sooner than the second.
	soon := time.Now().AddDate(0, 1, 0).UTC().Format(time.RFC3339)
	later := time.Now().AddDate(0, 2, 0).UTC().Format(time.RFC3339)
	first := issueCredit(t, h, operator, id, map[string]any{
		"currency": "INR", "amountMinor": 100000, "reference": utr(),
		"taxRatePercent": 0, "dueAt": soon,
	})
	second := issueCredit(t, h, operator, id, map[string]any{
		"currency": "INR", "amountMinor": 100000, "reference": utr(),
		"taxRatePercent": 0, "dueAt": later,
	})

	// 150,000 settles the first whole and half the second.
	payment := recordPayment(t, h, operator, id, map[string]any{
		"currency": "INR", "amountMinor": 150000, "reference": utr(),
		"receivedAt": time.Now().UTC().Format(time.RFC3339),
	})
	if len(payment.Allocations) != 2 {
		t.Fatalf("allocations = %+v, want two", payment.Allocations)
	}
	if payment.Allocations[0].InvoiceID != first.ID || payment.Allocations[0].AmountMinor != 100000 {
		t.Errorf("first allocation = %+v, want the soonest-due invoice settled whole",
			payment.Allocations[0])
	}
	if payment.Allocations[1].InvoiceID != second.ID || payment.Allocations[1].AmountMinor != 50000 {
		t.Errorf("second allocation = %+v, want 50000 against the later invoice",
			payment.Allocations[1])
	}
	if payment.UnappliedMinor != 0 {
		t.Errorf("unapplied = %d, want 0", payment.UnappliedMinor)
	}

	// The invoices report it, derived rather than stored.
	page := operatorInvoices(t, h, operator, id, "")
	for _, invoice := range page.Entries {
		switch invoice.ID {
		case first.ID:
			if invoice.Status != "paid" || invoice.OutstandingMinor != 0 {
				t.Errorf("first invoice = %+v, want paid and owing nothing", invoice)
			}
		case second.ID:
			if invoice.Status != "partly_paid" || invoice.OutstandingMinor != 50000 {
				t.Errorf("second invoice = %+v, want partly paid owing 50000", invoice)
			}
		}
	}

	// The same UTR again is refused, and moves nothing: the duplicate guard is
	// the reason a transfer is recorded once rather than once per invoice.
	again := h.do(http.MethodPost, "/v1/operator/tenants/"+id+"/payments", operator,
		map[string]any{"currency": "INR", "amountMinor": 150000,
			"reference": payment.Reference, "receivedAt": time.Now().UTC().Format(time.RFC3339)})
	if again.Code != http.StatusConflict {
		t.Errorf("the same UTR again = %d %s, want 409", again.Code, again.Body)
	}
	if after := operatorInvoices(t, h, operator, id, ""); after.Totals[0].ReceivedMinor != 150000 {
		t.Errorf("a refused duplicate banked more money: received %d",
			after.Totals[0].ReceivedMinor)
	}
}

// Money left over is held against the account, and the next invoice absorbs it.
func TestOverpaidMoneyIsHeldAndTheNextInvoiceAbsorbsIt(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()
	id := tenant.TenantID.String()

	issueCredit(t, h, operator, id, map[string]any{
		"currency": "INR", "amountMinor": 100000, "reference": utr(), "taxRatePercent": 0,
	})
	payment := recordPayment(t, h, operator, id, map[string]any{
		"currency": "INR", "amountMinor": 250000, "reference": utr(),
		"receivedAt": time.Now().UTC().Format(time.RFC3339),
	})
	if payment.UnappliedMinor != 150000 {
		t.Fatalf("unapplied = %d, want 150000 held rather than refused", payment.UnappliedMinor)
	}

	// Issue the next invoice: the held money claims it with no second act.
	next := issueCredit(t, h, operator, id, map[string]any{
		"currency": "INR", "amountMinor": 120000, "reference": utr(), "taxRatePercent": 0,
	})
	if next.Status != "paid" || next.ReceivedMinor != 120000 {
		t.Errorf("the next invoice = %+v, want it already settled from the overpayment", next)
	}
	var page tenantPaymentPage
	h.do(http.MethodGet, "/v1/operator/tenants/"+id+"/payments", operator, nil).decode(t, &page)
	if len(page.Entries) != 1 || page.Entries[0].UnappliedMinor != 30000 {
		t.Errorf("payments = %+v, want 30000 still held", page.Entries)
	}
}

// Voiding reverses without deleting.
func TestVoidingAPaymentGivesTheInvoiceItsDebtBackAndStaysOnTheRecord(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()
	id := tenant.TenantID.String()

	invoice := issueCredit(t, h, operator, id, map[string]any{
		"currency": "INR", "amountMinor": 100000, "reference": utr(), "taxRatePercent": 0,
	})
	payment := recordPayment(t, h, operator, id, map[string]any{
		"currency": "INR", "amountMinor": 100000, "reference": utr(),
		"receivedAt": time.Now().UTC().Format(time.RFC3339),
	})
	voidPath := "/v1/operator/tenants/" + id + "/payments/" + payment.ID + "/void"

	// A reason is required: a payment that reversed itself with no stated
	// reason is unauditable, and this is money.
	for name, body := range map[string]any{
		"no reason":   map[string]any{},
		"too short":   map[string]any{"reason": "no"},
		"too long":    map[string]any{"reason": strings.Repeat("x", 201)},
		"another key": map[string]any{"reason": "Wrong UTR", "voidedAt": "2026-01-01T00:00:00Z"},
	} {
		if res := h.do(http.MethodPost, voidPath, operator, body); res.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d %s, want 422", name, res.Code, res.Body)
		}
	}
	// Nothing moved while those were refused.
	if page := operatorInvoices(t, h, operator, id, ""); page.Entries[0].Status != "paid" {
		t.Fatalf("a refused void changed the invoice to %q", page.Entries[0].Status)
	}

	res := h.do(http.MethodPost, voidPath, operator, map[string]any{"reason": "Wrong UTR entered"})
	if res.Code != http.StatusOK {
		t.Fatalf("void = %d %s, want 200", res.Code, res.Body)
	}
	var voided tenantPaymentBody
	res.decode(t, &voided)
	if voided.VoidedAt == nil || voided.VoidReason == nil || *voided.VoidReason != "Wrong UTR entered" {
		t.Errorf("voided payment = %+v, want it to carry why and when", voided)
	}
	if voided.VoidedBy == nil {
		t.Errorf("voided payment does not say who: %+v", voided)
	}
	// It settled nothing, so it holds nothing back either.
	if len(voided.Allocations) != 0 || voided.UnappliedMinor != 0 {
		t.Errorf("a voided payment still allocates %+v / holds %d",
			voided.Allocations, voided.UnappliedMinor)
	}

	// The invoice owes its money again — derived, with no unwinding step.
	page := operatorInvoices(t, h, operator, id, "")
	if page.Entries[0].ID != invoice.ID || page.Entries[0].OutstandingMinor != 100000 {
		t.Errorf("after voiding, the invoice = %+v, want it owing 100000 again", page.Entries[0])
	}
	// It stays on the record rather than vanishing.
	var payments tenantPaymentPage
	h.do(http.MethodGet, "/v1/operator/tenants/"+id+"/payments", operator, nil).decode(t, &payments)
	if payments.Total != 1 {
		t.Errorf("payments after voiding = %d, want the row kept", payments.Total)
	}

	if second := h.do(http.MethodPost, voidPath, operator,
		map[string]any{"reason": "Voiding it twice"}); second.Code != http.StatusConflict {
		t.Errorf("voiding twice = %d %s, want 409", second.Code, second.Body)
	}
}

// The credit limit caps what a tenant may owe, and the refusal names the
// shortfall rather than leaving an operator to guess.
func TestACreditPastTheLimitIsRefusedAndTheRefusalNamesTheShortfall(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()
	id := tenant.TenantID.String()
	limitPath := "/v1/operator/tenants/" + id + "/credit-limit"

	set := h.do(http.MethodPut, limitPath, operator, map[string]any{"creditLimitMinor": 10000000})
	if set.Code != http.StatusOK {
		t.Fatalf("set limit = %d %s, want 200", set.Code, set.Body)
	}
	var detail struct {
		CreditLimitMinor    *int64  `json:"creditLimitMinor"`
		CreditLimitCurrency *string `json:"creditLimitCurrency"`
	}
	set.decode(t, &detail)
	if detail.CreditLimitMinor == nil || *detail.CreditLimitMinor != 10000000 ||
		detail.CreditLimitCurrency == nil || *detail.CreditLimitCurrency != "INR" {
		t.Errorf("tenant detail = %+v, want a 10000000 INR limit", detail)
	}

	// 90,000 at 18% is 1,06,200 — 6,200 past a 1,00,000 limit. Tax-inclusive,
	// because the limit caps what they owe and what they owe includes the tax.
	over := h.do(http.MethodPost, creditPath(id), operator, map[string]any{
		"currency": "INR", "amountMinor": 9000000, "reference": utr(),
	})
	if over.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a credit past the limit = %d %s, want 422", over.Code, over.Body)
	}
	if !strings.Contains(string(over.Body), "INR 6200.00") {
		t.Errorf("the refusal does not name the shortfall: %s", over.Body)
	}
	// Refused before any write: neither the wallet nor the invoice list moved.
	if page := operatorInvoices(t, h, operator, id, ""); page.Total != 0 {
		t.Errorf("a refused credit left %d invoices behind", page.Total)
	}

	// Under the limit is fine.
	issueCredit(t, h, operator, id, map[string]any{
		"currency": "INR", "amountMinor": 5000000, "reference": utr(), "taxRatePercent": 0,
	})
	// A different currency is not capped by a rupee limit.
	issueCredit(t, h, operator, id, map[string]any{
		"currency": "USD", "amountMinor": 9000000, "reference": utr(),
	})

	// Null removes the limit, which is a deliberate act.
	cleared := h.do(http.MethodPut, limitPath, operator, map[string]any{"creditLimitMinor": nil})
	if cleared.Code != http.StatusOK {
		t.Fatalf("clearing the limit = %d %s", cleared.Code, cleared.Body)
	}
	cleared.decode(t, &detail)
	if detail.CreditLimitMinor != nil || detail.CreditLimitCurrency != nil {
		t.Errorf("after clearing = %+v, want both null", detail)
	}
	if res := h.do(http.MethodPost, creditPath(id), operator, map[string]any{
		"currency": "INR", "amountMinor": 90000000, "reference": utr(),
	}); res.Code != http.StatusCreated {
		t.Errorf("with no limit a large credit = %d %s, want 201", res.Code, res.Body)
	}
}

// A tenant who has overpaid has already handed over money no invoice has
// claimed. Measuring a proposed credit against outstanding alone refuses credit
// they have already paid for.
func TestAnOverpayingTenantIsNotBlockedByTheirOwnCreditLimit(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()
	id := tenant.TenantID.String()

	if res := h.do(http.MethodPut, "/v1/operator/tenants/"+id+"/credit-limit", operator,
		map[string]any{"creditLimitMinor": 10000000}); res.Code != http.StatusOK {
		t.Fatalf("set limit = %d %s", res.Code, res.Body)
	}
	// Paid up front: 50,000 in, no invoice to claim it yet.
	payment := recordPayment(t, h, operator, id, map[string]any{
		"currency": "INR", "amountMinor": 5000000, "reference": utr(),
		"receivedAt": time.Now().UTC().Format(time.RFC3339),
	})
	if payment.UnappliedMinor != 5000000 {
		t.Fatalf("unapplied = %d, want the whole 5000000 held", payment.UnappliedMinor)
	}

	// 1,00,000 at 18% is 1,18,000 — past a 1,00,000 limit on its face, but
	// 68,000 once the money already paid is counted.
	invoice := issueCredit(t, h, operator, id, map[string]any{
		"currency": "INR", "amountMinor": 10000000, "reference": utr(),
	})
	if invoice.ReceivedMinor != 5000000 || invoice.OutstandingMinor != 6800000 {
		t.Errorf("invoice = %+v, want 5000000 already received and 6800000 owing", invoice)
	}
}

// The customer reads their own invoices and never the operator's notes.
func TestTheCustomerSeesTheirInvoicesButNeverTheOperatorsNote(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()
	id := tenant.TenantID.String()

	issueCredit(t, h, operator, id, map[string]any{
		"currency": "INR", "amountMinor": 100000, "reference": utr(),
		"note": "Agreed over the phone; chase before the next top-up.",
	})

	var mine accountInvoicePage
	res := h.do(http.MethodGet, "/v1/billing/account-invoices", tenant.Token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("customer invoices = %d %s, want 200", res.Code, res.Body)
	}
	res.decode(t, &mine)
	if len(mine.Entries) != 1 {
		t.Fatalf("customer sees %d invoices, want 1", len(mine.Entries))
	}
	if mine.Entries[0].Note != nil || mine.Entries[0].IssuedBy != nil {
		t.Errorf("the customer can read note=%v issuedBy=%v — both must be null",
			mine.Entries[0].Note, mine.Entries[0].IssuedBy)
	}
	// The raw body, not just the decoded struct: a note leaking under any key
	// is still a leak.
	if strings.Contains(string(res.Body), "Agreed over the phone") {
		t.Errorf("the operator's note reached the customer's route: %s", res.Body)
	}

	// The operator's own view keeps them.
	page := operatorInvoices(t, h, operator, id, "")
	if page.Entries[0].Note == nil || page.Entries[0].IssuedBy == nil {
		t.Errorf("the operator lost the note: %+v", page.Entries[0])
	}

	// One tenant never reads another's.
	other := h.newAccount("owner")
	var theirs accountInvoicePage
	h.do(http.MethodGet, "/v1/billing/account-invoices", other.Token, nil).decode(t, &theirs)
	if theirs.Total != 0 {
		t.Errorf("a different tenant sees %d invoices, want 0", theirs.Total)
	}
}

// Totals are computed over the whole filtered set, never over the page.
func TestTotalsCoverEveryMatchingInvoiceNotJustThePage(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()
	id := tenant.TenantID.String()

	for i := 0; i < 5; i++ {
		issueCredit(t, h, operator, id, map[string]any{
			"currency": "INR", "amountMinor": 100000, "reference": utr(), "taxRatePercent": 0,
		})
	}

	page := operatorInvoices(t, h, operator, id, "")
	if page.Total != 5 || len(page.Totals) != 1 || page.Totals[0].InvoicedMinor != 500000 {
		t.Fatalf("unpaged = %+v, want 5 invoices totalling 500000", page)
	}

	// One row at a time: the total must not follow the page.
	var first accountInvoicePage
	h.do(http.MethodGet, "/v1/operator/tenants/"+id+"/invoices?page=1&limit=1",
		operator, nil).decode(t, &first)
	if len(first.Entries) != 1 {
		t.Fatalf("a one-row page returned %d rows", len(first.Entries))
	}
	if first.Total != 5 {
		t.Errorf("total = %d, want 5 across every page", first.Total)
	}
	if first.Totals[0].InvoicedMinor != 500000 {
		t.Errorf("totals summed the page, not the set: %+v", first.Totals[0])
	}
	// A page past the end is empty, not an error, and still totals the set.
	var beyond accountInvoicePage
	h.do(http.MethodGet, "/v1/operator/tenants/"+id+"/invoices?page=99&limit=20",
		operator, nil).decode(t, &beyond)
	if len(beyond.Entries) != 0 || beyond.Total != 5 || beyond.Totals[0].InvoicedMinor != 500000 {
		t.Errorf("page 99 = %+v, want no rows but the same totals", beyond)
	}

	// `due` means not fully settled; `paid` means paid.
	recordPayment(t, h, operator, id, map[string]any{
		"currency": "INR", "amountMinor": 200000, "reference": utr(),
		"receivedAt": time.Now().UTC().Format(time.RFC3339),
	})
	if due := operatorInvoices(t, h, operator, id, "due"); due.Total != 3 {
		t.Errorf("due = %d invoices, want the 3 not fully settled", due.Total)
	}
	if paid := operatorInvoices(t, h, operator, id, "paid"); paid.Total != 2 {
		t.Errorf("paid = %d invoices, want 2", paid.Total)
	}
}

// The export is built from the same filter the paged route uses, and the
// customer's file is a strict prefix of the operator's.
func TestTheExportMatchesTheScreenAndWithholdsTheSameColumns(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()
	id := tenant.TenantID.String()

	for i := 0; i < 3; i++ {
		issueCredit(t, h, operator, id, map[string]any{
			"currency": "INR", "amountMinor": 100000, "reference": utr(),
			"taxRatePercent": 0, "note": "internal only",
		})
	}
	recordPayment(t, h, operator, id, map[string]any{
		"currency": "INR", "amountMinor": 100000, "reference": utr(),
		"receivedAt": time.Now().UTC().Format(time.RFC3339),
	})

	operatorCSV := h.do(http.MethodGet,
		"/v1/operator/tenants/"+id+"/invoices/export", operator, nil)
	if operatorCSV.Code != http.StatusOK {
		t.Fatalf("operator export = %d %s", operatorCSV.Code, operatorCSV.Body)
	}
	operatorRows := strings.Split(strings.TrimSpace(string(operatorCSV.Body)), "\n")
	// Header plus one row per invoice matching the filter — the same count the
	// paged route reports as its total.
	if got, want := len(operatorRows)-1, operatorInvoices(t, h, operator, id, "").Total; got != want {
		t.Errorf("export has %d rows, the screen says %d", got, want)
	}

	customerCSV := h.do(http.MethodGet, "/v1/billing/account-invoices/export", tenant.Token, nil)
	if customerCSV.Code != http.StatusOK {
		t.Fatalf("customer export = %d %s", customerCSV.Code, customerCSV.Body)
	}
	customerRows := strings.Split(strings.TrimSpace(string(customerCSV.Body)), "\n")

	// A strict prefix, column for column, so the two files cannot drift apart.
	if !strings.HasPrefix(operatorRows[0], customerRows[0]+",") {
		t.Errorf("the customer header %q is not a prefix of the operator's %q",
			customerRows[0], operatorRows[0])
	}
	if !strings.HasSuffix(operatorRows[0], ",issuedBy,note") {
		t.Errorf("operator header = %q, want issuedBy and note appended", operatorRows[0])
	}
	if strings.Contains(string(customerCSV.Body), "internal only") {
		t.Errorf("the operator's note reached the customer's file")
	}
	// Money as minor units, so a spreadsheet can sum the column.
	if !strings.Contains(operatorRows[1], ",100000,") {
		t.Errorf("row %q does not carry minor units", operatorRows[1])
	}

	// The filter reaches the file: fewer rows under `paid`.
	paidCSV := h.do(http.MethodGet,
		"/v1/operator/tenants/"+id+"/invoices/export?status=paid", operator, nil)
	paidRows := strings.Split(strings.TrimSpace(string(paidCSV.Body)), "\n")
	if len(paidRows)-1 != operatorInvoices(t, h, operator, id, "paid").Total {
		t.Errorf("the filtered export has %d rows, the filtered screen says %d",
			len(paidRows)-1, operatorInvoices(t, h, operator, id, "paid").Total)
	}
}

// Every one of these routes needs an operator, a tenant that exists and a page
// inside the bounds.
func TestThePostpaidRoutesRefuseTheUsualThreeWays(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()
	id := tenant.TenantID.String()
	const ghost = "6f1d1f6a-0000-4000-8000-000000000000"

	for _, path := range []string{
		"/v1/operator/tenants/" + id + "/invoices",
		"/v1/operator/tenants/" + id + "/invoices/export",
		"/v1/operator/tenants/" + id + "/payments",
	} {
		if res := h.do(http.MethodGet, path, tenant.Token, nil); res.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with a tenant token = %d, want 401", path, res.Code)
		}
		if res := h.do(http.MethodGet, path, "", nil); res.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with no token = %d, want 401", path, res.Code)
		}
	}
	for _, path := range []string{
		"/v1/operator/tenants/" + ghost + "/invoices",
		"/v1/operator/tenants/" + ghost + "/invoices/export",
		"/v1/operator/tenants/" + ghost + "/payments",
	} {
		if res := h.do(http.MethodGet, path, operator, nil); res.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, res.Code)
		}
	}
	if res := h.do(http.MethodPut, "/v1/operator/tenants/"+ghost+"/credit-limit", operator,
		map[string]any{"creditLimitMinor": 1000}); res.Code != http.StatusNotFound {
		t.Errorf("credit limit on an unknown tenant = %d, want 404", res.Code)
	}
	if res := h.do(http.MethodPost, "/v1/operator/tenants/"+ghost+"/payments", operator,
		map[string]any{"currency": "INR", "amountMinor": 1000, "reference": utr(),
			"receivedAt": time.Now().UTC().Format(time.RFC3339)}); res.Code != http.StatusNotFound {
		t.Errorf("payment against an unknown tenant = %d, want 404", res.Code)
	}

	for name, query := range map[string]string{
		"page zero":      "?page=0",
		"limit zero":     "?limit=0",
		"limit past cap": "?limit=201",
	} {
		res := h.do(http.MethodGet, "/v1/operator/tenants/"+id+"/invoices"+query, operator, nil)
		if res.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d %s, want 422", name, res.Code, res.Body)
		}
	}

	// A payment outside the rules is refused and banks nothing.
	for name, body := range map[string]map[string]any{
		"zero":             {"currency": "INR", "amountMinor": 0, "reference": utr(), "receivedAt": time.Now().UTC().Format(time.RFC3339)},
		"over one crore":   {"currency": "INR", "amountMinor": 1000000001, "reference": utr(), "receivedAt": time.Now().UTC().Format(time.RFC3339)},
		"unknown currency": {"currency": "EUR", "amountMinor": 1000, "reference": utr(), "receivedAt": time.Now().UTC().Format(time.RFC3339)},
		"short reference":  {"currency": "INR", "amountMinor": 1000, "reference": "UTR1", "receivedAt": time.Now().UTC().Format(time.RFC3339)},
		"an unknown field": {"currency": "INR", "amountMinor": 1000, "reference": utr(), "receivedAt": time.Now().UTC().Format(time.RFC3339), "allocations": []any{}},
	} {
		if res := h.do(http.MethodPost, "/v1/operator/tenants/"+id+"/payments", operator,
			body); res.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d %s, want 422", name, res.Code, res.Body)
		}
	}
	var payments tenantPaymentPage
	h.do(http.MethodGet, "/v1/operator/tenants/"+id+"/payments", operator, nil).decode(t, &payments)
	if payments.Total != 0 {
		t.Errorf("refused payments banked %d rows", payments.Total)
	}
}

func operatorInvoices(t *testing.T, h *harness, operator, tenantID, filter string) accountInvoicePage {
	t.Helper()
	path := "/v1/operator/tenants/" + tenantID + "/invoices?limit=200"
	if filter != "" {
		path += "&status=" + filter
	}
	var page accountInvoicePage
	res := h.do(http.MethodGet, path, operator, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("operator invoices = %d %s, want 200", res.Code, res.Body)
	}
	res.decode(t, &page)
	return page
}
