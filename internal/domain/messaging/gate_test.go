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
