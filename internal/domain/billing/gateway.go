// Package billing holds money rules that know nothing about HTTP or SQL.
package billing

import (
	"context"
	"errors"
	"fmt"
)

// ErrCaptureDeclined is returned when a payment is refused. Callers translate
// it to a 422 rather than a 500 — a declined card is a normal outcome, not a
// system failure.
var ErrCaptureDeclined = errors.New("billing: payment declined")

// Capture is a request to take money from a stored payment method.
type Capture struct {
	TenantID        string
	PaymentMethodID string
	Currency        string
	AmountMinor     int64
	Reference       string
}

// Receipt is what a gateway returns on success.
type Receipt struct {
	Provider      string
	ProviderRef   string
	CapturedMinor int64
}

// PaymentGateway is the seam a real provider slots into. Adding Razorpay or
// Stripe means one new implementation of this interface plus a config switch —
// no change to the ledger, the handlers, or the schema.
//
// Building the seam now costs an afternoon. Retrofitting it once the ledger
// holds real customer money costs a migration and a reconciliation exercise.
type PaymentGateway interface {
	Name() string
	Capture(ctx context.Context, capture Capture) (Receipt, error)
}

// ManualGateway records a capture without contacting anyone.
//
// This is not a stub standing in for "the real thing later": prepaid balances
// settled by bank transfer or invoice are how a large share of Indian A2P
// business actually works, and for those customers this IS the real flow. An
// operator confirms the transfer and the balance moves.
//
// What it deliberately does not do is pretend a card was charged. When card
// payments are wanted, a gateway implementation handles them and this one stays
// for the customers who never use cards.
type ManualGateway struct{}

func (ManualGateway) Name() string { return "manual" }

func (ManualGateway) Capture(_ context.Context, capture Capture) (Receipt, error) {
	if capture.AmountMinor <= 0 {
		return Receipt{}, fmt.Errorf("%w: amount must be positive", ErrCaptureDeclined)
	}
	return Receipt{
		Provider:      "manual",
		ProviderRef:   "manual:" + capture.Reference,
		CapturedMinor: capture.AmountMinor,
	}, nil
}
