// Package connector is the seam between us and a carrier. Adding an
// aggregator or an SMPP bind means one new implementation of Connector — no
// change to the gate, the state machine, or the ledger.
package connector

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Submission is one message handed to a carrier.
type Submission struct {
	MessageID string
	Msisdn    string
	Sender    string
	Body      string
	Channel   string
	Country   string

	// CarrierTemplateID is the id the CARRIER issued when it approved this
	// template — not Relay's own template id. RCS has no free-form send outside
	// an open conversation: both Indian gateways refuse one, so an RCS
	// submission without this is refused here rather than at the gateway.
	//
	// Empty for every other channel, where Body is the message.
	CarrierTemplateID string

	// TemplateVariables fill the template's slots.
	//
	// Ordered AND named, because the two carriers disagree about which they
	// need: Airtel's placeholders are positional ({{1}}, {{2}}) and it refuses
	// the send if fewer values arrive than the template declares; Vi's are named
	// ([DISCOUNT]) and travel as a JSON object. Relay's own templates are named.
	// Keeping both in one ordered list of pairs is what stops the two views
	// from drifting apart on the same message.
	//
	// Airtel caps each value at 30 characters.
	TemplateVariables []TemplateVariable

	// TTLSeconds stops delivery attempts after a while and revokes the message.
	// Zero means the template's own TTL applies, or none. A send-time value
	// overrides the template's on both carriers.
	TTLSeconds int
}

// TemplateVariable is one filled slot.
type TemplateVariable struct {
	Name  string
	Value string
}

// Receipt is what a carrier says at submit time. Accepted here means the
// carrier took the message — NOT that a handset received it. Delivery arrives
// later and asynchronously, which is why this type deliberately has no
// "delivered" field to be misread.
type Receipt struct {
	MessageID  string
	Accepted   bool
	CarrierRef string
	ErrorCode  string
}

var ErrConnectorUnavailable = errors.New("connector: unavailable")

type Health struct {
	Healthy bool
	Detail  string
}

// Connector submits batches. Batching is in the interface rather than left to
// callers because every real carrier protocol wants it — SMPP multiplexes over
// one bind, and HTTP aggregators charge per request.
type Connector interface {
	Name() string
	Submit(ctx context.Context, submissions []Submission) ([]Receipt, error)
	Health(ctx context.Context) Health
}

// DeliveryReport is a carrier telling us what happened to a message. It
// arrives minutes to hours after submission, over a completely separate
// channel from the submit call.
type DeliveryReport struct {
	CarrierRef string
	MessageID  string
	Delivered  bool
	ErrorCode  string
	OccurredAt time.Time
}

// submitEach satisfies the batching in Connector for carriers that have no
// batch endpoint — both RCS gateways send one message per request.
//
// Concurrency is bounded because these carriers throttle per ACCOUNT, not per
// message: Airtel returns 429 for the whole customer id at 40 TPS by default.
// One campaign fanning out unbounded would stop every other tenant on the same
// deployment from sending, so the limit here is deliberately conservative.
//
// A failure becomes a rejected receipt rather than an error, because the batch
// as a whole did not fail — one message did, and the caller has to be able to
// tell those apart to know whether money moved.
func submitEach(ctx context.Context, send func(context.Context, Submission) Receipt, submissions []Submission) ([]Receipt, error) {
	const workers = 8

	receipts := make([]Receipt, len(submissions))
	jobs := make(chan int)

	var wg sync.WaitGroup
	for i := 0; i < workers && i < len(submissions); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				receipts[index] = send(ctx, submissions[index])
			}
		}()
	}

	go func() {
		defer close(jobs)
		for i := range submissions {
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	wg.Wait()

	// A cancelled context leaves zero-valued receipts behind, which would read
	// as "carrier declined" — a silent, wrong, and expensive answer.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return receipts, nil
}
