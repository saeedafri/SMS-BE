package connector

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"time"
)

// Sandbox is a carrier that exists only in this process.
//
// It is not a mock in the testing sense — it is a real connector implementing
// the real interface, and it is how the entire pipeline gets exercised before
// any commercial agreement exists. It also stays useful afterwards: tenants
// need a test mode where sends cost nothing and reach nobody.
//
// Outcomes are deterministic from the recipient number rather than random, so
// a failing case is reproducible. Numbers ending in specific digits produce
// specific failures, which gives QA and customers a way to exercise every
// error path on demand:
//
//	…000  carrier rejects at submit
//	…001  accepted, then fails as ABSENT_SUBSCRIBER
//	…002  accepted, then blocked by DND
//	…003  accepted, then never reports (the reconciler must expire it)
//	all others  accepted, then delivered
type Sandbox struct {
	// Delay is how long after acceptance a delivery report is emitted. Zero
	// means immediate, which is what tests want.
	Delay time.Duration

	mu      sync.Mutex
	pending []DeliveryReport
}

func NewSandbox(delay time.Duration) *Sandbox {
	return &Sandbox{Delay: delay}
}

func (*Sandbox) Name() string { return "sandbox" }

func (*Sandbox) Health(context.Context) Health {
	return Health{Healthy: true, Detail: "sandbox connector is always available"}
}

func (s *Sandbox) Submit(_ context.Context, submissions []Submission) ([]Receipt, error) {
	receipts := make([]Receipt, 0, len(submissions))
	reports := make([]DeliveryReport, 0, len(submissions))
	now := time.Now().UTC()

	for _, submission := range submissions {
		outcome := outcomeFor(submission.Msisdn)
		if outcome == outcomeRejectAtSubmit {
			receipts = append(receipts, Receipt{
				MessageID: submission.MessageID,
				Accepted:  false,
				ErrorCode: "INVALID_SENDER",
			})
			continue
		}

		ref := carrierRef(submission.MessageID)
		receipts = append(receipts, Receipt{
			MessageID: submission.MessageID, Accepted: true, CarrierRef: ref,
		})

		// A message that never reports is the limbo case the reconciler
		// exists for, so the sandbox can produce it on demand.
		if outcome == outcomeNoReport {
			continue
		}

		report := DeliveryReport{
			CarrierRef: ref, MessageID: submission.MessageID,
			OccurredAt: now.Add(s.Delay),
		}
		switch outcome {
		case outcomeDelivered:
			report.Delivered = true
		case outcomeAbsentSubscriber:
			report.ErrorCode = "ABSENT_SUBSCRIBER"
		case outcomeDNDBlocked:
			report.ErrorCode = "DND_BLOCKED"
		}
		reports = append(reports, report)
	}

	s.mu.Lock()
	s.pending = append(s.pending, reports...)
	s.mu.Unlock()

	return receipts, nil
}

// DrainReports returns the delivery reports the carrier would have posted to
// our ingest endpoint, and clears them. A real connector never has this — the
// carrier calls us — so this exists purely to let a test or a local demo drive
// the asynchronous half of the pipeline synchronously.
func (s *Sandbox) DrainReports() []DeliveryReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	drained := s.pending
	s.pending = nil
	return drained
}

// TakeReportsFor removes and returns only the reports for one message, leaving
// every other message's queued.
//
// DrainReports empties the queue. A caller that wanted one message's report and
// called it anyway took everybody else's too and, having no use for them,
// dropped them on the floor — so those messages never settled, sat at "sent"
// forever, and were eventually expired by the reconciler as though the carrier
// had gone silent. Two callers wanting different messages could not both win.
//
// The background drainer still takes everything, which is right: it is the one
// caller whose job IS everything.
func (s *Sandbox) TakeReportsFor(messageID string) []DeliveryReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	mine := make([]DeliveryReport, 0, 1)
	kept := s.pending[:0]
	for _, report := range s.pending {
		if report.MessageID == messageID {
			mine = append(mine, report)
			continue
		}
		kept = append(kept, report)
	}
	s.pending = kept
	return mine
}

type outcome int

const (
	outcomeDelivered outcome = iota
	outcomeRejectAtSubmit
	outcomeAbsentSubscriber
	outcomeDNDBlocked
	outcomeNoReport
)

func outcomeFor(msisdn string) outcome {
	if len(msisdn) < 3 {
		return outcomeDelivered
	}
	switch msisdn[len(msisdn)-3:] {
	case "000":
		return outcomeRejectAtSubmit
	case "001":
		return outcomeAbsentSubscriber
	case "002":
		return outcomeDNDBlocked
	case "003":
		return outcomeNoReport
	default:
		return outcomeDelivered
	}
}

// carrierRef derives a stable pseudo-reference. Real carriers return their own
// opaque id; deriving ours from the message id keeps the sandbox reproducible
// across restarts.
func carrierRef(messageID string) string {
	sum := sha256.Sum256([]byte(messageID))
	return "sbx-" + hex(binary.BigEndian.Uint64(sum[:8]))
}

func hex(value uint64) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 16)
	for i := 15; i >= 0; i-- {
		out[i] = digits[value&0xF]
		value >>= 4
	}
	return string(out)
}
