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
	ErrTenantSuspended        = errors.New("messaging: tenant is suspended")
	ErrSenderNotApproved      = errors.New("messaging: sender is not approved")
	ErrTemplateNotApproved    = errors.New("messaging: template is not approved")
	ErrSenderTemplateMismatch = errors.New("messaging: template does not belong to that sender")
	ErrSuppressed             = errors.New("messaging: recipient is suppressed")
	ErrInsufficientFunds      = errors.New("messaging: insufficient balance")
	ErrInvalidRecipient       = errors.New("messaging: recipient is not a valid number")
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
	Suppressed     bool
	BalanceMinor   int64
	CostMinor      int64
	RecipientValid bool
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
func GateFailureCode(err error) string {
	switch {
	case errors.Is(err, ErrTenantSuspended):
		return "tenant_suspended"
	case errors.Is(err, ErrSenderNotApproved):
		return "sender_not_approved"
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
