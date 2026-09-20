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
	// ErrRegisteredTemplateRequired and ErrTemplateBodyMismatch are the two
	// halves of India's template binding. They are separate errors because they
	// are separate fixes: one means "attach your DLT template", the other means
	// "the text you sent is not what that template says".
	ErrRegisteredTemplateRequired = errors.New(
		"messaging: this country requires a registered template on every send")
	ErrTemplateBodyMismatch = errors.New(
		"messaging: body is not a legal instantiation of the registered template")

	// ErrRCSAgentNotResolved is an RCS send whose sender has no usable brand
	// identity on the carrier it would route over: no agent attached, a launch
	// still pending or rejected, or an agent suspended since the sender was
	// registered.
	//
	// It is refused HERE, before any money moves, rather than left to the
	// connector — which would also refuse it, but as a carrier rejection. The
	// carrier has said nothing; we have. Reporting our own refusal in the
	// carrier's name is the mistake ErrCarrierTemplateNotApproved exists to
	// avoid, one field over: it sends the customer to argue with Airtel about
	// an agent Airtel has never been asked to review.
	// ErrOutsidePromotionalWindow is promotional traffic outside the hours the
	// regulator allows it. Operators drop it rather than queue it, so refusing
	// here is the only way the customer hears why.
	ErrOutsidePromotionalWindow = errors.New(
		"messaging: promotional messages may only be sent during the permitted hours")

	ErrDNDBlocked = errors.New(
		"messaging: this number does not accept promotional messages")

	ErrDNDCheckUnavailable = errors.New(
		"messaging: this number could not be checked against the do-not-disturb register")

	ErrRCSAgentNotResolved = errors.New(
		"messaging: this sender has no RCS agent identity on that carrier")

	// ErrVariableUnresolved is a message that still carries a {{token}} nobody
	// filled, caught on its way out.
	//
	// The fan-out already skips a contact it cannot personalise, so in a
	// working system this never fires. That is what it is for. It reads the
	// RENDERED strings rather than trusting the list of missing values the fill
	// reported, because a bug in the fill is the thing it exists to catch — and
	// a fill broken enough to leave a token behind is broken enough to report
	// nothing missing.
	//
	// It matters most on RCS. Today an RCS send is not refused for this: it
	// goes to the sandbox, is charged, is logged as sent and is seen by nobody,
	// which is correct for a deployment with no carrier. The day credentials
	// are configured, every one of those guards flips at once, and an
	// unsubstituted {{first_name}} reaches a handset with everybody watching
	// RCS work and nobody watching for it.
	ErrVariableUnresolved = errors.New(
		"messaging: the message still contains a variable nothing filled")
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

	// RegisteredTemplateRequired is the destination regime's rule. When it is
	// set, a send must name an approved template AND its body must be an
	// instantiation of that template's registered text.
	RegisteredTemplateRequired bool

	// TemplateBody is the registered text, empty when no template was named.
	TemplateBody string
	// Body is what the caller actually asked us to send.
	Body string

	// RCSAgentRequired marks a channel that reaches a handset under a brand
	// identity — RCS, and only RCS today. RCSAgentResolved says the sender
	// actually has one on the carrier this message routes over.
	//
	// Two fields rather than one string because the two facts come from
	// different places and mean different things: the first is a property of
	// the channel, the second the answer to a per-tenant lookup that can change
	// between two messages when a carrier suspends an agent.
	RCSAgentRequired bool
	RCSAgentResolved bool

	// DNDBlocked and DNDCheckUnavailable are the do-not-disturb register's
	// answer for a promotional message to a country that keeps one. Blocked
	// means the register says no; unavailable means no register is configured
	// or the lookup failed, which refuses too rather than risking a breach.
	DNDBlocked          bool
	DNDCheckUnavailable bool

	// OutsidePromotionalWindow is a promotional message sent when its
	// destination forbids promotional traffic.
	OutsidePromotionalWindow bool

	// UnresolvedVariables says a {{token}} survived into the rendered message
	// — any of its strings, not only the body. Set by the caller, which is the
	// only layer that can see every string a message is made of: the text, an
	// RCS card's title and description, and each suggestion's own label and
	// link.
	UnresolvedVariables bool
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
	// Template binding, where the regime demands it. Checked after the
	// template's own approval — an unapproved template is a different and more
	// basic problem — and before anything to do with money.
	if input.RegisteredTemplateRequired {
		if input.TemplateStatus == "" {
			return ErrRegisteredTemplateRequired
		}
		// Matched only when there is a registered text AND a submitted body to
		// match it against. Two cases where there is not, and refusing either
		// would be wrong:
		//
		//   - a template whose channel keeps its content somewhere we cannot
		//     read gives us nothing to compare against;
		//   - an RCS campaign sends no body at all. The carrier holds the
		//     approved template and renders it from the variables we pass, so
		//     what reaches the handset IS the registered template by
		//     construction. Comparing an empty body against the template text
		//     refused every RCS campaign, which is how this was caught.
		//
		// The template requirement itself still stands in both cases.
		if input.TemplateBody != "" && input.Body != "" &&
			!MatchesTemplate(input.TemplateBody, input.Body) {
			return ErrTemplateBodyMismatch
		}
	}
	// Nothing leaves with a hole in it. Placed with the other content rules and
	// before every carrier check, because this is the one refusal that is about
	// the message itself rather than about who may send it — and the only one
	// whose failure mode is a delivered message rather than a refused one.
	if input.UnresolvedVariables {
		return ErrVariableUnresolved
	}
	// After our own approval, because a template neither side has approved
	// should say so in the order the customer would fix it: our review first,
	// then the carrier's, which cannot even begin until ours has passed.
	if input.CarrierTemplateStatus != "" && input.CarrierTemplateStatus != "approved" {
		return fmt.Errorf("%w (status %s)", ErrCarrierTemplateNotApproved,
			input.CarrierTemplateStatus)
	}
	// The brand the handset will draw. Last of the carrier-side checks and
	// still before money, because a message with no identity to send under was
	// never going to leave — and the alternative, falling back to a shared
	// agent, delivers it perfectly under someone else's name.
	if input.RCSAgentRequired && !input.RCSAgentResolved {
		return ErrRCSAgentNotResolved
	}
	if input.OutsidePromotionalWindow {
		return ErrOutsidePromotionalWindow
	}
	// Suppression is checked before balance so an opted-out recipient is never
	// billed for, not even momentarily.
	if input.Suppressed {
		return ErrSuppressed
	}
	// The register, after the person's own opt-out and before any money: a
	// refused promotional message must never hold funds, even briefly.
	if input.DNDBlocked {
		return ErrDNDBlocked
	}
	if input.DNDCheckUnavailable {
		return ErrDNDCheckUnavailable
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
	for _, refusal := range refusals {
		if errors.Is(err, refusal) {
			return true
		}
	}
	return false
}

// refusals is every error the gate refuses a send with. One list: IsRefusal
// reads it, and a test checks that each maps to a code the contract declares,
// so a new refusal cannot reach a customer as "Refused" with no reason.
var refusals = []error{
	ErrTenantSuspended, ErrSenderNotApproved, ErrTemplateNotApproved,
	ErrSenderTemplateMismatch, ErrSuppressed, ErrDNDBlocked, ErrDNDCheckUnavailable,
	ErrInsufficientFunds,
	ErrInvalidRecipient, ErrCarrierTemplateNotApproved, ErrContentNotAllowed,
	ErrRegisteredTemplateRequired, ErrTemplateBodyMismatch,
	ErrRCSAgentNotResolved, ErrOutsidePromotionalWindow,
	ErrVariableUnresolved,
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
	case errors.Is(err, ErrRegisteredTemplateRequired):
		return "registered_template_required"
	case errors.Is(err, ErrRCSAgentNotResolved):
		return "rcs_agent_not_resolved"
	case errors.Is(err, ErrTemplateBodyMismatch):
		return "template_body_mismatch"
	case errors.Is(err, ErrVariableUnresolved):
		return "variable_unresolved"
	case errors.Is(err, ErrOutsidePromotionalWindow):
		return "outside_promotional_window"
	case errors.Is(err, ErrSuppressed):
		return "recipient_suppressed"
	case errors.Is(err, ErrDNDBlocked):
		return "dnd_blocked"
	case errors.Is(err, ErrDNDCheckUnavailable):
		return "dnd_check_unavailable"
	case errors.Is(err, ErrInsufficientFunds):
		return "insufficient_balance"
	case errors.Is(err, ErrInvalidRecipient):
		return "invalid_recipient"
	default:
		return "rejected"
	}
}
