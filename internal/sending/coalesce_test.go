package sending_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"
)

// The batched send path exists for throughput, so every test here runs sends
// CONCURRENTLY. Run them one at a time and each batch is a batch of one, which
// is the unbatched path wearing a hat — it would pass while proving nothing.

// withCoalescer turns the fixture's service into the batched one. The resolver
// hands out a service WITHOUT a coalescer, which is what the API does too: a
// batch's own service must not be able to start another batch.
func (f *fixture) withCoalescer() *sending.Coalescer {
	f.t.Helper()
	coalescer := sending.NewCoalescer(func(context.Context) *sending.Service {
		return &sending.Service{DB: f.service.DB, ClickHouse: f.service.ClickHouse,
			Connector: f.service.Connector}
	})
	coalescer.Start()
	f.t.Cleanup(coalescer.Stop)
	f.service.Coalescer = coalescer
	return coalescer
}

// sendAll fires every request at once and returns the results in order.
func (f *fixture) sendAll(requests []sending.SendRequest) ([]sending.SendResult, []error) {
	f.t.Helper()
	results := make([]sending.SendResult, len(requests))
	errs := make([]error, len(requests))
	var wait sync.WaitGroup
	for i, request := range requests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[i], errs[i] = f.service.Send(context.Background(), f.identity, request)
		}()
	}
	wait.Wait()
	return results, errs
}

func (f *fixture) requests(count int, body string) []sending.SendRequest {
	requests := make([]sending.SendRequest, count)
	for i := range requests {
		requests[i] = sending.SendRequest{
			SenderID: f.senderID, TemplateID: &f.templateID,
			// Distinct recipients: identical ones would be a legitimate reason
			// for a batch to behave differently, and this is not testing that.
			Msisdn: fmt.Sprintf("98765%05d", i+10),
			Body:   body,
		}
	}
	return requests
}

// The money test. A batch takes ONE wallet movement for many messages, so an
// arithmetic slip here does not misprice one message — it misprices the batch,
// in either direction. Sixty concurrent sends must cost exactly sixty messages.
func TestEveryMessageInABatchIsChargedExactlyOnce(t *testing.T) {
	f := newFixture(t)
	f.withCoalescer()
	before := f.balance()

	const count = 60
	results, errs := f.sendAll(f.requests(count, "Your order has shipped."))

	seen := map[uuid.UUID]bool{}
	for i, result := range results {
		if errs[i] != nil {
			t.Fatalf("send %d: %v", i, errs[i])
		}
		if result.Status != "sent" {
			t.Fatalf("send %d: status = %q, want sent", i, result.Status)
		}
		if result.CostMinor != 12 {
			t.Fatalf("send %d: cost = %d, want 12", i, result.CostMinor)
		}
		if result.MessageID == uuid.Nil {
			t.Fatalf("send %d: no message id — the caller cannot look it up", i)
		}
		if seen[result.MessageID] {
			t.Fatalf("send %d: message id %s was handed to two callers", i, result.MessageID)
		}
		seen[result.MessageID] = true
	}

	if spent := before - f.balance(); spent != count*12 {
		t.Fatalf("the batch spent %d, want %d — a batched hold must charge for "+
			"exactly the messages it covers, no more and no fewer", spent, count*12)
	}
}

// Zero miss: every id the API hands back must be findable afterwards. A batch
// that answered its callers and then failed to write is the failure this
// catches, and it is the one that cannot be recovered from — the recipient got
// the message and we have no record of it.
func TestEveryMessageInABatchIsRecorded(t *testing.T) {
	f := newFixture(t)
	f.withCoalescer()

	results, errs := f.sendAll(f.requests(40, "Recorded?"))
	ids := make([]uuid.UUID, 0, len(results))
	for i, result := range results {
		if errs[i] != nil {
			t.Fatalf("send %d: %v", i, errs[i])
		}
		ids = append(ids, result.MessageID)
	}

	for _, id := range ids {
		if _, err := store.LoadMessageState(context.Background(), f.service.ClickHouse,
			f.identity.TenantID, id); err != nil {
			t.Fatalf("message %s was accepted but is not in the log: %v", id, err)
		}
	}
}

// A batch must refuse per message, not per batch. One suppressed recipient
// among many is one rejection — not a batch that fails, and not a batch that
// sends to them anyway because their neighbours were fine.
func TestASuppressedRecipientInABatchIsRefusedAlone(t *testing.T) {
	f := newFixture(t)
	f.withCoalescer()
	before := f.balance()

	requests := f.requests(20, "Hello")
	suppressed := "+919876500015"
	requests[5].Msisdn = suppressed
	if _, err := store.AddSuppression(context.Background(), f.service.DB, f.identity,
		store.Suppression{Identity: suppressed, Reason: "manual"}); err != nil {
		t.Fatalf("suppress: %v", err)
	}

	results, errs := f.sendAll(requests)
	for i, result := range results {
		if i == 5 {
			if result.Status == "sent" {
				t.Fatal("a suppressed recipient was sent to inside a batch — the " +
					"gate must apply per message, not per batch")
			}
			if result.CostMinor != 0 {
				t.Fatalf("a refused message cost %d, want 0", result.CostMinor)
			}
			continue
		}
		if errs[i] != nil {
			t.Fatalf("send %d: %v", i, errs[i])
		}
		if result.Status != "sent" {
			t.Fatalf("send %d: status = %q — one refusal must not take its "+
				"neighbours down with it", i, result.Status)
		}
	}

	if spent := before - f.balance(); spent != 19*12 {
		t.Fatalf("the batch spent %d, want %d for the 19 it actually sent", spent, 19*12)
	}
}

// The carrier's own refusal, mid-batch. The hold for the message it refused
// must come back, and only that one.
func TestACarrierRejectionInsideABatchReleasesOnlyItsOwnHold(t *testing.T) {
	f := newFixture(t)
	f.withCoalescer()
	before := f.balance()

	requests := f.requests(15, "Hello")
	// The sandbox rejects any number ending 000 at submit time.
	requests[7].Msisdn = "9876543000"

	results, errs := f.sendAll(requests)
	for i, result := range results {
		if errs[i] != nil {
			t.Fatalf("send %d: %v", i, errs[i])
		}
		if i == 7 {
			if result.Status == "sent" {
				t.Fatal("the carrier refused this message and it was reported as sent")
			}
			continue
		}
		if result.Status != "sent" {
			t.Fatalf("send %d: status = %q, want sent", i, result.Status)
		}
	}

	if spent := before - f.balance(); spent != 14*12 {
		t.Fatalf("the batch spent %d, want %d — a carrier rejection inside a "+
			"batch must release its own hold and no one else's", spent, 14*12)
	}
}

// A wallet that runs out partway through a batch must refuse the remainder
// rather than going negative. The balance is checked against a RUNNING total
// inside the batch, because every message in it is charged by one ledger entry
// that has not been written yet when the gate runs.
func TestAWalletThatRunsDryMidBatchNeverGoesNegative(t *testing.T) {
	f := newFixture(t)
	f.withCoalescer()

	// Spend the fixture's funding down to exactly ten messages' worth. The
	// ledger stores magnitudes and takes the direction from the type, so a
	// draw-down is a charge rather than a negative entry.
	balance := f.balance()
	if _, err := store.AppendLedgerEntry(context.Background(), f.service.DB, f.identity,
		store.LedgerEntry{Currency: "INR", Type: "charge",
			AmountMinor: balance - 10*12,
			Description: "Draw down to ten messages' worth"}); err != nil {
		t.Fatalf("draw down wallet: %v", err)
	}
	if remaining := f.balance(); remaining != 10*12 {
		t.Fatalf("balance = %d after draw-down, want %d", remaining, 10*12)
	}

	results, errs := f.sendAll(f.requests(30, "Hello"))
	sent := 0
	for i, result := range results {
		if errs[i] != nil && result.Status == "" {
			t.Fatalf("send %d failed outright rather than being refused: %v", i, errs[i])
		}
		if result.Status == "sent" {
			sent++
		}
	}

	if sent != 10 {
		t.Fatalf("%d messages were sent on a wallet that could afford 10", sent)
	}
	if after := f.balance(); after < 0 {
		t.Fatalf("balance went negative (%d) — a batch overdrew the wallet", after)
	}
}

// The batched and unbatched paths must be the same send. If they can disagree
// about status, cost or segments then turning batching on changes what
// customers are told and charged, which is not a performance change.
func TestTheBatchedPathAgreesWithTheUnbatchedOne(t *testing.T) {
	f := newFixture(t)

	body := "Your verification code is 123456."
	direct, err := f.service.Send(context.Background(), f.identity, sending.SendRequest{
		SenderID: f.senderID, TemplateID: &f.templateID, Msisdn: "9876543210", Body: body,
	})
	if err != nil {
		t.Fatalf("unbatched send: %v", err)
	}

	f.withCoalescer()
	batched, err := f.service.Send(context.Background(), f.identity, sending.SendRequest{
		SenderID: f.senderID, TemplateID: &f.templateID, Msisdn: "9876543211", Body: body,
	})
	if err != nil {
		t.Fatalf("batched send: %v", err)
	}

	if batched.Status != direct.Status {
		t.Fatalf("status: batched %q, unbatched %q", batched.Status, direct.Status)
	}
	if batched.CostMinor != direct.CostMinor {
		t.Fatalf("cost: batched %d, unbatched %d", batched.CostMinor, direct.CostMinor)
	}
	if batched.Segments != direct.Segments {
		t.Fatalf("segments: batched %d, unbatched %d", batched.Segments, direct.Segments)
	}
	if batched.Currency != direct.Currency {
		t.Fatalf("currency: batched %q, unbatched %q", batched.Currency, direct.Currency)
	}
}

// An unknown sender is still an unknown sender inside a batch, and must not
// make the batch fail for everyone else in it.
func TestAnUnknownSenderInABatchIsRefusedAlone(t *testing.T) {
	f := newFixture(t)
	f.withCoalescer()

	requests := f.requests(10, "Hello")
	requests[3].SenderID = uuid.New()

	results, errs := f.sendAll(requests)
	if results[3].FailureCode != "sender_not_found" {
		t.Fatalf("failure code = %q, want sender_not_found", results[3].FailureCode)
	}
	for i := range results {
		if i == 3 {
			continue
		}
		if errs[i] != nil {
			t.Fatalf("send %d: %v", i, errs[i])
		}
		if results[i].Status != "sent" {
			t.Fatalf("send %d: status = %q, want sent", i, results[i].Status)
		}
	}
}

// The messages table is ORDER BY (tenant_id, created_at, id) and collapses on
// version, so created_at is part of a row's IDENTITY, not just a column on it.
// A settled row written with a fresh created_at does not replace the queued row
// — it sits beside it forever, and the message is in the log twice.
//
// This is cheap to get wrong and invisible until someone counts messages, which
// is why it is asserted directly rather than through a status read.
func TestASettledMessageKeepsTheCreatedAtItWasQueuedWith(t *testing.T) {
	f := newFixture(t)
	f.withCoalescer()

	results, errs := f.sendAll(f.requests(12, "Hello"))
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("send %d: %v", i, errs[i])
		}
	}

	for _, result := range results {
		var distinct uint64
		if err := f.service.ClickHouse.QueryRow(context.Background(),
			`SELECT uniqExact(created_at) FROM messages WHERE tenant_id = ? AND id = ?`,
			f.identity.TenantID, result.MessageID).Scan(&distinct); err != nil {
			t.Fatalf("count created_at: %v", err)
		}
		if distinct != 1 {
			t.Fatalf("message %s has %d different created_at values — its queued "+
				"and settled rows will never collapse into one message",
				result.MessageID, distinct)
		}
	}
}
