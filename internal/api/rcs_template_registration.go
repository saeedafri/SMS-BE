package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// carrierRegistrationResponse renders the carrier's side of a template.
//
// Returned for RCS templates only. On every other channel the columns are at
// their defaults and showing them would put a field on screen that can never
// become true — WhatsApp has its own approval flow with Meta and Email has none.
func carrierRegistrationResponse(t store.Template) *gen.Template_CarrierRegistration {
	if t.Channel != "RCS" {
		return nil
	}
	var wrapper gen.Template_CarrierRegistration
	// The union has one variant, so the only way this can fail is a value that
	// will not marshal — which a struct of strings and times cannot be.
	_ = wrapper.FromCarrierTemplateRegistration(carrierRegistrationBody(t))
	return &wrapper
}

// airtelUseCase maps Relay's template category to the agent use case Airtel
// expects.
//
// Getting this wrong is not a cosmetic problem: "if your Agent was created and
// approved under a Transactional use case, any template submitted under this
// agent with Promotional content will fail validation and be automatically
// rejected". There is no safe default, which is why an uncategorised template
// is refused rather than guessed at.
func airtelUseCase(category *string) (string, bool) {
	if category == nil {
		return "", false
	}
	switch *category {
	case "MARKETING":
		return "PROMOTIONAL", true
	case "AUTHENTICATION":
		return "OTP", true
	case "UTILITY", "TRANSACTIONAL":
		return "TRANSACTIONAL", true
	default:
		return "", false
	}
}

// rcsTemplateText pulls the body out of an RCS template's stored content.
//
// RCS templates keep their message in rcs_content as the contract's own union,
// not in `body` — body is null for them. Only the text variant can be
// registered; a card carries structure the carrier template spec does not
// describe, and submitting one as text would have the carrier approve something
// that is not the template Relay holds.
func rcsTemplateText(t store.Template) (string, bool) {
	if len(t.RCSContent) == 0 {
		return "", false
	}
	var content struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(t.RCSContent, &content); err != nil {
		return "", false
	}
	if content.Kind != "text" || strings.TrimSpace(content.Text) == "" {
		return "", false
	}
	return content.Text, true
}

func (s *Server) RegisterTemplateWithCarrier(ctx context.Context,
	request gen.RegisterTemplateWithCarrierRequestObject) (gen.RegisterTemplateWithCarrierResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.RegisterTemplateWithCarrier401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if !canManageSettings(identity.Role) {
		return gen.RegisterTemplateWithCarrier403JSONResponse(
			errorBody(codeForbidden, "Your role doesn't include template management.")), nil
	}
	// Required, and checked by hand. The field is a non-pointer string, so a
	// body that omits it decodes to "" and reaches here looking like a request
	// that named nothing — which is exactly the guess this field retires.
	if request.Body == nil || !request.Body.Vendor.Valid() {
		return gen.RegisterTemplateWithCarrier422JSONResponse(errorBody(codeValidation,
			"Name the carrier this registration belongs to: vendor must be airtel or vi.")), nil
	}
	vendor := string(request.Body.Vendor)

	template, err := store.GetTemplate(ctx, s.DB, identity, request.Id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.RegisterTemplateWithCarrier404JSONResponse(
			errorBody(codeNotFound, "No such template.")), nil
	}
	if err != nil {
		return nil, err
	}
	if template.Channel != "RCS" {
		return gen.RegisterTemplateWithCarrier422JSONResponse(errorBody(codeValidation,
			"Only RCS templates are registered with a carrier.")), nil
	}
	// Our own review first. The carrier's review is a second opinion on content
	// we already stand behind, and spending a 24-hour Airtel review on something
	// our compliance team would reject wastes a day of the customer's time.
	if template.Status != "approved" {
		return gen.RegisterTemplateWithCarrier422JSONResponse(errorBody(codeValidation,
			"This template is not approved in Relay yet. The carrier's review comes after ours.")), nil
	}

	carrierAgentID, problem, err := s.registrationAgent(ctx, identity, template, vendor)
	if err != nil {
		return nil, err
	}
	if problem != "" {
		return gen.RegisterTemplateWithCarrier422JSONResponse(
			errorBody(codeValidation, problem)), nil
	}

	// A code from the carrier's portal. This is the ONLY route for Vi, which
	// has no template API at all.
	if request.Body != nil && request.Body.CarrierTemplateId != nil &&
		strings.TrimSpace(*request.Body.CarrierTemplateId) != "" {
		return s.attachCarrierTemplate(ctx, identity, template, vendor,
			strings.TrimSpace(*request.Body.CarrierTemplateId))
	}

	if template.CarrierTemplateID != nil {
		return gen.RegisterTemplateWithCarrier422JSONResponse(errorBody(codeValidation,
			"This template is already registered with a carrier ("+template.CarrierStatus+
				"). Attach a different code to replace it.")), nil
	}

	registrar, configured := s.Carriers.RCSTemplateRegistrarFor("RCS")
	if !configured || registrar == nil {
		return gen.RegisterTemplateWithCarrier503JSONResponse(errorBody(codeValidation,
			"This deployment has no RCS carrier configured.")), nil
	}
	// The named carrier, not whichever one is configured. Submitting to the
	// wrong one would come back approved under a template id the named carrier
	// has never seen — the "Template not found" this whole change removes.
	if registrar.Vendor() != vendor {
		return gen.RegisterTemplateWithCarrier503JSONResponse(errorBody(codeValidation,
			"This deployment has no "+vendor+" integration configured, so it cannot submit "+
				"a template to "+vendor+". Attach a template code from "+vendor+"'s portal instead.")), nil
	}

	text, isText := rcsTemplateText(template)
	if !isText {
		return gen.RegisterTemplateWithCarrier422JSONResponse(errorBody(codeValidation,
			"Only text RCS templates can be registered automatically. "+
				"Create a card or carousel template in the carrier's portal and attach the code.")), nil
	}
	useCase, categorised := airtelUseCase(template.Category)
	if !categorised {
		return gen.RegisterTemplateWithCarrier422JSONResponse(errorBody(codeValidation,
			"Give this template a category first. The carrier requires a use case, and it must "+
				"match the one your RCS agent was approved under.")), nil
	}

	registration, err := registrar.RegisterTemplate(ctx, carrierAgentID, connector.RCSTemplateSpec{
		Name:        template.Name,
		UseCase:     useCase,
		Text:        text,
		Variables:   template.Variables,
		SubmittedBy: identity.Email,
	})
	switch {
	case errors.Is(err, connector.ErrTemplateRegistrationManual):
		return gen.RegisterTemplateWithCarrier409JSONResponse(errorBody(codeConflict,
			"This carrier does not accept templates over an API. Create it in their portal, "+
				"then send the template code back here to attach it.")), nil
	case errors.Is(err, connector.ErrRCSNotConfigured):
		return gen.RegisterTemplateWithCarrier503JSONResponse(errorBody(codeValidation,
			"This deployment has no RCS carrier configured.")), nil
	case err != nil:
		// A validation failure is the customer's to fix and its message is
		// ours, written from the vendor's documented limits. Anything else is
		// the carrier's and is not repeated: their errors quote the agent id.
		if isCarrierTemplateValidation(err) {
			return gen.RegisterTemplateWithCarrier422JSONResponse(
				errorBody(codeValidation, capitalise(err.Error())+".")), nil
		}
		s.Logger.Error("carrier refused a template", "template", template.ID, "error", err)
		return gen.RegisterTemplateWithCarrier502JSONResponse(errorBody(codeValidation,
			"The carrier could not be reached or refused the template. Try again shortly.")), nil
	}

	saved, err := store.SaveCarrierTemplateRegistration(ctx, s.DB, identity, template.ID,
		registrar.Vendor(), registration.CarrierTemplateID, registration.Status,
		registration.RejectionReason)
	if errors.Is(err, store.ErrConflict) {
		// The carrier now holds a template we could not record, same as the
		// error path below — logged for the same reason: the id is recoverable
		// from their portal, and losing it quietly means a template approved
		// somewhere Relay cannot see.
		s.Logger.Error("carrier accepted a template whose id is already attached elsewhere",
			"template", template.ID, "carrier_template", registration.CarrierTemplateID)
		return gen.RegisterTemplateWithCarrier422JSONResponse(errorBody(codeConflict,
			"That carrier template code is already attached to another template.")), nil
	}
	if err != nil {
		// The carrier now holds a template we failed to record. Logged loudly
		// because the id is recoverable — it is in the carrier's portal — and
		// silently losing it means the customer's template is approved
		// somewhere Relay cannot see.
		s.Logger.Error("carrier accepted a template we failed to record",
			"template", template.ID, "carrier_template", registration.CarrierTemplateID,
			"error", err)
		return nil, err
	}
	return gen.RegisterTemplateWithCarrier200JSONResponse(
		carrierRegistrationBody(saved)), nil
}

// attachCarrierTemplate records a code the customer obtained in the carrier's
// portal.
//
// It is marked approved, not pending, and that is a deliberate assertion rather
// than a guess: Vi publishes no way to read a template's approval state, so the
// customer telling us the carrier approved it is the only source of truth that
// exists. If they are wrong the send is refused by the carrier with its own
// reason, which is recoverable — whereas leaving it pending forever would block
// every Vi tenant from sending at all.
func (s *Server) attachCarrierTemplate(ctx context.Context, identity store.Identity,
	template store.Template, vendor, carrierTemplateID string) (gen.RegisterTemplateWithCarrierResponseObject, error) {

	// The vendor is the caller's statement of which portal the code came from,
	// and it replaces a guess from the deployment's own carrier that labelled a
	// code pasted from the OTHER carrier's portal with ours.
	//
	// It is never null here, and that matters more than it looks. The column is
	// half of templates_carrier_identity, UNIQUE (carrier_vendor,
	// carrier_template_id), and Postgres treats NULLs as distinct — so a null
	// vendor would let two tenants hold the same code and each receive the
	// other's approval webhooks. The guess was wrong but it was holding that
	// index shut; the required field now does the same job honestly.
	//
	// No carrier needs to be configured to record one. The code already exists
	// at the carrier; nothing is sent until a send, and the send checks that
	// this vendor is the carrier the message actually goes through.
	saved, err := store.SaveCarrierTemplateRegistration(ctx, s.DB, identity, template.ID,
		vendor, carrierTemplateID, connector.RCSTemplateApproved, "")
	if errors.Is(err, store.ErrConflict) {
		return gen.RegisterTemplateWithCarrier422JSONResponse(errorBody(codeConflict,
			"That carrier template code is already attached to another template.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.RegisterTemplateWithCarrier200JSONResponse(carrierRegistrationBody(saved)), nil
}

// registrationAgent is the carrier's own id for the agent a template must be
// registered under, or the customer-facing reason there is none.
//
// Derived, never chosen. A registration is not free to name any of the tenant's
// agents: a send resolves its agent from the sender, and the gate refuses a
// template that belongs to a different sender. So the only agent a registration
// can usefully name is the agent of the template's OWN sender — any other
// produces a registration no send can ever use, and it arrives hours later as
// "Template not found". There is nothing to pick between, which is why the
// request carries no agent at all.
//
// The launch is read under the same conditions a send uses (store.CarrierAgentID),
// so a template cannot be registered under an agent that could not send it.
func (s *Server) registrationAgent(ctx context.Context, identity store.Identity,
	template store.Template, vendor string) (string, string, error) {

	sender, err := store.GetSenderID(ctx, s.DB, identity, template.SenderID)
	if err != nil {
		return "", "", err
	}
	if sender.RcsAgentID == nil {
		return "", "This template's sender has no RCS agent. A carrier registration " +
			"belongs to one agent, and the agent comes from the sender, so there is " +
			"nothing to register it under.", nil
	}
	carrier := connector.RCSIntegrations[vendor]
	carrierAgentID, err := store.CarrierAgentID(ctx, s.DB, identity, *sender.RcsAgentID, carrier)
	if errors.Is(err, store.ErrNotFound) {
		return "", "The agent behind this template's sender has no approved launch on " +
			carrier + ", so a template registered there could never send. Launch the " +
			"agent on " + carrier + " first.", nil
	}
	if err != nil {
		return "", "", err
	}
	return carrierAgentID, "", nil
}

func carrierRegistrationBody(t store.Template) gen.CarrierTemplateRegistration {
	body := gen.CarrierTemplateRegistration{
		CarrierTemplateId: t.CarrierTemplateID,
		RejectionReason:   t.CarrierRejectionReason,
		SubmittedAt:       t.CarrierSubmittedAt,
		UpdatedAt:         t.CarrierUpdatedAt,
		Status:            gen.CarrierTemplateRegistrationStatus(t.CarrierStatus),
	}
	if t.CarrierVendor != nil {
		vendor := gen.RcsVendor(*t.CarrierVendor)
		body.Vendor = &vendor
	}
	return body
}

// isCarrierTemplateValidation separates a rule the customer broke from a
// carrier or transport failure. ValidateAirtelTemplate's errors are plain
// errors.New values with no wrapper to match on, so they are told apart by
// NOT being one of the connector's sentinels — the set of those is small and
// closed, which makes this the reliable direction to test.
func isCarrierTemplateValidation(err error) bool {
	if errors.Is(err, connector.ErrRCSNotConfigured) ||
		errors.Is(err, connector.ErrRCSThrottled) ||
		errors.Is(err, connector.ErrTemplateRegistrationManual) {
		return false
	}
	// Anything the carrier itself said is prefixed by the connector.
	return !strings.HasPrefix(err.Error(), "airtel rcs:") &&
		!strings.HasPrefix(err.Error(), "vi rcs:")
}

func capitalise(text string) string {
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}
