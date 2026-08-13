// Package connector is the seam between us and a carrier. Adding an
// aggregator or an SMPP bind means one new implementation of Connector — no
// change to the gate, the state machine, or the ledger.
package connector

import (
	"context"
	"errors"
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
