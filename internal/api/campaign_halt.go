package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// Pause, resume and cancel.
//
// Before these there was no brake. A campaign of nine hundred thousand
// recipients with a wrong link, a wrong list or a wrong price ran to completion
// and the only available action was to watch it.
//
// All three share halt() because all three are the same operation with a
// different verb: find the campaign, take the row lock, check the transition is
// legal, write it, and answer with the whole campaign. Sharing the code is what
// keeps 404-before-409 true for all three rather than for whichever was written
// most carefully.

// haltOutcome is what the three handlers each need back.
type haltOutcome struct {
	campaign store.Campaign
	// notFound and illegal are separated because they are different answers,
	// and because the ORDER matters: an id that is not this tenant's must
	// answer 404 before any transition validation runs, so a probe cannot tell
	// "not yours" from "wrong state".
	notFound bool
	illegal  string
}

func (s *Server) halt(ctx context.Context, id uuid.UUID, action string) (haltOutcome, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return haltOutcome{}, errUnauthenticated
	}
	campaign, err := store.HaltCampaign(ctx, s.DB, identity, id, action)
	if errors.Is(err, store.ErrNotFound) {
		return haltOutcome{notFound: true}, nil
	}
	if errors.Is(err, store.ErrCampaignHaltIllegal) {
		return haltOutcome{illegal: err.Error()}, nil
	}
	if err != nil {
		return haltOutcome{}, err
	}

	s.recordActivity(ctx, identity, "campaign."+action,
		fmt.Sprintf("%s campaign %q", haltVerb(action), campaign.Name))

	return haltOutcome{campaign: campaign}, nil
}

func haltVerb(action string) string {
	switch action {
	case "pause":
		return "Paused"
	case "resume":
		return "Resumed"
	default:
		return "Cancelled"
	}
}

// haltConflict is the 409 message.
func haltConflict(action string) string {
	return fmt.Sprintf("This campaign cannot be %sd from its current state.", action)
}

func (s *Server) PauseCampaign(ctx context.Context, request gen.PauseCampaignRequestObject) (
	gen.PauseCampaignResponseObject, error) {

	outcome, err := s.halt(ctx, request.Id, "pause")
	if err != nil {
		return nil, err
	}
	if outcome.notFound {
		return gen.PauseCampaign404JSONResponse(
			errorBody("not_found", "No such campaign.")), nil
	}
	if outcome.illegal != "" {
		return gen.PauseCampaign409JSONResponse(
			errorBody("conflict", haltConflict("pause"))), nil
	}
	identity, _ := identityFrom(ctx)
	return gen.PauseCampaign200JSONResponse(
		s.toCampaign(ctx, identity, outcome.campaign)), nil
}

// ResumeCampaign puts the campaign back to sending and restarts fan-out from
// the recipient the pause stopped at.
//
// Dispatch runs in the background rather than inside this request, which is the
// one place resume deliberately differs from create. Create sends inline and
// answers when it is finished; a resume that did the same would answer "sent"
// to a caller whose screen is about to render a Pause button, and would hold a
// connection open for the length of the remaining run.
func (s *Server) ResumeCampaign(ctx context.Context, request gen.ResumeCampaignRequestObject) (
	gen.ResumeCampaignResponseObject, error) {

	outcome, err := s.halt(ctx, request.Id, "resume")
	if err != nil {
		return nil, err
	}
	if outcome.notFound {
		return gen.ResumeCampaign404JSONResponse(
			errorBody("not_found", "No such campaign.")), nil
	}
	if outcome.illegal != "" {
		return gen.ResumeCampaign409JSONResponse(
			errorBody("conflict", haltConflict("resume"))), nil
	}

	identity, _ := identityFrom(ctx)
	s.resumeDispatch(ctx, identity, outcome.campaign)

	return gen.ResumeCampaign200JSONResponse(
		s.toCampaign(ctx, identity, outcome.campaign)), nil
}

// resumeDispatch restarts fan-out for a resumed campaign.
//
// WithoutCancel because the request that asked for the resume is about to
// return, and its context going away must not stop the send it just started.
// A campaign that dies mid-fan-out anyway is landed by the stuck-campaign
// sweep, which is the same safety net create already relies on.
func (s *Server) resumeDispatch(ctx context.Context, identity store.Identity,
	campaign store.Campaign) {

	service := s.sendingService(ctx)
	if service == nil {
		return
	}
	detached := context.WithoutCancel(ctx)
	go func() {
		defer func() {
			// A panic in fan-out must not take the process down with it. The
			// campaign is left for the sweep, which is exactly the state it
			// would be in after any other abandoned run.
			if recovered := recover(); recovered != nil && s.Logger != nil {
				s.Logger.Error("campaign resume panicked",
					"campaign", campaign.ID, "panic", recovered)
			}
		}()
		if _, _, err := service.LaunchCampaign(detached, identity, campaign); err != nil &&
			s.Logger != nil {
			s.Logger.Warn("resumed campaign did not finish",
				"campaign", campaign.ID, "error", err)
		}
	}()
}

func (s *Server) CancelCampaign(ctx context.Context, request gen.CancelCampaignRequestObject) (
	gen.CancelCampaignResponseObject, error) {

	outcome, err := s.halt(ctx, request.Id, "cancel")
	if err != nil {
		return nil, err
	}
	if outcome.notFound {
		return gen.CancelCampaign404JSONResponse(
			errorBody("not_found", "No such campaign.")), nil
	}
	if outcome.illegal != "" {
		return gen.CancelCampaign409JSONResponse(
			errorBody("conflict", haltConflict("cancel"))), nil
	}
	identity, _ := identityFrom(ctx)
	return gen.CancelCampaign200JSONResponse(
		s.toCampaign(ctx, identity, outcome.campaign)), nil
}
