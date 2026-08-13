package messaging_test

import (
	"testing"

	"github.com/saeedafri/sms-be/internal/domain/messaging"
)

func TestLegalTransitions(t *testing.T) {
	legal := [][2]messaging.State{
		{messaging.StateQueued, messaging.StateSubmitting},
		{messaging.StateSubmitting, messaging.StateSubmitted},
		{messaging.StateSubmitted, messaging.StateAccepted},
		{messaging.StateAccepted, messaging.StateDelivered},
		{messaging.StateAccepted, messaging.StateUndelivered},
		{messaging.StateSubmitted, messaging.StateExpired},
	}
	for _, pair := range legal {
		if !messaging.CanTransition(pair[0], pair[1]) {
			t.Errorf("%s -> %s should be legal", pair[0], pair[1])
		}
	}
}

// A terminal state is final. A carrier replaying an old receipt must not be
// able to walk a delivered message back to accepted, or a failed one to
// delivered — either would corrupt both the log and the billing that follows it.
func TestTerminalStatesAreFinal(t *testing.T) {
	terminal := []messaging.State{
		messaging.StateDelivered, messaging.StateUndelivered,
		messaging.StateRejected, messaging.StateExpired,
	}
	every := append([]messaging.State{
		messaging.StateQueued, messaging.StateSubmitting,
		messaging.StateSubmitted, messaging.StateAccepted,
	}, terminal...)

	for _, from := range terminal {
		if !messaging.IsTerminal(from) {
			t.Errorf("%s should be terminal", from)
		}
		for _, to := range every {
			if messaging.CanTransition(from, to) {
				t.Errorf("%s -> %s should be refused; %s is terminal", from, to, from)
			}
		}
	}
}

// Skipping states must be refused: a message cannot go straight from queued to
// delivered, because nothing would ever have been submitted to a carrier.
func TestIllegalTransitionsAreRefused(t *testing.T) {
	illegal := [][2]messaging.State{
		{messaging.StateQueued, messaging.StateDelivered},
		{messaging.StateQueued, messaging.StateAccepted},
		{messaging.StateSubmitting, messaging.StateDelivered},
		{messaging.StateSubmitted, messaging.StateDelivered},
	}
	for _, pair := range illegal {
		if messaging.CanTransition(pair[0], pair[1]) {
			t.Errorf("%s -> %s should be refused", pair[0], pair[1])
		}
	}
}

// The product's headline promise, expressed as code: money converts on exactly
// one state, and every other terminal outcome gives it back.
func TestOnlyDeliveryCharges(t *testing.T) {
	if got := messaging.EffectOf(messaging.StateDelivered); got != messaging.EffectCharge {
		t.Fatalf("delivered effect = %q, want charge", got)
	}
	for _, state := range []messaging.State{
		messaging.StateUndelivered, messaging.StateRejected, messaging.StateExpired,
	} {
		if got := messaging.EffectOf(state); got != messaging.EffectRelease {
			t.Errorf("%s effect = %q, want release — we must never charge for an undelivered message", state, got)
		}
	}
	for _, state := range []messaging.State{
		messaging.StateQueued, messaging.StateSubmitting,
		messaging.StateSubmitted, messaging.StateAccepted,
	} {
		if got := messaging.EffectOf(state); got != messaging.EffectNone {
			t.Errorf("%s effect = %q, want none while still in flight", state, got)
		}
	}
}

// Carrier-accepted is not handset-confirmed. If accepted ever maps to
// "delivered", the product is lying to its users about the one thing it claims
// to be honest about.
func TestAcceptedIsReportedAsSentNotDelivered(t *testing.T) {
	if got := messaging.ContractStatus(messaging.StateAccepted); got != "sent" {
		t.Fatalf("accepted maps to %q, want sent — a carrier accepting is not a handset receiving", got)
	}
	if got := messaging.ContractStatus(messaging.StateDelivered); got != "delivered" {
		t.Fatalf("delivered maps to %q, want delivered", got)
	}
}

func TestContractStatusCoversEveryState(t *testing.T) {
	valid := map[string]bool{
		"queued": true, "sent": true, "delivered": true, "failed": true, "read": true,
	}
	for _, state := range []messaging.State{
		messaging.StateQueued, messaging.StateSubmitting, messaging.StateSubmitted,
		messaging.StateAccepted, messaging.StateDelivered, messaging.StateUndelivered,
		messaging.StateRejected, messaging.StateExpired,
	} {
		got := messaging.ContractStatus(state)
		if !valid[got] {
			t.Errorf("%s maps to %q, which is not in the contract's MessageStatus enum", state, got)
		}
	}
}

// A carrier code on its own tells a developer nothing. The PRD calls plain
// -language error explanations a differentiator, so every class carries one.
func TestCarrierErrorsAreClassifiedAndExplained(t *testing.T) {
	cases := map[string]messaging.ErrorClass{
		"ABSENT_SUBSCRIBER": messaging.ErrorUnreachable,
		"DND_BLOCKED":       messaging.ErrorBlocked,
		"SPAM_BLOCKED":      messaging.ErrorBlocked,
		"INVALID_SENDER":    messaging.ErrorRejected,
		"TEMPLATE_MISMATCH": messaging.ErrorRejected,
		"EXPIRED":           messaging.ErrorExpired,
		"SOMETHING_NEW":     messaging.ErrorRejected,
	}
	for code, wantClass := range cases {
		class, explanation := messaging.ClassifyCarrierError(code)
		if class != wantClass {
			t.Errorf("%s classified as %q, want %q", code, class, wantClass)
		}
		if explanation == "" {
			t.Errorf("%s has no plain-language explanation", code)
		}
	}
	if class, explanation := messaging.ClassifyCarrierError(""); class != "" || explanation != "" {
		t.Error("an empty code should classify as nothing")
	}
}
