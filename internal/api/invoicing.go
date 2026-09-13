package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// istZone is the zone invoice months are cut in: an Indian customer's
// September is September in IST, not in UTC.
var istZone = time.FixedZone("IST", 5*3600+1800)

// IssueMonthlyInvoices bills last month's delivered traffic for every tenant.
//
// Run on a timer rather than on the first of the month, because IssueInvoice
// skips a period already billed: the first tick of a new month issues them,
// and every later tick, restart or retry finds nothing left to do.
func (s *Server) IssueMonthlyInvoices(ctx context.Context) error {
	clickhouse, err := s.clickhouse(ctx)
	if err != nil {
		return nil
	}
	start, end := previousMonth(time.Now())
	if s.invoicedPeriod.Equal(start) {
		return nil // this month's run already completed; skip the warehouse scan
	}
	usage, err := store.BilledUsageBetween(ctx, clickhouse, start, end)
	if err != nil {
		return err
	}
	var failures []error
	issued := 0
	for key, lines := range invoiceLines(usage) {
		invoice := buildInvoice(key.currency, start, end, lines)
		ok, err := store.IssueInvoice(ctx, s.DB, store.Identity{TenantID: key.tenant},
			invoice, lines)
		if err != nil {
			failures = append(failures, fmt.Errorf("tenant %s: %w", key.tenant, err))
			continue
		}
		if ok {
			issued++
		}
	}
	if issued > 0 {
		s.Logger.Info("issued monthly invoices", "count", issued, "period", start.Format("2006-01"))
	}
	if len(failures) == 0 {
		s.invoicedPeriod = start
	}
	return errors.Join(failures...)
}

type invoiceKey struct {
	tenant   uuid.UUID
	currency string
}

func invoiceLines(usage []store.BilledUsage) map[invoiceKey][]store.InvoiceLine {
	out := map[invoiceKey][]store.InvoiceLine{}
	for _, row := range usage {
		key := invoiceKey{row.TenantID, row.Currency}
		unit := int64(0)
		if row.MessageCount > 0 {
			unit = row.AmountMinor / row.MessageCount
		}
		out[key] = append(out[key], store.InvoiceLine{
			Description: fmt.Sprintf("%s messages (%s)", row.Channel, row.Country),
			Quantity:    row.MessageCount, UnitMinor: unit, AmountMinor: row.AmountMinor,
		})
	}
	return out
}

// buildInvoice totals the lines and applies the currency's tax, rounding the
// tax half-up to the minor unit.
func buildInvoice(currency string, start, end time.Time, lines []store.InvoiceLine) store.Invoice {
	var subtotal int64
	for _, line := range lines {
		subtotal += line.AmountMinor
	}
	rate := store.TaxRatePercentFor(currency)
	tax := (subtotal*int64(rate) + 50) / 100
	return store.Invoice{Currency: currency, PeriodStart: start, PeriodEnd: end,
		SubtotalMinor: subtotal, TaxRatePercent: rate, TaxMinor: tax,
		TotalMinor: subtotal + tax}
}

// previousMonth is [first instant of last month, first instant of this month)
// in IST.
func previousMonth(now time.Time) (time.Time, time.Time) {
	local := now.In(istZone)
	end := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, istZone)
	return end.AddDate(0, -1, 0).UTC(), end.UTC()
}
