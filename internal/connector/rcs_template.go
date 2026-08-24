package connector

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Carrier-side template registration.
//
// An RCS template has two approvals and they are not the same approval. Relay
// approves a template so its own compliance rules are satisfied; the carrier
// approves it separately, and a send quoting a template the carrier has never
// seen is refused at the gateway ("Template not found for provided
// templateId"). A template can therefore be perfectly approved in Relay and
// fail every single send.
//
// The two vendors do not agree on whether this is even an API:
//
//	Airtel  POST /rcs-content-manager/v1/rcs/template creates one; review takes
//	        up to 24 hours; the outcome arrives on the webhook and can also be
//	        polled. Category must match the AGENT's approved use case — a
//	        promotional template under a transactional agent is auto-rejected.
//
//	Vi      No API. Templates are created and approved in the Vi RBM portal by a
//	        Vi admin, and the brand is given a template code. Nothing here can
//	        create one or read its status.
//
// That asymmetry is why registration has a manual path as well as an automatic
// one, rather than being modelled as "call the carrier and wait".

// RCSTemplateSpec is one template offered to a carrier for approval.
//
// Only the TEXT shape is modelled. Media, rich card and carousel templates
// carry structure this does not describe, and half-supporting them would mean
// submitting a card the carrier stores as text.
type RCSTemplateSpec struct {
	// Name is the carrier's friendly name. Airtel caps it at 60 characters.
	Name string

	// UseCase must match the registered agent's own use case:
	// PROMOTIONAL, TRANSACTIONAL or OTP.
	UseCase string

	// Text is the body in RELAY's own form, with {{named}} tokens. Each carrier
	// renders it into its own dialect — Airtel wants positional {{1}}, Vi wants
	// bracketed [NAME] — because a placeholder dialect is a property of the
	// vendor, not of the template.
	Text string

	// Variables are the template's declared names IN ORDER. The order is what
	// makes a positional dialect possible at all, and it must be the same order
	// the send path fills values in, or {{1}} is the discount code on one side
	// and the customer's name on the other.
	Variables []string

	// TTL in seconds, zero for none. Airtel resolves a send-time TTL over this
	// one; it is the floor, not the rule.
	TTL int

	// SubmittedBy is the email of the person asking. Airtel requires a valid
	// address and records it in the template's event log, which is the only
	// audit trail of who submitted what.
	SubmittedBy string
}

// Template registration states, deliberately lowercase to match how Relay
// stores every other status rather than the carriers' upper case.
const (
	RCSTemplateNotSubmitted = "not_submitted"
	RCSTemplatePending      = "pending"
	RCSTemplateApproved     = "approved"
	RCSTemplateRejected     = "rejected"
)

// RCSTemplateRegistration is the carrier's side of a template's life.
type RCSTemplateRegistration struct {
	CarrierTemplateID string
	Status            string
	RejectionReason   string
}

// RCSTemplateRegistrar is a carrier that can register templates over an API.
//
// Vi implements it too, and refuses: a vendor that has no such API is a fact
// the product has to state, not an interface to leave unimplemented. Returning
// ErrTemplateRegistrationManual is what lets one endpoint tell a customer
// "create this in the Vi portal and paste the code back" instead of failing
// with something that reads like an outage.
type RCSTemplateRegistrar interface {
	Vendor() string
	RegisterTemplate(ctx context.Context, spec RCSTemplateSpec) (RCSTemplateRegistration, error)
	TemplateStatus(ctx context.Context, carrierTemplateID string) (RCSTemplateRegistration, error)
}

// ErrTemplateRegistrationManual means this carrier has no template API and the
// template must be created in their portal.
var ErrTemplateRegistrationManual = errors.New(
	"connector: this carrier has no template API; create the template in their portal and attach the code")

// Airtel's documented template limits. Checked before submitting rather than
// after, because their review takes up to 24 hours: a template rejected for
// something countable is a day lost to a mistake we could have named
// immediately.
const (
	airtelMaxTemplateText = 2500
	airtelMaxVariables    = 15
	airtelMaxTemplateName = 60
)

// RenderAirtelText turns Relay's {{named}} body into Airtel's positional form.
//
// Position comes from the template's declared variable order — the same order
// the send path fills values in. If these two ever disagree, every message goes
// out with its variables shuffled and nothing errors, which is why they read
// from the same list rather than from two.
//
// A token naming a variable the template does not declare is left exactly as it
// was. Rewriting it to some position would invent a slot the send path will
// never fill, and Airtel refuses a send with fewer values than the template
// declares — so the template would be approved and unusable.
func RenderAirtelText(text string, variables []string) string {
	position := make(map[string]int, len(variables))
	for index, name := range variables {
		position[name] = index + 1
	}

	// One pass, not a ReplaceAll per variable. Sequential replacement rewrites
	// its own output: with variables ["x", "1"], replacing {{x}} produces {{1}},
	// and the next replacement — for the variable literally named "1" — turns
	// that into {{2}}. Both slots then read {{2}}, every message goes out with
	// the wrong values, and nothing errors. A template body containing {{1}} is
	// all it takes, and the variable parser accepts one.
	return namedTokenPattern.ReplaceAllStringFunc(text, func(token string) string {
		name := strings.TrimSpace(token[2 : len(token)-2])
		index, declared := position[name]
		if !declared {
			// Left exactly as it was: rewriting an undeclared token would
			// invent a slot the send path never fills, and Airtel refuses a
			// send with fewer values than the template declares.
			return token
		}
		return "{{" + strconv.Itoa(index) + "}}"
	})
}

// namedTokenPattern matches Relay's own {{named}} form, including the already
// positional {{1}} — which is exactly why the rewrite has to be a single pass.
var namedTokenPattern = regexp.MustCompile(`\{\{\s*[^{}]+?\s*\}\}`)

// ValidateAirtelTemplate reports what Airtel would reject, in words the person
// who wrote the template can act on.
func ValidateAirtelTemplate(spec RCSTemplateSpec) error {
	switch {
	case strings.TrimSpace(spec.Name) == "":
		return errors.New("the template needs a name")
	case len(spec.Name) > airtelMaxTemplateName:
		return fmt.Errorf("the template name is %d characters; Airtel allows %d",
			len(spec.Name), airtelMaxTemplateName)
	case strings.TrimSpace(spec.Text) == "":
		return errors.New("the template body is empty")
	case len(spec.Text) > airtelMaxTemplateText:
		return fmt.Errorf("the template body is %d characters; Airtel allows %d",
			len(spec.Text), airtelMaxTemplateText)
	case len(spec.Variables) > airtelMaxVariables:
		return fmt.Errorf("the template has %d variables; Airtel allows %d",
			len(spec.Variables), airtelMaxVariables)
	case spec.UseCase == "":
		return errors.New("the template needs a use case matching the agent's own")
	}

	// Airtel requires variables to be separated by whitespace and allows at
	// most three in a row. Checked on the RENDERED text, because that is what
	// they will actually see.
	rendered := RenderAirtelText(spec.Text, spec.Variables)
	if adjacentPlaceholders(rendered) {
		return errors.New("two variables sit next to each other with nothing between them; " +
			"Airtel requires a space between variables")
	}
	if consecutivePlaceholders(rendered) > 3 {
		return errors.New("more than three variables in a row; Airtel allows three")
	}
	return nil
}

var placeholderPattern = regexp.MustCompile(`\{\{\s*\d+\s*\}\}`)

// adjacentPlaceholders finds two placeholders with nothing at all between them.
func adjacentPlaceholders(text string) bool {
	spans := placeholderPattern.FindAllStringIndex(text, -1)
	for i := 1; i < len(spans); i++ {
		if spans[i][0] == spans[i-1][1] {
			return true
		}
	}
	return false
}

// consecutivePlaceholders is the longest run of placeholders separated only by
// whitespace.
func consecutivePlaceholders(text string) int {
	spans := placeholderPattern.FindAllStringIndex(text, -1)
	longest, run := 0, 0
	for i, span := range spans {
		if i == 0 || strings.TrimSpace(text[spans[i-1][1]:span[0]]) != "" {
			run = 1
		} else {
			run++
		}
		if run > longest {
			longest = run
		}
	}
	return longest
}

// normaliseCarrierTemplateStatus maps a carrier's word to Relay's.
//
// Anything unrecognised becomes pending rather than approved. Guessing upward
// would let an unknown state unblock sending, and a send on a template the
// carrier has not approved is refused at the gateway after the money has moved.
func normaliseCarrierTemplateStatus(carrierStatus string) string {
	switch carrierStatus {
	case "APPROVED", "ACTIVE", "approved":
		return RCSTemplateApproved
	case "REJECTED", "rejected":
		return RCSTemplateRejected
	default:
		return RCSTemplatePending
	}
}
