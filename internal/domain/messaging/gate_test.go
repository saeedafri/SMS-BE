package messaging_test

import (
	"errors"
	"testing"

	"github.com/saeedafri/sms-be/internal/domain/messaging"
)

func validInput() messaging.GateInput {
	return messaging.GateInput{
		TenantStatus: "active", SenderStatus: "approved", SenderID: "sender-1",
		TemplateStatus: "approved", TemplateSender: "sender-1",
		Suppressed: false, BalanceMinor: 10_000, CostMinor: 12, RecipientValid: true,
	}
}

func TestGatePassesACompliantSend(t *testing.T) {
	if err := messaging.Check(validInput()); err != nil {
		t.Fatalf("a fully compliant send was refused: %v", err)
	}
}

func TestGateRefusesEachViolation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*messaging.GateInput)
		wantErr error
	}{
		{"suspended tenant", func(i *messaging.GateInput) { i.TenantStatus = "suspended" },
			messaging.ErrTenantSuspended},
		{"unapproved sender", func(i *messaging.GateInput) { i.SenderStatus = "pending_review" },
			messaging.ErrSenderNotApproved},
		{"rejected sender", func(i *messaging.GateInput) { i.SenderStatus = "rejected" },
			messaging.ErrSenderNotApproved},
		{"unapproved template", func(i *messaging.GateInput) { i.TemplateStatus = "pending_review" },
			messaging.ErrTemplateNotApproved},
		{"template from another sender", func(i *messaging.GateInput) { i.TemplateSender = "sender-2" },
			messaging.ErrSenderTemplateMismatch},
		{"suppressed recipient", func(i *messaging.GateInput) { i.Suppressed = true },
			messaging.ErrSuppressed},
		{"insufficient balance", func(i *messaging.GateInput) { i.BalanceMinor = 5 },
			messaging.ErrInsufficientFunds},
		{"invalid recipient", func(i *messaging.GateInput) { i.RecipientValid = false },
			messaging.ErrInvalidRecipient},
		{"an RCS sender with no agent identity", func(i *messaging.GateInput) {
			i.RCSAgentRequired = true
			i.RCSAgentResolved = false
		}, messaging.ErrRCSAgentNotResolved},
		{"promotional outside the permitted hours", func(i *messaging.GateInput) {
			i.OutsidePromotionalWindow = true
		}, messaging.ErrOutsidePromotionalWindow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := validInput()
			tc.mutate(&input)
			err := messaging.Check(input)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if code := messaging.GateFailureCode(err); code == "" || code == "rejected" {
				t.Errorf("failure code = %q, want something specific", code)
			}
		})
	}
}

// Order matters: telling someone "insufficient balance" when the real problem
// is an unapproved sender sends them to fix the wrong thing, and they will top
// up money they did not need to spend.
func TestComplianceFailuresAreReportedBeforeMoney(t *testing.T) {
	input := validInput()
	input.SenderStatus = "pending_review"
	input.BalanceMinor = 0 // both wrong at once

	if err := messaging.Check(input); !errors.Is(err, messaging.ErrSenderNotApproved) {
		t.Fatalf("err = %v, want the sender problem reported before the balance", err)
	}
}

// A suppressed recipient must never be billed for, not even momentarily, so
// suppression is checked before the balance.
func TestSuppressionIsCheckedBeforeBalance(t *testing.T) {
	input := validInput()
	input.Suppressed = true
	input.BalanceMinor = 0

	if err := messaging.Check(input); !errors.Is(err, messaging.ErrSuppressed) {
		t.Fatalf("err = %v, want suppression reported before the balance", err)
	}
}

// A single-send with no template is legitimate (an OTP composed in code), so
// an empty template status must not be treated as unapproved.
func TestATemplatelessSendIsAllowed(t *testing.T) {
	input := validInput()
	input.TemplateStatus = ""
	input.TemplateSender = ""

	if err := messaging.Check(input); err != nil {
		t.Fatalf("a send with no template was refused: %v", err)
	}
}

// Exactly-equal balance must pass: refusing it would strand the last message a
// customer can afford.
func TestExactBalanceIsSufficient(t *testing.T) {
	input := validInput()
	input.BalanceMinor = 12
	input.CostMinor = 12

	if err := messaging.Check(input); err != nil {
		t.Fatalf("a balance exactly equal to the cost was refused: %v", err)
	}
}

// The brand a handset draws is checked before money, and only where a real
// gateway is configured.
//
// Both halves matter. Refusing after the hold would take and release money on a
// message that was never going to leave; refusing when no RCS gateway exists
// would stop every send on a deployment that has not got carrier credentials
// yet — which is every deployment until they land.
func TestTheAgentCheckCostsNothingAndOnlyBindsWhereACarrierExists(t *testing.T) {
	unresolved := validInput()
	unresolved.RCSAgentRequired = true
	unresolved.BalanceMinor = 0
	if err := messaging.Check(unresolved); !errors.Is(err, messaging.ErrRCSAgentNotResolved) {
		t.Errorf("err = %v, want the agent refusal ahead of the balance one", err)
	}

	// No gateway configured: not required, so nothing to resolve.
	noCarrier := validInput()
	noCarrier.RCSAgentRequired = false
	noCarrier.RCSAgentResolved = false
	if err := messaging.Check(noCarrier); err != nil {
		t.Errorf("a send with no RCS gateway was refused: %v", err)
	}

	resolved := validInput()
	resolved.RCSAgentRequired = true
	resolved.RCSAgentResolved = true
	if err := messaging.Check(resolved); err != nil {
		t.Errorf("a resolved agent was refused: %v", err)
	}
}

// The dispatch invariant: nothing leaves with a hole in it.
//
// The fan-out already skips a contact it cannot personalise, so this refusal
// never fires in a working system. That is what it is for — it is the check
// that still holds when the fill is the thing that broke.
func TestAMessageWithAnUnfilledVariableIsRefused(t *testing.T) {
	input := validInput()
	input.UnresolvedVariables = true
	if err := messaging.Check(input); !errors.Is(err, messaging.ErrVariableUnresolved) {
		t.Fatalf("Check = %v, want ErrVariableUnresolved", err)
	}
	if code := messaging.GateFailureCode(messaging.ErrVariableUnresolved); code != "variable_unresolved" {
		t.Fatalf("code = %q, want variable_unresolved", code)
	}
	if !messaging.IsRefusal(messaging.ErrVariableUnresolved) {
		t.Fatal("an unfilled variable is a refusal, not a fault: it must not read as a 500")
	}
}

// Refused before the carrier's own verdict, so the reason a customer reads is
// the one they can act on. A template the carrier has not approved and a
// variable nothing filled are two different fixes, and only the second would
// otherwise be DELIVERED rather than refused.
func TestAnUnfilledVariableIsReportedBeforeTheCarriersVerdict(t *testing.T) {
	input := validInput()
	input.UnresolvedVariables = true
	input.CarrierTemplateStatus = "pending"
	if err := messaging.Check(input); !errors.Is(err, messaging.ErrVariableUnresolved) {
		t.Fatalf("Check = %v, want ErrVariableUnresolved", err)
	}
}
