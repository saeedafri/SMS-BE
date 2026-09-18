package billing

import (
	"sort"
	"time"
)

// Postpaid credit on account: the money rules behind an invoice a tenant owes
// for credit issued to them, and the payments that settle it.
//
// Nothing here is stored. An Invoice holds what was issued and a Payment holds
// what arrived; which invoice a payment settled, how much an invoice has
// received, whether it is overdue and what a tenant owes are all computed here
// on every read. That is what makes an invoice turn overdue the instant its due
// date passes, with no scheduled job to fail — and why voiding a payment needs
// no unwinding logic: drop it from the pass and every dependent figure corrects
// itself.
//
// Every amount is minor units in the invoice's own currency. There is no
// float anywhere in this file.

// Invoice is one credit issued to a tenant on account.
type Invoice struct {
	ID             string
	Number         string
	Currency       string
	TaxableMinor   int64
	TaxRatePercent int
	TaxMinor       int64
	TotalMinor     int64
	IssuedAt       time.Time
	DueAt          time.Time
	Reference      string
	Note           string
	IssuedBy       string
}

// Payment is one bank transfer received from a tenant.
type Payment struct {
	ID          string
	Currency    string
	AmountMinor int64
	Reference   string
	ReceivedAt  time.Time
	RecordedAt  time.Time
	RecordedBy  string
	VoidedAt    *time.Time
	VoidReason  string
	VoidedBy    string
}

// Allocation is how much of one payment settled one invoice.
type Allocation struct {
	PaymentID     string
	InvoiceID     string
	InvoiceNumber string
	AmountMinor   int64
}

// Settlement is the whole derived picture for one tenant.
type Settlement struct {
	Allocations []Allocation
	// Left over after every open invoice was settled, per payment. A tenant who
	// overpays has it held against the account rather than refused, and the next
	// invoice issued absorbs it on the next run of this same pass.
	UnappliedByPayment map[string]int64
	ReceivedByInvoice  map[string]int64
}

// TaxRateFor is the rate a country's regulator charges on messaging, used when
// a credit does not name one, and whether we have one at all.
//
// Per country rather than a constant: hardcoding 18 would make India's GST the
// whole world's. A country not listed has no confirmed rate and gets zero,
// which is honest — it never invents a charge nobody asked for.
func TaxRateFor(country string) (int, bool) {
	rate, known := map[string]int{"IN": 18, "US": 0, "GB": 20, "AE": 5}[country]
	return rate, known
}

// BillingLocationFor is the timezone a tenant's month ends in.
//
// "The end of the month" is not a UTC fact. A credit issued at 02:00 IST on 1
// October is 20:30 UTC on 30 September, so a month end computed in UTC would
// fall due at 05:29 that same morning — an invoice raised on the 1st, due
// before breakfast. The contract already defines Invoice.periodStart and
// periodEnd in IST; these two must agree about when a month ends.
func BillingLocationFor(country string) *time.Location {
	names := map[string]string{
		"IN": "Asia/Kolkata",
		"AE": "Asia/Dubai",
		"GB": "Europe/London",
		"US": "America/New_York",
	}
	if name, known := names[country]; known {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	// A country we have no zone for falls back to UTC, which is wrong by at
	// most a day and never silently wrong about which country it is.
	return time.UTC
}

// TaxOn is the tax charged ON TOP of a taxable amount, never carved out of it.
//
// Loading 50,000 at 18% means 50,000 of sending power reaches the wallet and the
// tenant owes 59,000. Carving the tax out would put 42,372.88 in the wallet —
// less than the operator typed, and nobody notices until a customer says their
// balance is short.
func TaxOn(taxableMinor int64, ratePercent int) int64 {
	if taxableMinor <= 0 || ratePercent <= 0 {
		return 0
	}
	// Integer throughout: the product is exact and the only division is the
	// final one, rounded half away from zero on a positive value.
	product := taxableMinor * int64(ratePercent)
	return (product + 50) / 100
}

// MonthEnd is the last instant of the month a moment falls in, IN THE GIVEN
// LOCATION.
//
// The location is the point. An invoice issued at 02:00 IST on 1 October is
// 20:30 UTC on 30 September, so a month end computed in UTC would fall due at
// 05:29 that same morning — an invoice raised on the 1st, due before breakfast.
// The contract already defines Invoice.periodStart and periodEnd in the
// tenant's own time, and these two must agree about when a month ends.
func MonthEnd(at time.Time, loc *time.Location) time.Time {
	local := at.In(loc)
	firstOfNext := time.Date(local.Year(), local.Month()+1, 1, 0, 0, 0, 0, loc)
	return firstOfNext.Add(-time.Nanosecond)
}

// Settle applies a tenant's payments to their invoices, oldest due first.
//
// Runs per currency: rupees never settle a dollar invoice, and the two never
// meet in a total. Voided payments are excluded rather than reversed, which is
// the whole reason voiding is a one-column write.
func Settle(invoices []Invoice, payments []Payment) Settlement {
	settlement := Settlement{
		UnappliedByPayment: map[string]int64{},
		ReceivedByInvoice:  map[string]int64{},
	}

	byCurrency := map[string][]Invoice{}
	for _, invoice := range invoices {
		byCurrency[invoice.Currency] = append(byCurrency[invoice.Currency], invoice)
	}
	inflow := map[string][]Payment{}
	for _, payment := range payments {
		if payment.VoidedAt != nil {
			continue
		}
		inflow[payment.Currency] = append(inflow[payment.Currency], payment)
	}

	// Every currency present on either side, so an overpayment in a currency
	// with no invoices is still reported as unapplied rather than dropped.
	currencies := map[string]bool{}
	for currency := range byCurrency {
		currencies[currency] = true
	}
	for currency := range inflow {
		currencies[currency] = true
	}
	ordered := make([]string, 0, len(currencies))
	for currency := range currencies {
		ordered = append(ordered, currency)
	}
	sort.Strings(ordered)

	for _, currency := range ordered {
		open := append([]Invoice(nil), byCurrency[currency]...)
		sort.SliceStable(open, func(i, j int) bool {
			return bySettlementOrder(open[i], open[j])
		})
		money := append([]Payment(nil), inflow[currency]...)
		sort.SliceStable(money, func(i, j int) bool {
			return byReceiptOrder(money[i], money[j])
		})

		owed := make(map[string]int64, len(open))
		for _, invoice := range open {
			owed[invoice.ID] = invoice.TotalMinor
		}

		for _, payment := range money {
			left := payment.AmountMinor
			for _, invoice := range open {
				if left == 0 {
					break
				}
				remaining := owed[invoice.ID]
				if remaining == 0 {
					continue
				}
				applied := remaining
				if left < applied {
					applied = left
				}
				owed[invoice.ID] = remaining - applied
				left -= applied
				settlement.Allocations = append(settlement.Allocations, Allocation{
					PaymentID: payment.ID, InvoiceID: invoice.ID,
					InvoiceNumber: invoice.Number, AmountMinor: applied,
				})
				settlement.ReceivedByInvoice[invoice.ID] += applied
			}
			settlement.UnappliedByPayment[payment.ID] = left
		}
	}
	return settlement
}

// bySettlementOrder: soonest due first, then the one issued first, then the
// number. Deterministic all the way down, so the pass cannot depend on the
// order rows came back from the database.
func bySettlementOrder(a, b Invoice) bool {
	if !a.DueAt.Equal(b.DueAt) {
		return a.DueAt.Before(b.DueAt)
	}
	if !a.IssuedAt.Equal(b.IssuedAt) {
		return a.IssuedAt.Before(b.IssuedAt)
	}
	return a.Number < b.Number
}

// byReceiptOrder: payments apply in the order the money LANDED, not the order
// it was typed in. An operator reconciling a week of transfers enters them in
// whatever order they read the statement.
func byReceiptOrder(a, b Payment) bool {
	if !a.ReceivedAt.Equal(b.ReceivedAt) {
		return a.ReceivedAt.Before(b.ReceivedAt)
	}
	if !a.RecordedAt.Equal(b.RecordedAt) {
		return a.RecordedAt.Before(b.RecordedAt)
	}
	return a.ID < b.ID
}

// The four states an invoice can be in, derived from three facts.
const (
	StatusDue        = "due"
	StatusPartlyPaid = "partly_paid"
	StatusOverdue    = "overdue"
	StatusPaid       = "paid"
)

// StatusOf derives an invoice's state from its total, what it has received and
// the date.
//
// `overdue` deliberately wins over both `due` and `partly_paid`: once a date has
// passed, what matters about the invoice is that it is late, and an operator
// scanning for who to chase must not have to read two columns to find out. The
// outstanding figure beside it still says how much is unpaid, so collapsing the
// two loses nothing.
func StatusOf(invoice Invoice, receivedMinor int64, now time.Time) string {
	if receivedMinor >= invoice.TotalMinor {
		return StatusPaid
	}
	if invoice.DueAt.Before(now) {
		return StatusOverdue
	}
	if receivedMinor > 0 {
		return StatusPartlyPaid
	}
	return StatusDue
}

// OutstandingOn is what one invoice still owes: invoiced minus received, never
// negative.
//
// An invoice of 50,000 holding 30,000 contributes 20,000, not its face value.
// Of the two directions a money figure can be wrong in, overstating the debt is
// the one that loses a customer's trust.
func OutstandingOn(invoice Invoice, receivedMinor int64) int64 {
	if receivedMinor >= invoice.TotalMinor {
		return 0
	}
	return invoice.TotalMinor - receivedMinor
}

// CurrencyTotals is money summed for ONE currency. Always a list, one entry per
// currency present — two currencies are never added together and there is no
// combined figure anywhere.
type CurrencyTotals struct {
	Currency         string
	InvoicedMinor    int64
	ReceivedMinor    int64
	OutstandingMinor int64
	// The share of OutstandingMinor whose due date has passed. A SUBSET of it,
	// never a separate pot to be added to it.
	OverdueMinor int64
}

// Row is an invoice with its derived figures already attached, which is what
// both the totals and the projection read.
type Row struct {
	Invoice          Invoice
	ReceivedMinor    int64
	OutstandingMinor int64
	Status           string
}

// Rows derives every invoice's figures in one pass.
func Rows(invoices []Invoice, settlement Settlement, now time.Time) []Row {
	rows := make([]Row, 0, len(invoices))
	for _, invoice := range invoices {
		received := settlement.ReceivedByInvoice[invoice.ID]
		rows = append(rows, Row{
			Invoice:          invoice,
			ReceivedMinor:    received,
			OutstandingMinor: OutstandingOn(invoice, received),
			Status:           StatusOf(invoice, received, now),
		})
	}
	return rows
}

// TotalsFor sums a whole filtered set, one entry per currency, ordered by
// currency so two identical sets always produce identical output.
//
// The caller passes every row matching the filter, NOT the page being returned:
// a total summed from one page is wrong the moment a second page exists, and it
// looks authoritative while being wrong.
func TotalsFor(rows []Row) []CurrencyTotals {
	index := map[string]*CurrencyTotals{}
	for _, row := range rows {
		totals, seen := index[row.Invoice.Currency]
		if !seen {
			totals = &CurrencyTotals{Currency: row.Invoice.Currency}
			index[row.Invoice.Currency] = totals
		}
		totals.InvoicedMinor += row.Invoice.TotalMinor
		totals.ReceivedMinor += row.ReceivedMinor
		totals.OutstandingMinor += row.OutstandingMinor
		if row.Status == StatusOverdue {
			totals.OverdueMinor += row.OutstandingMinor
		}
	}
	ordered := make([]CurrencyTotals, 0, len(index))
	for _, totals := range index {
		ordered = append(ordered, *totals)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Currency < ordered[j].Currency })
	return ordered
}

// OutstandingFor is what a tenant owes right now in one currency — the figure a
// credit limit is measured against. Tax-inclusive, because the limit caps what
// they owe and what they owe includes the tax.
func OutstandingFor(invoices []Invoice, settlement Settlement, currency string) int64 {
	var total int64
	for _, invoice := range invoices {
		if invoice.Currency != currency {
			continue
		}
		total += OutstandingOn(invoice, settlement.ReceivedByInvoice[invoice.ID])
	}
	return total
}

// UnappliedFor is money a tenant has paid that no invoice has claimed yet, in
// one currency.
func UnappliedFor(payments []Payment, settlement Settlement, currency string) int64 {
	var total int64
	for _, payment := range payments {
		if payment.Currency != currency || payment.VoidedAt != nil {
			continue
		}
		total += settlement.UnappliedByPayment[payment.ID]
	}
	return total
}

// CreditHeadroom reports how far a proposed credit would take a tenant past
// their limit, and zero when it fits.
//
// Unapplied money counts. A tenant who has overpaid has already handed over
// money no invoice has claimed, and the next invoice issued absorbs it on the
// very next allocation pass — so measuring the proposal against outstanding
// alone refuses credit the tenant has already paid for. The arithmetic here is
// the same arithmetic the settlement pass will do a moment later.
func CreditHeadroom(outstandingMinor, unappliedMinor, proposedTotalMinor, limitMinor int64) int64 {
	projected := outstandingMinor + proposedTotalMinor - unappliedMinor
	if projected < 0 {
		projected = 0
	}
	if projected <= limitMinor {
		return 0
	}
	return projected - limitMinor
}

// MatchesFilter reports whether a status is in the set a filter names.
//
// `due` means "not fully settled", overdue included: an overdue invoice is
// still money owed, and an operator asking what is outstanding must not have to
// ask twice. `overdue` narrows that to the late ones — a SUBSET of due, never a
// sibling, so the two counts are never added together.
//
// Every case is named and the default is unreachable. It used to be the `due`
// branch, which meant every undeclared string — `pad`, `OVERDUE`, a one-letter
// typo — was answered as `due`: the late invoices plus every invoice still
// comfortably inside its terms, with nothing on screen to say the question had
// not been understood. The filter is now validated at the HTTP edge
// (invoiceStatusFilter), so anything reaching here has already been checked,
// and the default refuses rather than guessing.
func MatchesFilter(status, filter string) bool {
	switch filter {
	case "":
		return true
	case StatusPaid:
		return status == StatusPaid
	case StatusOverdue:
		return status == StatusOverdue
	case StatusDue:
		return status != StatusPaid
	default:
		// Unreachable: the edge refuses an undeclared filter with a 422. If it
		// is ever reached, matching nothing is the safe direction — an empty
		// list reads as "no rows", which is visibly a question worth re-asking,
		// where a full-looking list does not.
		return false
	}
}
