package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

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

// Spend on four channels is reported as four channels.
//
// It used to be reported as one row labelled SMS, whatever the tenant actually
// sent: the query grouped the wallet ledger by currency and stamped "SMS" on
// every row it produced, because a charge row carries no channel. On live data
// that renamed 290 WhatsApp, 274 RCS, 253 email and 211 voice messages to SMS
// and reported a third of the real volume — a wrong answer rather than a
// missing one, which is why nobody had reported it.
//
// The same superseded-row hazard as the journey test below applies: WhatsApp's
// message is written twice, delivered then read, and must still count once.
func TestUsageSplitsSpendAcrossTheChannelsThatEarnedIt(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")

	ctx := context.Background()
	conn, err := h.server.ClickHouse.Conn(ctx)
	if err != nil {
		t.Fatalf("clickhouse: %v", err)
	}
	batch, err := conn.PrepareBatch(ctx, `INSERT INTO messages (
		tenant_id, id, channel, country, sender_header, msisdn, status,
		fraud_flag, segments, cost_minor, currency,
		created_at, updated_at, version)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	now := time.Now().UTC()
	whatsapp := uuid.New()
	type messageRow struct {
		id      uuid.UUID
		channel string
		status  string
		cost    int64
		version uint64
	}
	for _, r := range []messageRow{
		{uuid.New(), "SMS", "delivered", 500, 1},
		{uuid.New(), "SMS", "delivered", 500, 1},
		{whatsapp, "WHATSAPP", "delivered", 1_200, 1},
		{whatsapp, "WHATSAPP", "read", 1_200, 2},
		{uuid.New(), "RCS", "delivered", 900, 1},
		// Undelivered, so it is not billable and must not appear at all.
		{uuid.New(), "VOICE", "undelivered", 4_000, 1},
	} {
		if err := batch.Append(acct.TenantID, r.id, r.channel, "IN", "ACMERT",
			"+919820000001", r.status, "none", uint8(1), r.cost, "INR",
			now, now, r.version); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatalf("send batch: %v", err)
	}

	res := h.do(http.MethodGet, "/v1/billing/usage?range=30d&currency=INR", acct.Token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body)
	}
	var report gen.UsageReport
	res.decode(t, &report)

	got := map[string]struct{ amount, count int }{}
	for _, row := range report.ByChannel {
		got[string(row.Channel)] = struct{ amount, count int }{row.AmountMinor, row.MessageCount}
	}
	want := map[string]struct{ amount, count int }{
		"SMS":      {1_000, 2},
		"WHATSAPP": {1_200, 1},
		"RCS":      {900, 1},
	}
	for channel, expected := range want {
		if got[channel] != expected {
			t.Errorf("%s = %+v, want %+v (all rows: %+v)",
				channel, got[channel], expected, report.ByChannel)
		}
	}
	if _, billed := got["VOICE"]; billed {
		t.Errorf("an undelivered message was billed: %+v", got["VOICE"])
	}
}

// A journey's messageCount is its real volume, not that volume multiplied by
// how many send steps settled.
//
// This is the trap the frontend asked us to prove we had avoided: a campaign
// settles one ledger charge row, a journey settles one PER settled send step,
// so accumulating the count the way amountMinor is accumulated silently
// multiplies a journey's volume by its step count with no error to catch it.
// The count here is taken from the message rows and grouped by the journey, so
// charge rows never enter into it.
//
// The second hazard is this table's engine. messages is a ReplacingMergeTree:
// every status change writes another row for the same id, and the versions
// coexist until a background merge collapses them. Counting rows would report
// one number before a merge and another after, with no data change in between.
// So the two send steps below each get a superseded 'sent' row alongside their
// final 'delivered' one, and the answer must still be 2.
func TestJourneyMessageCountIsNotMultipliedByItsSteps(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")

	ctx := context.Background()
	conn, err := h.server.ClickHouse.Conn(ctx)
	if err != nil {
		t.Fatalf("clickhouse: %v", err)
	}
	batch, err := conn.PrepareBatch(ctx, `INSERT INTO messages (
		tenant_id, id, journey_id, journey_name, channel, country, sender_header,
		msisdn, status, fraud_flag, segments, cost_minor, currency,
		created_at, updated_at, version)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	journeyID := uuid.New()
	now := time.Now().UTC()
	// Two distinct messages, one per settled send step.
	//
	// The first is written twice: delivered, then read. Both statuses are
	// billable, so a count of ROWS sees three messages where there are two, and
	// a sum over rows whose status is exactly 'delivered' loses the first
	// message's charge as soon as a merge collapses it to its 'read' row.
	first, second := uuid.New(), uuid.New()
	type versionRow struct {
		id      uuid.UUID
		status  string
		version uint64
	}
	for _, r := range []versionRow{
		{first, "delivered", 1},
		{first, "read", 2},
		{second, "delivered", 1},
	} {
		if err := batch.Append(acct.TenantID, r.id, &journeyID,
			ptr("Winback"), "SMS", "IN", "ACMERT", "+919820000001", r.status,
			"none", uint8(1), int64(500), "INR", now, now, r.version); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatalf("send batch: %v", err)
	}

	res := h.do(http.MethodGet, "/v1/billing/usage?range=30d&currency=INR", acct.Token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body)
	}
	var report gen.UsageReport
	res.decode(t, &report)

	if len(report.ByJourney) != 1 {
		t.Fatalf("got %d journey rows, want 1: %+v", len(report.ByJourney), report.ByJourney)
	}
	row := report.ByJourney[0]
	if row.MessageCount != 2 {
		t.Errorf("messageCount = %d, want 2 — three rows for two messages across two steps",
			row.MessageCount)
	}
	if row.AmountMinor != 1000 {
		t.Errorf("amountMinor = %d, want 1000", row.AmountMinor)
	}
}

func ptr[T any](v T) *T { return &v }
