package api

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

func TestAMonthsDeliveredUsageBecomesOneGSTInvoicePerCurrency(t *testing.T) {
	tenant := uuid.New()
	lines := invoiceLines([]store.BilledUsage{
		{TenantID: tenant, Channel: "SMS", Country: "IN", Currency: "INR", Quantity: 8000, UnitMinor: 12, AmountMinor: 96000},
		{TenantID: tenant, Channel: "RCS", Country: "IN", Currency: "INR", Quantity: 1000, UnitMinor: 21, AmountMinor: 21000},
		{TenantID: tenant, Channel: "SMS", Country: "US", Currency: "USD", Quantity: 7, UnitMinor: 1, AmountMinor: 7},
	})
	if len(lines) != 2 {
		t.Fatalf("%d invoices, want one per currency", len(lines))
	}
	inr := buildInvoice("INR", time.Time{}, time.Time{}, lines[invoiceKey{tenant, "INR"}])
	if inr.SubtotalMinor != 117000 || inr.TaxRatePercent != 18 || inr.TaxMinor != 21060 ||
		inr.TotalMinor != 138060 {
		t.Errorf("INR invoice = %+v, want 117000 + 18%% GST 21060 = 138060", inr)
	}
	usd := buildInvoice("USD", time.Time{}, time.Time{}, lines[invoiceKey{tenant, "USD"}])
	if usd.TaxMinor != 0 || usd.TotalMinor != 7 {
		t.Errorf("USD invoice = %+v, want untaxed", usd)
	}

	ist := time.FixedZone("IST", 5*3600+1800)
	// 1 Oct 02:00 IST is still 30 Sep in UTC; the invoice month is September either way.
	start, end := previousMonth(time.Date(2026, 10, 1, 2, 0, 0, 0, ist))
	if !start.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, ist)) || !end.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, ist)) {
		t.Errorf("period = %s – %s, want September IST", start, end)
	}
}
