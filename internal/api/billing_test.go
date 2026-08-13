package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/store"
)

// seedInvoice writes an invoice directly. Invoice generation is a scheduled
// job that lands with the data plane; these tests are about the read path and
// the arithmetic, so the row is created here.
func seedInvoice(t *testing.T, h *harness, tenantID uuid.UUID, currency string, subtotal int64) uuid.UUID {
	t.Helper()

	rate := store.TaxRatePercentFor(currency)
	tax := subtotal * int64(rate) / 100
	var invoiceID uuid.UUID
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO invoices (tenant_id, currency, period_start, period_end, status,
		    subtotal_minor, tax_rate_percent, tax_minor, total_minor)
		VALUES ($1, $2, now() - interval '30 days', now(), 'issued', $3, $4, $5, $6)
		RETURNING id`,
		tenantID, currency, subtotal, rate, tax, subtotal+tax).Scan(&invoiceID); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	return invoiceID
}

func TestNewTenantHasNoInvoices(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodGet, "/v1/billing/invoices", acct.Token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}
	var page gen.InvoicePage
	res.decode(t, &page)
	if page.Invoices == nil {
		t.Error("invoices is null, want an empty array")
	}
	if len(page.Invoices) != 0 {
		t.Errorf("got %d invoices for a new tenant, want 0", len(page.Invoices))
	}
	if page.NextCursor != nil {
		t.Errorf("nextCursor = %v on an empty page, want null", *page.NextCursor)
	}
}

// India charges 18% GST on INR. The contract says plainly that no other
// country's tax rules are modelled, so everything else is zero rather than
// guessed at.
func TestInvoiceTaxIsGSTForINRAndZeroElsewhere(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	cases := []struct {
		currency string
		subtotal int64
		wantRate int
		wantTax  int64
	}{
		{"INR", 100_000, 18, 18_000},
		{"USD", 100_000, 0, 0},
		{"GBP", 100_000, 0, 0},
		{"AED", 100_000, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.currency, func(t *testing.T) {
			invoiceID := seedInvoice(t, h, acct.TenantID, tc.currency, tc.subtotal)

			res := h.do(http.MethodGet, "/v1/billing/invoices/"+invoiceID.String(), acct.Token, nil)
			if res.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
			}
			var invoice gen.Invoice
			res.decode(t, &invoice)

			if invoice.TaxRatePercent != tc.wantRate {
				t.Errorf("taxRatePercent = %d, want %d", invoice.TaxRatePercent, tc.wantRate)
			}
			if invoice.TaxMinor != int(tc.wantTax) {
				t.Errorf("taxMinor = %d, want %d", invoice.TaxMinor, tc.wantTax)
			}
			// The identity a customer checks first.
			if invoice.TotalMinor != invoice.SubtotalMinor+invoice.TaxMinor {
				t.Errorf("total %d != subtotal %d + tax %d",
					invoice.TotalMinor, invoice.SubtotalMinor, invoice.TaxMinor)
			}
			if invoice.LineItems == nil {
				t.Error("lineItems is null, want an array")
			}
		})
	}
}

func TestInvoicesAreTenantScoped(t *testing.T) {
	h := newHarness(t)
	owner := h.newAccount("owner")
	other := h.newAccount("owner")
	invoiceID := seedInvoice(t, h, owner.TenantID, "INR", 50_000)

	if res := h.do(http.MethodGet, "/v1/billing/invoices/"+invoiceID.String(),
		other.Token, nil); res.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant get: status = %d, want 404; body = %s", res.Code, res.Body)
	}

	res := h.do(http.MethodGet, "/v1/billing/invoices", other.Token, nil)
	var page gen.InvoicePage
	res.decode(t, &page)
	if len(page.Invoices) != 0 {
		t.Fatalf("another tenant sees %d invoices, want 0", len(page.Invoices))
	}
}

func TestBillingIsForbiddenForMembers(t *testing.T) {
	h := newHarness(t)
	member := h.newAccount("member")

	for _, path := range []string{"/v1/billing/invoices",
		"/v1/billing/invoices/" + uuid.New().String()} {
		res := h.do(http.MethodGet, path, member.Token, nil)
		if res.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403; body = %s", path, res.Code, res.Body)
		}
	}
}

func TestUsageReturnsAllThreeGroupings(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodGet, "/v1/billing/usage?range=30d", acct.Token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}
	var report gen.UsageReport
	res.decode(t, &report)

	// All three are required arrays; null would break the UI's mapping.
	if report.ByChannel == nil || report.ByCampaign == nil || report.ByJourney == nil {
		t.Fatalf("a grouping is null: %+v", report)
	}
	if len(report.ByChannel) != 0 {
		t.Errorf("a new tenant has %d channel usage rows, want 0", len(report.ByChannel))
	}
}

// Once a charge exists, usage reflects it — proving the report reads real
// ledger data rather than always returning empty.
func TestUsageReflectsActualCharges(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	card := addCard(t, h, acct.Token, "visa", "4242")
	topUp(t, h, acct.Token, "INR", 100_000, card.Id)

	if _, err := store.AppendLedgerEntry(context.Background(), h.pool,
		store.Identity{TenantID: acct.TenantID}, store.LedgerEntry{
			Currency: "INR", Type: "charge", AmountMinor: 12_000,
		}); err != nil {
		t.Fatalf("charge: %v", err)
	}

	res := h.do(http.MethodGet, "/v1/billing/usage?range=30d&currency=INR", acct.Token, nil)
	var report gen.UsageReport
	res.decode(t, &report)

	if len(report.ByChannel) != 1 {
		t.Fatalf("got %d channel rows, want 1: %+v", len(report.ByChannel), report.ByChannel)
	}
	if report.ByChannel[0].AmountMinor != 12_000 {
		t.Fatalf("amountMinor = %d, want 12000", report.ByChannel[0].AmountMinor)
	}
}
