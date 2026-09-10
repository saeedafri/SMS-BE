package api

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"

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
		out.CarrierLaunches = append(out.CarrierLaunches, gen.RcsCarrierLaunch{
			Carrier:         gen.CarrierId(launch.Carrier),
			Status:          gen.RcsCarrierLaunchStatus(launch.Status),
			CarrierAgentId:  launch.CarrierAgentID,
			RejectionReason: launch.RejectionReason,
			SubmittedAt:     launch.SubmittedAt,
			UpdatedAt:       &launch.UpdatedAt,
		})
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
	country := string(request.Body.Country)
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
	update := store.RcsAgentUpdate{
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
