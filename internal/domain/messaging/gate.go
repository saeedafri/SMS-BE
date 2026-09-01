package messaging

import (
	"errors"
	"fmt"
)

// The gate is the set of checks every message passes before it can be queued.
// Each failure is a distinct error so the caller can report which rule stopped
// it — "blocked" with no reason is exactly the opaque behaviour the PRD
// criticises incumbents for.
var (
	ErrTenantSuspended     = errors.New("messaging: tenant is suspended")
	ErrSenderNotApproved   = errors.New("messaging: sender is not approved")
	ErrTemplateNotApproved = errors.New("messaging: template is not approved")

	// ErrCarrierTemplateNotApproved is a DIFFERENT refusal from
	// ErrTemplateNotApproved and is deliberately not folded into it.
	//
	// An RCS template has two approvals: ours, which says the content meets
	// Relay's compliance rules, and the carrier's, which is a separate review
	// by Airtel or a Vi admin. A template approved here and unknown to the
	// carrier is refused at the gateway — after the hold has been taken. The
	// two errors go to different people to fix, so telling a customer "your
	// template is not approved" when we approved it ourselves last week sends
	// them arguing with the wrong team.
	ErrCarrierTemplateNotApproved = errors.New("messaging: the carrier has not approved this template")
	ErrSenderTemplateMismatch     = errors.New("messaging: template does not belong to that sender")
	ErrSuppressed                 = errors.New("messaging: recipient is suppressed")
	ErrInsufficientFunds          = errors.New("messaging: insufficient balance")
	ErrInvalidRecipient           = errors.New("messaging: recipient is not a valid number")
	// ErrContentNotAllowed is the country's own content rule refusing the body
	// — India's ban on public URL shorteners under DLT, today. Distinct from a
	// template refusal: the template may be perfectly approved and the text
	// still carry something the regulator does not permit.
	ErrContentNotAllowed = errors.New("messaging: content is not allowed in this country")
)

// GateInput is everything the gate needs to decide. It is a plain struct with
// no database or HTTP types so the rules stay testable in isolation — the
// order and completeness of these checks is the single most safety-critical
// thing in the system.
type GateInput struct {
	TenantStatus   string
	SenderStatus   string
	SenderID       string
	TemplateStatus string
	TemplateSender string

	// CarrierTemplateStatus is the carrier's own verdict: not_submitted,
	// pending, approved or rejected. Empty means the channel has no carrier
	// template registry — every channel except RCS today — and the check is
	// skipped entirely rather than defaulting to a refusal.
	CarrierTemplateStatus string
	Suppressed            bool
	BalanceMinor          int64
	CostMinor             int64
	RecipientValid        bool
}

// Check runs the gate. Order matters and is deliberate: compliance failures
// are reported before money, because telling a user "insufficient balance"
// when the real problem is an unapproved sender sends them to fix the wrong
// thing.
func Check(input GateInput) error {
	if input.TenantStatus == "suspended" {
		return ErrTenantSuspended
	}
	if !input.RecipientValid {
		return ErrInvalidRecipient
	}
	if input.SenderStatus != "approved" {
		return fmt.Errorf("%w (status %s)", ErrSenderNotApproved, input.SenderStatus)
	}
	if input.TemplateStatus != "" {
		if input.TemplateStatus != "approved" {
			return fmt.Errorf("%w (status %s)", ErrTemplateNotApproved, input.TemplateStatus)
		}
		if input.TemplateSender != "" && input.TemplateSender != input.SenderID {
			return ErrSenderTemplateMismatch
		}
	}
	// After our own approval, because a template neither side has approved
	// should say so in the order the customer would fix it: our review first,
	// then the carrier's, which cannot even begin until ours has passed.
	if input.CarrierTemplateStatus != "" && input.CarrierTemplateStatus != "approved" {
		return fmt.Errorf("%w (status %s)", ErrCarrierTemplateNotApproved,
			input.CarrierTemplateStatus)
	}
	// Suppression is checked before balance so an opted-out recipient is never
	// billed for, not even momentarily.
	if input.Suppressed {
		return ErrSuppressed
	}
	if input.BalanceMinor < input.CostMinor {
		return ErrInsufficientFunds
	}
	return nil
}

// GateFailureCode maps a gate error to a stable, machine-readable code for the
// API and the message log.
// IsRefusal reports whether err is the gate declining a send, rather than the
// send path itself failing.
//
// The difference matters to anyone reporting the outcome: a refusal is a normal
// result with a reason the caller can act on ("that sender is not approved"),
// while any other error means something broke and there is nothing useful to
// tell them. Treating the two alike is how a customer sending from a pending
// sender got "an unexpected error occurred" and a 500 — found on production the
// day the send API shipped.
func IsRefusal(err error) bool {
	for _, refusal := range []error{
		ErrTenantSuspended, ErrSenderNotApproved, ErrTemplateNotApproved,
		ErrSenderTemplateMismatch, ErrSuppressed, ErrInsufficientFunds,
		ErrInvalidRecipient, ErrCarrierTemplateNotApproved, ErrContentNotAllowed,
	} {
		if errors.Is(err, refusal) {
			return true
		}
	}
	return false
}

func GateFailureCode(err error) string {
	switch {
	case errors.Is(err, ErrContentNotAllowed):
		return "content_not_allowed"
	case errors.Is(err, ErrTenantSuspended):
		return "tenant_suspended"
	case errors.Is(err, ErrSenderNotApproved):
		return "sender_not_approved"
	case errors.Is(err, ErrCarrierTemplateNotApproved):
		return "carrier_template_not_approved"
	case errors.Is(err, ErrTemplateNotApproved):
		return "template_not_approved"
	case errors.Is(err, ErrSenderTemplateMismatch):
		return "sender_template_mismatch"
	case errors.Is(err, ErrSuppressed):
		return "recipient_suppressed"
	case errors.Is(err, ErrInsufficientFunds):
		return "insufficient_balance"
	case errors.Is(err, ErrInvalidRecipient):
		return "invalid_recipient"
	default:
		return "rejected"
	}
}
