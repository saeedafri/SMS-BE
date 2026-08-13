package sending_test

import (
	"context"
	"testing"
	"time"

	"github.com/saeedafri/sms-be/internal/store"
)

// The limbo case. A carrier accepted the message and then went silent — no
// report will ever arrive. Without the reconciler the tenant is charged forever
// for a message nobody can prove was delivered.
func TestReconcilerExpiresSilentMessagesAndRefunds(t *testing.T) {
	f := newFixture(t)
	before := f.balance()

	// The sandbox accepts any number ending 003 and never reports on it.
	result, err := f.send("9876543003", "Hello")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if reports := f.sandbox.DrainReports(); len(reports) != 0 {
		t.Fatalf("sandbox emitted %d reports for a silent number, want 0", len(reports))
	}
	if held := f.balance(); held != before-12 {
		t.Fatalf("balance = %d after the hold, want %d", held, before-12)
	}

	// A window this message is not yet old enough for: it is still in flight,
	// and expiring it early would refund a message that may still arrive.
	expired, err := f.service.Reconcile(context.Background(), time.Hour, 100)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if expired != 0 {
		t.Fatalf("reconciler expired %d messages inside the validity window, want 0", expired)
	}
	if held := f.balance(); held != before-12 {
		t.Fatalf("balance moved to %d during a no-op reconcile", held)
	}

	// Wait for the message to become visible to a RANGE scan. A point lookup by
	// id sees a fresh ClickHouse insert immediately, but a scan filtering on
	// updated_at takes a few hundred milliseconds to include it. That lag is
	// irrelevant in production — the reconciler runs every 15 minutes against
	// messages 48 hours old — so waiting here keeps the test honest about what
	// it is asserting: the reconciler's behaviour, not ClickHouse's insert
	// visibility. Without this the test measures the wrong thing and fails
	// intermittently.
	visible := false
	for attempt := 0; attempt < 25 && !visible; attempt++ {
		stale, err := store.FindStaleMessages(context.Background(),
			f.service.ClickHouse, time.Now().UTC(), 200)
		if err != nil {
			t.Fatalf("find stale: %v", err)
		}
		for _, message := range stale {
			if message.ID == result.MessageID {
				visible = true
			}
		}
		if !visible {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !visible {
		t.Fatal("the message never became visible to the reconciler's sweep")
	}

	// Now past the window.
	if _, err := f.service.Reconcile(context.Background(), time.Nanosecond, 100); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if after := f.balance(); after != before {
		t.Fatalf("balance = %d after expiry, want the original %d — a message the "+
			"carrier never reported on must not be charged for", after, before)
	}

	// The label matters: `expired` says the carrier went silent, which is a
	// different diagnosis from `undelivered`, where it told us it failed.
	state, err := store.LoadMessageState(context.Background(), f.service.ClickHouse,
		f.identity.TenantID, result.MessageID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Status != "expired" {
		t.Fatalf("status = %q after reconciliation, want expired", state.Status)
	}

	// Running again must not refund a second time.
	if _, err := f.service.Reconcile(context.Background(), time.Nanosecond, 100); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if after := f.balance(); after != before {
		t.Fatalf("balance = %d after a repeat reconcile, want %d — expiry must be idempotent",
			after, before)
	}
}

// A message that was delivered normally must be invisible to the reconciler.
// If it is not, every delivered message eventually gets refunded and the
// business makes no money at all.
func TestReconcilerIgnoresSettledMessages(t *testing.T) {
	f := newFixture(t)
	before := f.balance()

	if _, err := f.send("9876543210", "Hello"); err != nil {
		t.Fatalf("send: %v", err)
	}
	for _, report := range f.sandbox.DrainReports() {
		if err := f.service.ApplyDeliveryReport(context.Background(), f.identity, report); err != nil {
			t.Fatalf("apply report: %v", err)
		}
	}

	expired, err := f.service.Reconcile(context.Background(), time.Nanosecond, 100)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if expired != 0 {
		t.Fatalf("reconciler touched %d already-delivered messages, want 0", expired)
	}
	if after := f.balance(); after != before-12 {
		t.Fatalf("balance = %d, want %d — a delivered message must stay charged", after, before-12)
	}
}
