package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/domain/rcs"
	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/store"
)

// reachableCarriers is the set of carriers this deployment can actually put an
// agent to. Derived from whether a connector is configured, not from a list of
// networks that exist.
func (s *Server) reachableCarriers() map[string]bool {
	if s.RCSCarrier == nil {
		return nil
	}
	// The vendor this deployment actually holds credentials for. Derived from
	// the same handle that decides whether an RCS send can leave at all, so
	// "we can launch an agent there" and "we can send there" cannot drift.
	return map[string]bool{strings.ToUpper(s.RCSCarrier.Vendor()): true}
}

// rcsAgentResponse renders an agent, filling in a launch row for every carrier
// that operates in its country.
//
// A carrier we cannot reach is SHOWN, not hidden, sitting permanently at
// not_submitted. A customer whose reach looks short should be able to see which
// network is missing rather than wondering — and the alternative, omitting it,
// makes an unsupported network indistinguishable from one that does not exist.
//
// The carrier set is derived from routes rather than a table of countries, so a
// corridor configured tomorrow appears here with no second list to keep in step.
func (s *Server) rcsAgentResponse(ctx context.Context, agent store.RcsAgent) gen.RcsAgent {
	out := gen.RcsAgent{
		Id:                agent.ID,
		DisplayName:       agent.DisplayName,
		Description:       agent.Description,
		PrimaryColor:      agent.PrimaryColor,
		PhoneNumber:       agent.PhoneNumber,
		Email:             agent.Email,
		Website:           agent.Website,
		PrivacyPolicyUrl:  agent.PrivacyPolicyURL,
		TermsOfServiceUrl: agent.TermsOfServiceURL,
		UseCase:           gen.RcsAgentUseCase(agent.UseCase),
		Country:           gen.CountryCode(agent.Country),
		RegistrationId:    agent.RegistrationID,
		Status:            gen.RcsAgentStatus(agent.Status),
		RejectionReason:   agent.RejectionReason,
		CreatedAt:         agent.CreatedAt,
		UpdatedAt:         &agent.UpdatedAt,
	}

	var verification gen.RcsAgent_Verification
	_ = verification.FromRcsAgentVerification(gen.RcsAgentVerification{
		Status:          gen.RcsAgentVerificationStatus(agent.Verification.Status),
		ContactName:     agent.Verification.ContactName,
		ContactEmail:    agent.Verification.ContactEmail,
		ContactPhone:    agent.Verification.ContactPhone,
		DocumentAssetId: agent.Verification.DocumentAssetID,
		RejectionReason: agent.Verification.RejectionReason,
		SubmittedAt:     agent.Verification.SubmittedAt,
		ReviewedAt:      agent.Verification.ReviewedAt,
	})
	out.Verification = &verification

	recorded := map[string]store.RcsCarrierLaunch{}
	for _, launch := range agent.Launches {
		recorded[launch.Carrier] = launch
	}
	carriers, err := store.CarriersForCountry(ctx, s.operatorPool(), agent.Country)
	if err != nil {
		// A launch list we could not read is reported as the rows we do hold
		// rather than as an error: the agent itself is still worth rendering,
		// and an empty list would read as "no carrier anywhere" — a stronger
		// claim than "we could not look".
		carriers = nil
		for carrier := range recorded {
			carriers = append(carriers, carrier)
		}
	}
	for carrier := range recorded {
		if !slices.Contains(carriers, carrier) {
			carriers = append(carriers, carrier)
		}
	}
	slices.Sort(carriers)

	out.CarrierLaunches = make([]gen.RcsCarrierLaunch, 0, len(carriers))
	for _, carrier := range carriers {
		launch, ok := recorded[carrier]
		if !ok {
			launch = store.RcsCarrierLaunch{Carrier: carrier, Status: "not_submitted"}
		}
		row := gen.RcsCarrierLaunch{
			Carrier:         gen.CarrierId(launch.Carrier),
			Status:          gen.RcsCarrierLaunchStatus(launch.Status),
			CarrierAgentId:  launch.CarrierAgentID,
			RejectionReason: launch.RejectionReason,
			SubmittedAt:     launch.SubmittedAt,
		}
		// Null, not the zero time. A synthesised not_submitted row has never
		// been updated, and 0001-01-01T00:00:00Z is a value that looks like
		// data — the exact failure we warned the frontend about one required
		// field earlier, committed here in our own hand on the same day.
		if !launch.UpdatedAt.IsZero() {
			updatedAt := launch.UpdatedAt
			row.UpdatedAt = &updatedAt
		}
		out.CarrierLaunches = append(out.CarrierLaunches, row)
	}
	return out
}

func (s *Server) ListRcsAgents(ctx context.Context, request gen.ListRcsAgentsRequestObject) (
	gen.ListRcsAgentsResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	page, pageOK := pageNumber(request.Params.Page)
	if !pageOK {
		return gen.ListRcsAgents422JSONResponse(
			errorBody(codeValidation, pageTooLow)), nil
	}
	limit, limitOK := pageSize(request.Params.Limit)
	if !limitOK {
		return gen.ListRcsAgents422JSONResponse(
			errorBody(codeValidation, limitOutOfRange)), nil
	}
	filter := store.RcsAgentFilter{
		Status:  optionalEnum(request.Params.Status),
		Country: optionalEnum(request.Params.Country),
		Search:  searchTerm(request.Params.Q),
		Page:    page,
		Limit:   limit,
	}
	agents, total, err := store.ListRcsAgents(ctx, s.DB, identity, filter)
	if err != nil {
		return nil, err
	}
	out := make([]gen.RcsAgent, 0, len(agents))
	for _, agent := range agents {
		out = append(out, s.rcsAgentResponse(ctx, agent))
	}
	return gen.ListRcsAgents200JSONResponse(gen.RcsAgentPage{Agents: out, Total: total}), nil
}

// CreateRcsAgent writes a draft.
//
// Refused when the tenant holds no approved entity for that country: an agent
// is a brand claim, and the legal identity behind it is the one the compliance
// spine already approved. Letting a draft exist without it would invite a
// customer to fill in a whole brand profile and be told at verification that
// they were never eligible.
func (s *Server) CreateRcsAgent(ctx context.Context, request gen.CreateRcsAgentRequestObject) (
	gen.CreateRcsAgentResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	name := strings.TrimSpace(request.Body.DisplayName)
	if name == "" {
		return gen.CreateRcsAgent422JSONResponse(errorBody(codeValidation,
			"An agent needs a display name.")), nil
	}
	if err := rcs.CheckDisplayName(name); err != nil {
		return gen.CreateRcsAgent422JSONResponse(errorBody(codeValidation,
			capitalise(err.Error())+".")), nil
	}
	if !request.Body.UseCase.Valid() {
		return gen.CreateRcsAgent422JSONResponse(
			unknownUseCase(request.Body.UseCase)), nil
	}
	country := string(request.Body.Country)
	// Before the entity check. Telling someone to get a business entity approved
	// in a country where no agent can ever launch sends them through days of
	// compliance review for nothing, and they learn why only at the end.
	if problem, err := s.rcsUnavailableIn(ctx, country); err != nil || problem != "" {
		if err != nil {
			return nil, err
		}
		return gen.CreateRcsAgent422JSONResponse(errorBody(codeValidation, problem)), nil
	}
	approved, err := store.HasApprovedRegistration(ctx, s.DB, identity, country)
	if err != nil {
		return nil, err
	}
	if !approved {
		return gen.CreateRcsAgent409JSONResponse(errorBody(codeConflict,
			"This account has no approved business entity for "+country+" yet. "+
				"An RCS agent carries your registered brand identity, so that has "+
				"to be approved first.")), nil
	}

	agent, err := store.CreateRcsAgent(ctx, s.DB, identity, store.RcsAgent{
		DisplayName:    name,
		Description:    request.Body.Description,
		UseCase:        string(request.Body.UseCase),
		Country:        country,
		RegistrationID: request.Body.RegistrationId,
	})
	if err != nil {
		return nil, err
	}
	return gen.CreateRcsAgent201JSONResponse(s.rcsAgentResponse(ctx, agent)), nil
}

func (s *Server) GetRcsAgent(ctx context.Context, request gen.GetRcsAgentRequestObject) (
	gen.GetRcsAgentResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	agent, err := store.GetRcsAgent(ctx, s.DB, identity, request.Id)
	// Another tenant's agent is a 404 and not a 403. A refusal would confirm
	// the id exists, which turns the id space into an oracle for who the
	// customers are.
	if errors.Is(err, store.ErrNotFound) {
		return gen.GetRcsAgent404JSONResponse(errorBody(codeNotFound, "No such agent.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetRcsAgent200JSONResponse(s.rcsAgentResponse(ctx, agent)), nil
}

// UpdateRcsAgent edits a draft or a rejected agent.
//
// Refused in any other state: an agent under carrier review must not change
// underneath the carrier reviewing it.
func (s *Server) UpdateRcsAgent(ctx context.Context, request gen.UpdateRcsAgentRequestObject) (
	gen.UpdateRcsAgentResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	agent, err := store.GetRcsAgent(ctx, s.DB, identity, request.Id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.UpdateRcsAgent404JSONResponse(errorBody(codeNotFound, "No such agent.")), nil
	}
	if err != nil {
		return nil, err
	}
	if agent.Status != "draft" && agent.Status != "verification_rejected" {
		return gen.UpdateRcsAgent409JSONResponse(errorBody(codeConflict,
			"This agent is "+agent.Status+" and cannot be edited. An agent under "+
				"review must not change underneath the reviewer.")), nil
	}

	body := request.Body
	// A null here is refused, not ignored. Neither can be cleared — an agent
	// with no name shows nothing on a handset, and carriers review an agent by
	// its use case — and silently dropping the null reports success for a change
	// that did not happen: the caller believes the field is gone and the record
	// still holds it. Merge Patch's "null clears" still applies to every field
	// that CAN be cleared, below.
	for _, uncleared := range []struct {
		field    string
		sentNull bool
	}{
		{"displayName", body.DisplayName == nil},
		{"useCase", body.UseCase == nil},
	} {
		if field := uncleared.field; uncleared.sentNull && bodyMentions(ctx, field) {
			return gen.UpdateRcsAgent422JSONResponse(errorBody(codeValidation,
				field+" cannot be cleared. Send a new value, or leave the key out to keep "+
					"the current one.")), nil
		}
	}
	// The two carrier rules that refuse an agent at SUBMISSION, days later, in
	// a queue the customer cannot see. Checked on the edit rather than only at
	// submit so the refusal arrives while the field is still on the screen.
	if body.DisplayName != nil {
		if err := rcs.CheckDisplayName(strings.TrimSpace(*body.DisplayName)); err != nil {
			return gen.UpdateRcsAgent422JSONResponse(errorBody(codeValidation,
				capitalise(err.Error())+".")), nil
		}
	}
	if body.PrimaryColor != nil {
		if err := rcs.CheckPrimaryColor(*body.PrimaryColor); err != nil {
			return gen.UpdateRcsAgent422JSONResponse(errorBody(codeValidation,
				capitalise(err.Error())+".")), nil
		}
	}

	update := store.RcsAgentUpdate{
		// JSON Merge Patch: an omitted key leaves the stored value alone, an
		// explicit null clears it. Both arrive here as a nil pointer, so the
		// difference comes from the middleware's record of which keys the
		// request actually carried — see clearedFields.
		//
		// displayName and useCase are absent from this map on purpose: a null
		// on either was refused above.
		Cleared: clearedFields(ctx, map[string]bool{
			"description":       body.Description == nil,
			"logoAssetId":       body.LogoAssetId == nil,
			"heroImageAssetId":  body.HeroImageAssetId == nil,
			"primaryColor":      body.PrimaryColor == nil,
			"phoneNumber":       body.PhoneNumber == nil,
			"email":             body.Email == nil,
			"website":           body.Website == nil,
			"privacyPolicyUrl":  body.PrivacyPolicyUrl == nil,
			"termsOfServiceUrl": body.TermsOfServiceUrl == nil,
			"registrationId":    body.RegistrationId == nil,
		}),
		DisplayName:       body.DisplayName,
		Description:       body.Description,
		LogoAssetID:       body.LogoAssetId,
		HeroAssetID:       body.HeroImageAssetId,
		PrimaryColor:      body.PrimaryColor,
		PhoneNumber:       body.PhoneNumber,
		Email:             body.Email,
		Website:           body.Website,
		PrivacyPolicyURL:  body.PrivacyPolicyUrl,
		TermsOfServiceURL: body.TermsOfServiceUrl,
		RegistrationID:    body.RegistrationId,
	}
	if body.UseCase != nil {
		if !body.UseCase.Valid() {
			return gen.UpdateRcsAgent422JSONResponse(
				unknownUseCase(*body.UseCase)), nil
		}
		useCase := string(*body.UseCase)
		update.UseCase = &useCase
	}
	updated, err := store.UpdateRcsAgent(ctx, s.DB, identity, request.Id, update)
	if errors.Is(err, store.ErrNotFound) {
		return gen.UpdateRcsAgent404JSONResponse(errorBody(codeNotFound, "No such agent.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.UpdateRcsAgent200JSONResponse(s.rcsAgentResponse(ctx, updated)), nil
}

// SubmitRcsAgentVerification puts a brand claim to a reviewer.
func (s *Server) SubmitRcsAgentVerification(ctx context.Context,
	request gen.SubmitRcsAgentVerificationRequestObject) (
	gen.SubmitRcsAgentVerificationResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	body := request.Body
	// The document has to be this tenant's. Without the check, an id from
	// anywhere would attach another customer's paperwork to this brand claim.
	if _, err := store.GetMediaAsset(ctx, s.DB, identity, body.DocumentAssetId); err != nil {
		return gen.SubmitRcsAgentVerification422JSONResponse(errorBody(codeValidation,
			"That verification document does not exist on this account.")), nil
	}

	// The carrier's own brand rules, checked at the moment they would be
	// applied. Airtel refuses the agent here — up to 24 hours later, with its
	// own wording, in a queue the customer cannot see — so refusing now is the
	// difference between a corrected field and a lost day.
	//
	// The edit path checks these too. This is not a duplicate: an agent can
	// reach submission carrying a colour set before the rule existed, and the
	// last gate before a carrier sees it is the one that must hold.
	if problem := s.brandProblem(ctx, identity, request.Id); problem != "" {
		return gen.SubmitRcsAgentVerification422JSONResponse(
			errorBody(codeValidation, problem)), nil
	}

	agent, err := store.SubmitRcsAgentVerification(ctx, s.DB, identity, request.Id,
		strings.TrimSpace(body.ContactName), strings.TrimSpace(body.ContactEmail),
		strings.TrimSpace(body.ContactPhone), body.DocumentAssetId)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return gen.SubmitRcsAgentVerification404JSONResponse(
			errorBody(codeNotFound, "No such agent.")), nil
	case errors.Is(err, store.ErrIllegalTransition):
		return gen.SubmitRcsAgentVerification409JSONResponse(errorBody(codeConflict,
			"This agent has already been submitted for verification.")), nil
	case err != nil:
		return nil, err
	}
	return gen.SubmitRcsAgentVerification200JSONResponse(s.rcsAgentResponse(ctx, agent)), nil
}

// brandProblem re-reads the agent and applies the carrier's brand rules,
// returning the customer-facing reason it would be refused.
//
// Empty when the agent cannot be read: the transition below answers 404 for
// that, and guessing here would turn a missing agent into a validation error
// about a name nobody typed.
// rcsUnavailableIn is the refusal for creating an agent in a country none of
// whose carriers we can reach, naming where RCS is available instead. Empty
// means the country is fine.
//
// "Integrated" means we hold an adapter, not that this deployment has signed
// credentials for it — see connector.RCSIntegrations. Gating on credentials
// would refuse every country on a deployment still waiting for its contract,
// including India, and brand verification is days of work a customer should be
// able to start before then.
//
// Derived from routes and the adapter list rather than written down as "IN",
// so the day an adapter lands elsewhere that country opens with no change here.
func (s *Server) rcsUnavailableIn(ctx context.Context, country string) (string, error) {
	integrated := make([]string, 0, len(connector.RCSIntegrations))
	for _, carrier := range connector.RCSIntegrations {
		integrated = append(integrated, carrier)
	}
	available, err := store.RCSCountries(ctx, s.operatorPool(), integrated)
	if err != nil {
		return "", err
	}
	if slices.Contains(available, country) {
		return "", nil
	}
	if len(available) == 0 {
		return "RCS is not available in any country yet. A country opens when one " +
			"of its carriers has an RCS integration.", nil
	}
	return "RCS is not available in " + country + " yet: none of its carriers has " +
		"an RCS integration, so an agent created there could never launch. RCS " +
		"agents can be created in " + strings.Join(available, ", ") + ".", nil
}

// unknownUseCase is the refusal for a use case no carrier recognises.
//
// It exists because the alternative is a 500. The use_case check constraint is
// the floor under this column, and a value that reaches it is refused by
// Postgres as an integrity error — which surfaces to a customer as "an
// unexpected error occurred", naming neither the field nor the accepted set.
// MULTI_USE was a legal value until migration 00047 dropped it, so the callers
// most likely to send one are the ones who integrated against the old contract.
//
// The accepted values are the GENERATED constants rather than a literal list:
// dropping a member from the contract then deletes an identifier this function
// names, and the build fails instead of the message quietly going stale.
func unknownUseCase(value gen.RcsAgentUseCase) gen.Error {
	return errorBody(codeValidation, fmt.Sprintf(
		"%q is not a use case any carrier recognises. Accepted values are %s, %s and %s.",
		string(value), gen.RcsAgentUseCaseOTP,
		gen.RcsAgentUseCaseTRANSACTIONAL, gen.RcsAgentUseCasePROMOTIONAL))
}

func (s *Server) brandProblem(ctx context.Context, identity store.Identity,
	agentID uuid.UUID) string {

	agent, err := store.GetRcsAgent(ctx, s.DB, identity, agentID)
	if err != nil {
		return ""
	}
	if err := rcs.CheckDisplayName(agent.DisplayName); err != nil {
		return capitalise(err.Error()) + "."
	}
	if agent.PrimaryColor != nil {
		if err := rcs.CheckPrimaryColor(*agent.PrimaryColor); err != nil {
			return capitalise(err.Error()) + "."
		}
	}
	return ""
}

// LaunchRcsAgentOnCarrier puts a verified agent to one carrier.
//
// THE GUARD ORDER IS DELIBERATE: country before reachability. Told we hold no
// integration for a carrier that does not operate in their country either, a
// customer goes looking for a coverage gap that is really a wrong-country
// mistake.
func (s *Server) LaunchRcsAgentOnCarrier(ctx context.Context,
	request gen.LaunchRcsAgentOnCarrierRequestObject) (
	gen.LaunchRcsAgentOnCarrierResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	agent, err := store.GetRcsAgent(ctx, s.DB, identity, request.Id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.LaunchRcsAgentOnCarrier404JSONResponse(
			errorBody(codeNotFound, "No such agent.")), nil
	}
	if err != nil {
		return nil, err
	}
	carrier := string(request.Body.Carrier)

	inCountry, err := store.CarriersForCountry(ctx, s.operatorPool(), agent.Country)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(inCountry, carrier) {
		return gen.LaunchRcsAgentOnCarrier409JSONResponse(errorBody(codeConflict,
			carrier+" does not carry RCS in "+agent.Country+".")), nil
	}
	if !s.reachableCarriers()[carrier] {
		return gen.LaunchRcsAgentOnCarrier409JSONResponse(errorBody(codeConflict,
			"We hold no "+carrier+" integration yet, so an agent cannot be put to "+
				"them. Your agent is unaffected on every other network.")), nil
	}

	launched, err := store.LaunchRcsAgentOnCarrier(ctx, s.DB, identity, request.Id, carrier)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return gen.LaunchRcsAgentOnCarrier404JSONResponse(
			errorBody(codeNotFound, "No such agent.")), nil
	case errors.Is(err, store.ErrIllegalTransition):
		return gen.LaunchRcsAgentOnCarrier409JSONResponse(errorBody(codeConflict,
			"This agent is not ready to launch on "+carrier+
				" — it needs an approved verification, and it must not already be "+
				"pending or live there.")), nil
	case err != nil:
		return nil, err
	}
	return gen.LaunchRcsAgentOnCarrier200JSONResponse(s.rcsAgentResponse(ctx, launched)), nil
}

// ApproveRcsAgent accepts a brand claim.
//
// It approves the VERIFICATION and nothing else — the agent still reaches
// nobody until a carrier admits it. Collapsing the two would let an operator
// believe they had made an agent live, and let a customer believe it too.
func (s *Server) ApproveRcsAgent(ctx context.Context, request gen.ApproveRcsAgentRequestObject) (
	gen.ApproveRcsAgentResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return nil, errUnauthenticated
	}
	switch err := store.ReviewRcsAgent(ctx, s.operatorPool(), request.Id, true, ""); {
	case errors.Is(err, store.ErrNotFound):
		return gen.ApproveRcsAgent404JSONResponse(errorBody(codeNotFound, "No such agent.")), nil
	case errors.Is(err, store.ErrIllegalTransition):
		return gen.ApproveRcsAgent409JSONResponse(errorBody(codeConflict,
			"That agent is not awaiting verification.")), nil
	case err != nil:
		return nil, err
	}
	s.recordAgentReview(ctx, operator.Email, request.Id, "Approved an RCS agent")
	return reviewedAgentOf(s, ctx, request.Id, func(agent gen.RcsAgent) gen.ApproveRcsAgentResponseObject {
		return gen.ApproveRcsAgent200JSONResponse(agent)
	})
}

// RejectRcsAgent refuses a brand claim, with a reason the customer reads
// verbatim. Required, because a rejection nobody can act on is a dead end.
func (s *Server) RejectRcsAgent(ctx context.Context, request gen.RejectRcsAgentRequestObject) (
	gen.RejectRcsAgentResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return nil, errUnauthenticated
	}
	reason := strings.TrimSpace(request.Body.Reason)
	if reason == "" {
		return gen.RejectRcsAgent422JSONResponse(errorBody(codeValidation,
			"A rejection needs a reason — the customer is shown it as written.")), nil
	}
	switch err := store.ReviewRcsAgent(ctx, s.operatorPool(), request.Id, false, reason); {
	case errors.Is(err, store.ErrNotFound):
		return gen.RejectRcsAgent404JSONResponse(errorBody(codeNotFound, "No such agent.")), nil
	case errors.Is(err, store.ErrIllegalTransition):
		return gen.RejectRcsAgent409JSONResponse(errorBody(codeConflict,
			"That agent is not awaiting verification.")), nil
	case err != nil:
		return nil, err
	}
	s.recordAgentReview(ctx, operator.Email, request.Id, "Rejected an RCS agent: "+reason)
	return reviewedAgentOf(s, ctx, request.Id, func(agent gen.RcsAgent) gen.RejectRcsAgentResponseObject {
		return gen.RejectRcsAgent200JSONResponse(agent)
	})
}

// reviewedAgent re-reads an agent after an operator decision, on the operator
// pool because the reviewer is not the tenant.
func reviewedAgentOf[T any](s *Server, ctx context.Context, agentID uuid.UUID,
	render func(gen.RcsAgent) T) (T, error) {

	var zero T
	agent, err := store.GetRcsAgentAsOperator(ctx, s.operatorPool(), agentID)
	if err != nil {
		return zero, err
	}
	return render(s.rcsAgentResponse(ctx, agent)), nil
}

// recordAgentReview writes the decision to the operator audit log.
//
// Best-effort and logged rather than returned: the decision has already
// committed, and failing the request now would tell the operator their approval
// did not happen when it did.
func (s *Server) recordAgentReview(ctx context.Context, actor string,
	agentID uuid.UUID, detail string) {

	agent, err := store.GetRcsAgentAsOperator(ctx, s.operatorPool(), agentID)
	if err != nil {
		return
	}
	if err := store.RecordOperatorAction(ctx, s.DB, actor, "rcs_agent.review",
		&agent.TenantID, "", agent.DisplayName, detail); err != nil && s.Logger != nil {
		s.Logger.Warn("an RCS agent decision was not recorded in the audit log",
			"agent", agentID, "error", err)
	}
}
