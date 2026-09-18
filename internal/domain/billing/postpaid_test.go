package billing_test

import (
	"testing"
	"time"

	"github.com/saeedafri/sms-be/internal/domain/billing"
)

// The eight rules the schemas cannot express, one test each, named after the
// rule it protects. A wrong implementation of any one of them fails the test
// that names it and no others — which is what makes a failure here point at the
// rule rather than at "money is wrong somewhere".

var kolkata = mustLoad("Asia/Kolkata")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func at(iso string) time.Time {
	moment, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		panic(err)
	}
	return moment
}

func invoice(id, number, currency string, taxable int64, rate int, issued, due string) billing.Invoice {
	tax := billing.TaxOn(taxable, rate)
	return billing.Invoice{
		ID: id, Number: number, Currency: currency,
		TaxableMinor: taxable, TaxRatePercent: rate, TaxMinor: tax,
		TotalMinor: taxable + tax, IssuedAt: at(issued), DueAt: at(due),
	}
}

func payment(id, currency string, amount int64, received string) billing.Payment {
	return billing.Payment{
		ID: id, Currency: currency, AmountMinor: amount,
		ReceivedAt: at(received), RecordedAt: at(received),
	}
}

// 4.1 — tax goes ON TOP, never carved out.
func TestTaxIsChargedOnTopSoTheFullCreditReachesTheWallet(t *testing.T) {
	// Loading 50,000 at 18% means 50,000 of sending power and 59,000 owed.
	// Carving it out would put 42,372.88 in the wallet — less than the operator
	// typed, and nobody notices until a customer says their balance is short.
	if tax := billing.TaxOn(5_000_000, 18); tax != 900_000 {
		t.Errorf("tax on 5,000,000 at 18%% = %d, want 900000", tax)
	}
	if total := int64(5_000_000) + billing.TaxOn(5_000_000, 18); total != 5_900_000 {
		t.Errorf("total = %d, want 5900000", total)
	}
	// Zero is a rate, not "unset": a credit that carries no tax owes the amount.
	if tax := billing.TaxOn(5_000_000, 0); tax != 0 {
		t.Errorf("tax at 0%% = %d, want 0", tax)
	}
	// Rounded to the paisa, half away from zero, and never by float.
	for _, tc := range []struct {
		taxable int64
		rate    int
		want    int64
	}{
		{333, 18, 60}, // 59.94 → 60
		{1, 18, 0},    // 0.18 → 0
		{3, 18, 1},    // 0.54 → 1
		{100, 5, 5},   // exact
		{12345, 20, 2469},
	} {
		if got := billing.TaxOn(tc.taxable, tc.rate); got != tc.want {
			t.Errorf("TaxOn(%d, %d) = %d, want %d", tc.taxable, tc.rate, got, tc.want)
		}
	}
}

// The due date is the end of the month IN THE TENANT'S OWN TIME.
//
// This is the rule the frontend's mock gets wrong by computing in UTC. A credit
// issued at 02:00 IST on 1 October is 20:30 UTC on 30 September, so a UTC month
// end falls due at 05:29 that same morning: an invoice raised on the 1st, due
// before breakfast.
func TestAMonthEndsInTheTenantsOwnTimezoneNotInUTC(t *testing.T) {
	issued := at("2026-10-01T02:00:00+05:30")
	due := billing.MonthEnd(issued, kolkata)

	if got := due.In(kolkata).Format("2006-01-02 15:04:05"); got != "2026-10-31 23:59:59" {
		t.Errorf("month end for 1 Oct 02:00 IST = %s, want 2026-10-31 23:59:59 IST", got)
	}
	if !due.After(issued) {
		t.Errorf("an invoice issued at %s is due at %s — in the past", issued, due)
	}
	// The same instant read in UTC lands in September, which is exactly the
	// mistake: a UTC month end would be 30 Sep, three and a half hours away.
	if issued.UTC().Month() != time.September {
		t.Fatalf("test premise broken: %s is not September in UTC", issued.UTC())
	}
	// A February that a naive day-arithmetic implementation gets wrong.
	if got := billing.MonthEnd(at("2028-02-03T10:00:00+05:30"), kolkata).
		In(kolkata).Format("2006-01-02"); got != "2028-02-29" {
		t.Errorf("leap February ends %s, want 2028-02-29", got)
	}
	if got := billing.MonthEnd(at("2026-12-15T10:00:00+05:30"), kolkata).
		In(kolkata).Format("2006-01-02"); got != "2026-12-31" {
		t.Errorf("December ends %s, want 2026-12-31", got)
	}
}

// 4.2 — status is DERIVED, never stored, and overdue wins.
func TestStatusIsDerivedAndOverdueBeatsPartlyPaid(t *testing.T) {
	now := at("2026-10-15T00:00:00Z")
	open := invoice("i1", "INV-2026-10-001", "INR", 100_000, 0,
		"2026-10-01T00:00:00Z", "2026-10-31T23:59:59Z")
	late := invoice("i2", "INV-2026-09-001", "INR", 100_000, 0,
		"2026-09-01T00:00:00Z", "2026-09-30T23:59:59Z")

	for name, tc := range map[string]struct {
		invoice  billing.Invoice
		received int64
		want     string
	}{
		"nothing paid, not yet due":     {open, 0, billing.StatusDue},
		"some paid, not yet due":        {open, 40_000, billing.StatusPartlyPaid},
		"paid in full":                  {open, 100_000, billing.StatusPaid},
		"overpaid is still paid":        {open, 150_000, billing.StatusPaid},
		"nothing paid and late":         {late, 0, billing.StatusOverdue},
		"partly paid and late is LATE":  {late, 40_000, billing.StatusOverdue},
		"paid in full beats being late": {late, 100_000, billing.StatusPaid},
	} {
		if got := billing.StatusOf(tc.invoice, tc.received, now); got != tc.want {
			t.Errorf("%s = %q, want %q", name, got, tc.want)
		}
	}

	// Derived means it turns over at the instant, with no job to run.
	justBefore := late.DueAt.Add(-time.Nanosecond)
	justAfter := late.DueAt.Add(time.Nanosecond)
	if got := billing.StatusOf(late, 0, justBefore); got != billing.StatusDue {
		t.Errorf("a nanosecond before the due date = %q, want due", got)
	}
	if got := billing.StatusOf(late, 0, justAfter); got != billing.StatusOverdue {
		t.Errorf("a nanosecond after the due date = %q, want overdue", got)
	}
}

// 4.3 — outstanding is invoiced MINUS received.
func TestOutstandingIsInvoicedMinusReceivedAndNeverNegative(t *testing.T) {
	one := invoice("i1", "INV-2026-10-001", "INR", 5_000_000, 18,
		"2026-10-01T00:00:00Z", "2026-10-31T23:59:59Z")
	// 50,000 + 18% = 59,000 owed; 30,000 received leaves 29,000, not 59,000.
	if got := billing.OutstandingOn(one, 3_000_000); got != 2_900_000 {
		t.Errorf("outstanding = %d, want 2900000", got)
	}
	if got := billing.OutstandingOn(one, 0); got != 5_900_000 {
		t.Errorf("untouched outstanding = %d, want the full 5900000", got)
	}
	// An overpaid invoice owes nothing; it never owes a negative amount that
	// would net off against another invoice's real debt.
	if got := billing.OutstandingOn(one, 9_000_000); got != 0 {
		t.Errorf("overpaid outstanding = %d, want 0", got)
	}
}

// 4.5 — one payment per transfer, allocated oldest-first, in receipt order.
func TestAPaymentSettlesTheOldestInvoicesFirstInTheOrderMoneyLanded(t *testing.T) {
	oldest := invoice("i1", "INV-2026-08-001", "INR", 100_000, 0,
		"2026-08-01T00:00:00Z", "2026-08-31T23:59:59Z")
	middle := invoice("i2", "INV-2026-09-001", "INR", 100_000, 0,
		"2026-09-01T00:00:00Z", "2026-09-30T23:59:59Z")
	newest := invoice("i3", "INV-2026-10-001", "INR", 100_000, 0,
		"2026-10-01T00:00:00Z", "2026-10-31T23:59:59Z")

	// One transfer of 250,000 settles two invoices whole and half of a third.
	// Recording it per invoice would mean entering the same UTR three times.
	settlement := billing.Settle(
		[]billing.Invoice{newest, oldest, middle}, // deliberately out of order
		[]billing.Payment{payment("p1", "INR", 250_000, "2026-10-05T00:00:00Z")})

	if got := settlement.ReceivedByInvoice["i1"]; got != 100_000 {
		t.Errorf("oldest received %d, want 100000", got)
	}
	if got := settlement.ReceivedByInvoice["i2"]; got != 100_000 {
		t.Errorf("middle received %d, want 100000", got)
	}
	if got := settlement.ReceivedByInvoice["i3"]; got != 50_000 {
		t.Errorf("newest received %d, want 50000", got)
	}
	if got := settlement.UnappliedByPayment["p1"]; got != 0 {
		t.Errorf("unapplied %d, want 0", got)
	}
	// The allocations are reported oldest due first, which is the order an
	// operator reconciles in.
	if len(settlement.Allocations) != 3 ||
		settlement.Allocations[0].InvoiceID != "i1" ||
		settlement.Allocations[1].InvoiceID != "i2" ||
		settlement.Allocations[2].InvoiceID != "i3" {
		t.Errorf("allocations = %+v, want i1, i2, i3 in that order", settlement.Allocations)
	}

	// Payments apply in the order money LANDED, not the order it was typed.
	// Entered second, landed first: it must take the oldest invoice.
	late := payment("late", "INR", 100_000, "2026-10-09T00:00:00Z")
	early := payment("early", "INR", 100_000, "2026-10-02T00:00:00Z")
	order := billing.Settle([]billing.Invoice{oldest, middle}, []billing.Payment{late, early})
	for _, allocation := range order.Allocations {
		if allocation.InvoiceID == "i1" && allocation.PaymentID != "early" {
			t.Errorf("the oldest invoice was settled by %q, want the money that landed first",
				allocation.PaymentID)
		}
	}
}

// 4.5 — money left over is held, and rupees never settle a dollar invoice.
func TestOverpaymentIsHeldAndCurrenciesNeverMeet(t *testing.T) {
	rupees := invoice("i1", "INV-2026-10-001", "INR", 100_000, 0,
		"2026-10-01T00:00:00Z", "2026-10-31T23:59:59Z")
	dollars := invoice("i2", "INV-2026-10-001", "USD", 100_000, 0,
		"2026-10-01T00:00:00Z", "2026-10-31T23:59:59Z")

	settlement := billing.Settle(
		[]billing.Invoice{rupees, dollars},
		[]billing.Payment{payment("p1", "INR", 500_000, "2026-10-05T00:00:00Z")})

	if got := settlement.ReceivedByInvoice["i2"]; got != 0 {
		t.Errorf("rupees settled %d of a dollar invoice, want 0", got)
	}
	// Held against the account rather than refused: the next invoice issued
	// absorbs it on the next run of this same pass.
	if got := settlement.UnappliedByPayment["p1"]; got != 400_000 {
		t.Errorf("unapplied = %d, want 400000 held", got)
	}
	if got := billing.UnappliedFor(
		[]billing.Payment{payment("p1", "INR", 500_000, "2026-10-05T00:00:00Z")},
		settlement, "INR"); got != 400_000 {
		t.Errorf("unapplied in INR = %d, want 400000", got)
	}

	// Issue the next invoice and the held money claims it, with no second act.
	next := invoice("i3", "INV-2026-11-001", "INR", 300_000, 0,
		"2026-11-01T00:00:00Z", "2026-11-30T23:59:59Z")
	after := billing.Settle(
		[]billing.Invoice{rupees, dollars, next},
		[]billing.Payment{payment("p1", "INR", 500_000, "2026-10-05T00:00:00Z")})
	if got := after.ReceivedByInvoice["i3"]; got != 300_000 {
		t.Errorf("the next invoice received %d of the held money, want 300000", got)
	}
	if got := after.UnappliedByPayment["p1"]; got != 100_000 {
		t.Errorf("still held = %d, want 100000", got)
	}
}

// 4.6 — voiding reverses, it does not delete.
func TestVoidingAPaymentGivesEveryInvoiceItsDebtBack(t *testing.T) {
	one := invoice("i1", "INV-2026-10-001", "INR", 100_000, 0,
		"2026-10-01T00:00:00Z", "2026-10-31T23:59:59Z")
	voidedAt := at("2026-10-20T00:00:00Z")

	settled := payment("p1", "INR", 100_000, "2026-10-05T00:00:00Z")
	before := billing.Settle([]billing.Invoice{one}, []billing.Payment{settled})
	if got := billing.StatusOf(one, before.ReceivedByInvoice["i1"],
		at("2026-10-10T00:00:00Z")); got != billing.StatusPaid {
		t.Fatalf("test premise broken: invoice is %q before the void", got)
	}

	// The whole of voiding: one field. The pass excludes it and every dependent
	// figure corrects itself, with no unwinding logic to get wrong.
	settled.VoidedAt = &voidedAt
	after := billing.Settle([]billing.Invoice{one}, []billing.Payment{settled})
	if got := after.ReceivedByInvoice["i1"]; got != 0 {
		t.Errorf("a voided payment still settles %d, want 0", got)
	}
	if got := billing.StatusOf(one, after.ReceivedByInvoice["i1"],
		at("2026-10-10T00:00:00Z")); got != billing.StatusDue {
		t.Errorf("after voiding the invoice is %q, want due again", got)
	}
	// A voided payment holds nothing back either.
	if got := after.UnappliedByPayment["p1"]; got != 0 {
		t.Errorf("a voided payment holds %d unapplied, want 0", got)
	}
}

// 4.4 — totals are per currency, over the whole set, and overdue is a SUBSET.
func TestTotalsAreOneRowPerCurrencyAndOverdueIsInsideOutstanding(t *testing.T) {
	now := at("2026-10-15T00:00:00Z")
	late := invoice("i1", "INV-2026-09-001", "INR", 100_000, 0,
		"2026-09-01T00:00:00Z", "2026-09-30T23:59:59Z")
	open := invoice("i2", "INV-2026-10-001", "INR", 200_000, 0,
		"2026-10-01T00:00:00Z", "2026-10-31T23:59:59Z")
	dollars := invoice("i3", "INV-2026-10-001", "USD", 50_000, 0,
		"2026-10-01T00:00:00Z", "2026-10-31T23:59:59Z")

	invoices := []billing.Invoice{late, open, dollars}
	// 40,000 lands: oldest first, so it part-pays the late one.
	settlement := billing.Settle(invoices,
		[]billing.Payment{payment("p1", "INR", 40_000, "2026-10-05T00:00:00Z")})
	totals := billing.TotalsFor(billing.Rows(invoices, settlement, now))

	if len(totals) != 2 {
		t.Fatalf("totals = %+v, want one row per currency", totals)
	}
	if totals[0].Currency != "INR" || totals[1].Currency != "USD" {
		t.Errorf("totals are not ordered by currency: %+v", totals)
	}
	inr := totals[0]
	if inr.InvoicedMinor != 300_000 || inr.ReceivedMinor != 40_000 {
		t.Errorf("INR invoiced/received = %d/%d, want 300000/40000",
			inr.InvoicedMinor, inr.ReceivedMinor)
	}
	// Invoiced minus received, not the face value of what is unpaid.
	if inr.OutstandingMinor != 260_000 {
		t.Errorf("INR outstanding = %d, want 260000", inr.OutstandingMinor)
	}
	// The late invoice owes 60,000 of that 260,000. A subset, never a pot to
	// be added on top — 260,000 + 60,000 would invent 60,000 of debt.
	if inr.OverdueMinor != 60_000 {
		t.Errorf("INR overdue = %d, want 60000", inr.OverdueMinor)
	}
	if inr.OverdueMinor > inr.OutstandingMinor {
		t.Errorf("overdue %d exceeds outstanding %d — it is a subset",
			inr.OverdueMinor, inr.OutstandingMinor)
	}
	if usd := totals[1]; usd.OutstandingMinor != 50_000 || usd.OverdueMinor != 0 {
		t.Errorf("USD row = %+v, want 50000 outstanding and nothing overdue", usd)
	}
}

// 4.7 — the credit limit measures OUTSTANDING, tax-inclusive, and counts money
// the tenant has already handed over.
func TestTheCreditLimitMeasuresWhatIsOwedAndCreditsWhatIsAlreadyPaid(t *testing.T) {
	const limit = 10_000_000 // ₹1,00,000

	for name, tc := range map[string]struct {
		outstanding, unapplied, proposed int64
		wantOver                         int64
	}{
		"fits exactly":             {0, 0, 10_000_000, 0},
		"one paisa past":           {0, 0, 10_000_001, 1},
		"already owes most of it":  {9_900_000, 0, 300_000, 200_000},
		"nothing owed, small load": {0, 0, 100_000, 0},
		// The one the frontend's mock refuses wrongly: a tenant who has
		// overpaid has already handed over money no invoice has claimed, and
		// the settlement pass claims it the moment the invoice exists.
		"overpaid tenant is not blocked": {0, 5_000_000, 11_800_000, 0},
		"overpaid but still too large":   {0, 5_000_000, 16_000_000, 1_000_000},
	} {
		got := billing.CreditHeadroom(tc.outstanding, tc.unapplied, tc.proposed, limit)
		if got != tc.wantOver {
			t.Errorf("%s: over by %d, want %d", name, got, tc.wantOver)
		}
	}

	// Tax-inclusive: the limit caps what they owe and what they owe includes
	// the tax, so a 90,000 load at 18% is 1,06,200 against the limit.
	taxable := int64(9_000_000)
	total := taxable + billing.TaxOn(taxable, 18)
	if over := billing.CreditHeadroom(0, 0, total, limit); over == 0 {
		t.Errorf("a %d load at 18%% fits under a %d limit — the tax was not counted",
			taxable, limit)
	}

	// Outstanding is what it measures, not the face value of unpaid invoices.
	one := invoice("i1", "INV-2026-10-001", "INR", 5_000_000, 18,
		"2026-10-01T00:00:00Z", "2026-10-31T23:59:59Z")
	settlement := billing.Settle([]billing.Invoice{one},
		[]billing.Payment{payment("p1", "INR", 3_000_000, "2026-10-05T00:00:00Z")})
	if got := billing.OutstandingFor([]billing.Invoice{one}, settlement, "INR"); got != 2_900_000 {
		t.Errorf("outstanding for the limit = %d, want 2900000 (not the 5900000 face value)", got)
	}
	// Per currency: a dollar debt does not consume a rupee limit.
	if got := billing.OutstandingFor([]billing.Invoice{one}, settlement, "USD"); got != 0 {
		t.Errorf("a rupee invoice contributed %d to the USD total", got)
	}
}

// The filter the paged routes and both exports share.
func TestDueMeansNotFullySettledAndOverdueNarrowsItToTheLateOnes(t *testing.T) {
	for name, tc := range map[string]struct {
		status, filter string
		want           bool
	}{
		"no filter keeps everything":  {billing.StatusPaid, "", true},
		"due keeps due":               {billing.StatusDue, "due", true},
		"due keeps partly paid":       {billing.StatusPartlyPaid, "due", true},
		"due keeps overdue":           {billing.StatusOverdue, "due", true},
		"due drops paid":              {billing.StatusPaid, "due", false},
		"paid keeps only paid":        {billing.StatusPaid, "paid", true},
		"paid drops overdue":          {billing.StatusOverdue, "paid", false},
		"overdue keeps only the late": {billing.StatusOverdue, "overdue", true},
		"overdue drops partly paid":   {billing.StatusPartlyPaid, "overdue", false},
		// The default branch used to be `due`, so every undeclared string was
		// answered as due — `?status=ovedue`, one letter out, returned the late
		// invoices PLUS every invoice comfortably inside its terms, and looked
		// like a working answer. The filter is validated at the HTTP edge now,
		// and if anything still reaches here it matches nothing: an empty list
		// reads as a question worth re-asking, a full-looking list does not.
		"a typo matches nothing":   {billing.StatusOverdue, "ovedue", false},
		"a typo does not mean due": {billing.StatusDue, "ovedue", false},
		"case is not folded":       {billing.StatusOverdue, "OVERDUE", false},
		"nonsense matches nothing": {billing.StatusPaid, "pad", false},
	} {
		if got := billing.MatchesFilter(tc.status, tc.filter); got != tc.want {
			t.Errorf("%s: MatchesFilter(%q, %q) = %v, want %v",
				name, tc.status, tc.filter, got, tc.want)
		}
	}
}

// The settlement pass must not depend on the order rows come back in. A
// database is free to return them in any order, and two operators reading the
// same account must see the same allocations.
func TestSettlementIsTheSameWhateverOrderTheRowsArriveIn(t *testing.T) {
	// Two invoices sharing a due date, separated only by issue date and number,
	// which is exactly where an unstable sort would show.
	a := invoice("a", "INV-2026-10-001", "INR", 100_000, 0,
		"2026-10-01T00:00:00Z", "2026-10-31T23:59:59Z")
	b := invoice("b", "INV-2026-10-002", "INR", 100_000, 0,
		"2026-10-02T00:00:00Z", "2026-10-31T23:59:59Z")
	money := payment("p1", "INR", 150_000, "2026-10-05T00:00:00Z")

	forward := billing.Settle([]billing.Invoice{a, b}, []billing.Payment{money})
	backward := billing.Settle([]billing.Invoice{b, a}, []billing.Payment{money})

	for _, id := range []string{"a", "b"} {
		if forward.ReceivedByInvoice[id] != backward.ReceivedByInvoice[id] {
			t.Errorf("invoice %s received %d one way and %d the other",
				id, forward.ReceivedByInvoice[id], backward.ReceivedByInvoice[id])
		}
	}
	// The one issued first takes the money.
	if forward.ReceivedByInvoice["a"] != 100_000 {
		t.Errorf("the invoice issued first received %d, want all 100000",
			forward.ReceivedByInvoice["a"])
	}
}
