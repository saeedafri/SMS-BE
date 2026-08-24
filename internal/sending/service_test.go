package sending_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/domain/messaging"
	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"
)

type fixture struct {
	t        *testing.T
	service  *sending.Service
	sandbox  *connector.Sandbox
	identity store.Identity
	senderID uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pgURL, adminURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_DATABASE_ADMIN_URL")
	chURL := os.Getenv("TEST_CLICKHOUSE_URL")
	if pgURL == "" || adminURL == "" || chURL == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DATABASE_ADMIN_URL / TEST_CLICKHOUSE_URL not set")
	}
	ctx := context.Background()

	pool, err := store.Open(ctx, pgURL)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	t.Cleanup(pool.Close)
	admin, err := store.Open(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	t.Cleanup(admin.Close)
	ch, err := store.OpenClickHouse(ctx, chURL)
	if err != nil {
		t.Fatalf("open clickhouse: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close() })

	tenantID, senderID := uuid.New(), uuid.New()
	if _, err := admin.Exec(ctx,
		`INSERT INTO tenants (id, name, country) VALUES ($1, 'Send Co', 'IN')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	// The sender is approved: the gate's job is tested separately, and these
	// tests are about what happens once it passes.
	if _, err := admin.Exec(ctx,
		`INSERT INTO sender_ids (id, tenant_id, header, channel, country, status)
		 VALUES ($1, $2, 'SENDCO', 'SMS', 'IN', 'approved')`, senderID, tenantID); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	t.Cleanup(func() {
		// The wallet ledger is append-only and its trigger fires on the CASCADE
		// from tenants, so a tenant that funded a wallet — which this fixture
		// always does — could not be deleted at all, and the error was
		// discarded. Disabled for this statement only, exactly as the demo
		// reseed does.
		if _, err := admin.Exec(context.Background(),
			`ALTER TABLE wallet_ledger DISABLE TRIGGER wallet_ledger_append_only`); err == nil {
			defer func() {
				_, _ = admin.Exec(context.Background(),
					`ALTER TABLE wallet_ledger ENABLE TRIGGER wallet_ledger_append_only`)
			}()
		}
		if _, err := admin.Exec(context.Background(),
			`DELETE FROM tenants WHERE id = $1`, tenantID); err != nil {
			t.Logf("tenant %s was not cleaned up: %v", tenantID, err)
		}
		// ClickHouse rows must go too. The reconciler sweeps every tenant, so a
		// test tenant left behind in ClickHouse but deleted from Postgres is an
		// orphan that fails to settle forever and pollutes every later run.
		// mutations_sync so the delete has actually happened when this returns.
		// ClickHouse mutations are asynchronous by default, so the rows outlived
		// the test that made them and piled up across a day of runs — 363 across
		// 59 tenants — until they aged past a validity window and broke a sweep
		// test that had nothing to do with them.
		_ = ch.Exec(context.Background(),
			"ALTER TABLE messages DELETE WHERE tenant_id = ? SETTINGS mutations_sync = 1",
			tenantID)
	})

	identity := store.Identity{TenantID: tenantID}
	// Fund the wallet so the gate's balance check passes.
	if _, err := store.AppendLedgerEntry(ctx, pool, identity, store.LedgerEntry{
		Currency: "INR", Type: "topup", AmountMinor: 1_000_000,
	}); err != nil {
		t.Fatalf("fund wallet: %v", err)
	}

	sandbox := connector.NewSandbox(0)
	return &fixture{
		t: t, sandbox: sandbox, identity: identity, senderID: senderID,
		service: &sending.Service{DB: pool, ClickHouse: ch, Connector: sandbox},
	}
}

func (f *fixture) balance() int64 {
	f.t.Helper()
	balances, err := store.ListWalletBalances(context.Background(), f.service.DB, f.identity)
	if err != nil {
		f.t.Fatalf("balance: %v", err)
	}
	for _, entry := range balances {
		if entry.Currency == "INR" {
			return entry.BalanceMinor
		}
	}
	return 0
}

func (f *fixture) send(msisdn, body string) (sending.SendResult, error) {
	f.t.Helper()
	return f.service.Send(context.Background(), f.identity, sending.SendRequest{
		SenderID: f.senderID, Msisdn: msisdn, Body: body,
	})
}

// The whole pipeline: gate, hold, submit, delivery report, settlement.
func TestDeliveredMessageIsChargedOnce(t *testing.T) {
	f := newFixture(t)
	before := f.balance()

	result, err := f.send("9876543210", "Your order has shipped.")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	// Carrier-accepted is reported as sent, never delivered.
	if result.Status != "sent" {
		t.Fatalf("status = %q immediately after submit, want sent", result.Status)
	}
	if result.CostMinor != 12 {
		t.Fatalf("cost = %d, want 12 (1 segment at the IN SMS rate)", result.CostMinor)
	}

	// The hold has already left the wallet.
	if held := f.balance(); held != before-12 {
		t.Fatalf("balance = %d after the hold, want %d", held, before-12)
	}

	for _, report := range f.sandbox.DrainReports() {
		if err := f.service.ApplyDeliveryReport(context.Background(), f.identity, report); err != nil {
			t.Fatalf("apply report: %v", err)
		}
	}

	// Delivery converts the hold to a real charge — the money stays spent.
	if after := f.balance(); after != before-12 {
		t.Fatalf("balance = %d after delivery, want %d", after, before-12)
	}
}

// The product's headline promise. An undelivered message must cost nothing,
// and the refund must be automatic rather than a support ticket.
func TestUndeliveredMessageIsNotCharged(t *testing.T) {
	f := newFixture(t)
	before := f.balance()

	// The sandbox fails any number ending 001 with ABSENT_SUBSCRIBER.
	if _, err := f.send("9876543001", "Hello"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if held := f.balance(); held != before-12 {
		t.Fatalf("balance = %d after the hold, want %d", held, before-12)
	}

	for _, report := range f.sandbox.DrainReports() {
		if err := f.service.ApplyDeliveryReport(context.Background(), f.identity, report); err != nil {
			t.Fatalf("apply report: %v", err)
		}
	}

	if after := f.balance(); after != before {
		t.Fatalf("balance = %d after an undelivered message, want the original %d — "+
			"we must never charge for a message that did not arrive", after, before)
	}
}

// A carrier refusing at submit releases the hold immediately: nothing was
// delivered, so nothing is owed, and the customer should not wait for a
// receipt that will never come.
func TestCarrierRejectionReleasesTheHoldImmediately(t *testing.T) {
	f := newFixture(t)
	before := f.balance()

	// The sandbox rejects any number ending 000 at submit time.
	result, err := f.send("9876543000", "Hello")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %q, want failed", result.Status)
	}
	if after := f.balance(); after != before {
		t.Fatalf("balance = %d after a carrier rejection, want %d", after, before)
	}
}

// Carriers retry receipts. Applying the same one twice must not refund twice —
// which would let a customer manufacture credit by replaying a webhook.
func TestReplayedDeliveryReportIsIgnored(t *testing.T) {
	f := newFixture(t)
	before := f.balance()

	if _, err := f.send("9876543001", "Hello"); err != nil {
		t.Fatalf("send: %v", err)
	}
	reports := f.sandbox.DrainReports()
	for range 3 {
		for _, report := range reports {
			if err := f.service.ApplyDeliveryReport(context.Background(), f.identity, report); err != nil {
				t.Fatalf("apply report: %v", err)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	if after := f.balance(); after != before {
		t.Fatalf("balance = %d after replaying a failure receipt three times, want %d — "+
			"a replayed receipt refunded more than once", after, before)
	}
}

// The gate must refuse before any money moves.
func TestGateRefusalCostsNothing(t *testing.T) {
	f := newFixture(t)
	before := f.balance()

	suppressed := "+919876543210"
	if _, err := store.AddSuppression(context.Background(), f.service.DB, f.identity,
		store.Suppression{Identity: suppressed, Msisdn: &suppressed, Reason: "manual"}); err != nil {
		t.Fatalf("suppress: %v", err)
	}

	result, err := f.send("9876543210", "Hello")
	if !errors.Is(err, messaging.ErrSuppressed) {
		t.Fatalf("err = %v, want ErrSuppressed", err)
	}
	if result.FailureCode != "recipient_suppressed" {
		t.Fatalf("failureCode = %q, want recipient_suppressed", result.FailureCode)
	}
	if after := f.balance(); after != before {
		t.Fatalf("balance = %d after a gate refusal, want %d — nothing should have moved",
			after, before)
	}
}

// A report for a message we never sent must be dropped, not trusted. Otherwise
// anyone who can reach the ingest endpoint can move our state.
func TestReportForAnUnknownMessageIsDropped(t *testing.T) {
	f := newFixture(t)
	before := f.balance()

	err := f.service.ApplyDeliveryReport(context.Background(), f.identity,
		connector.DeliveryReport{MessageID: uuid.New().String(), Delivered: false})
	if err != nil {
		t.Fatalf("apply unknown report: %v", err)
	}
	if after := f.balance(); after != before {
		t.Fatalf("balance moved on a report for an unknown message")
	}
}

// Longer bodies cost more, using the same segment arithmetic the estimate
// endpoint quotes — so the quote and the charge cannot disagree.
func TestMultiSegmentMessageCostsProportionally(t *testing.T) {
	f := newFixture(t)
	body := ""
	for range 161 {
		body += "a"
	}
	result, err := f.send("9876543210", body)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if result.Segments != 2 {
		t.Fatalf("segments = %d, want 2", result.Segments)
	}
	if result.CostMinor != 24 {
		t.Fatalf("cost = %d, want 24 (2 segments at 12)", result.CostMinor)
	}
}
