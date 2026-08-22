package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// toJourney maps a stored journey onto the contract shape.
//
// completedCount and exitedSuppressedCount are derived at read time, exactly as
// the contract documents them. Storing them would let the counters drift from
// the enrolment they describe the moment a contact is suppressed after
// enrolling — and a funnel that disagrees with the suppression list is worse
// than no funnel.
func (s *Server) toJourney(ctx context.Context, identity store.Identity,
	journey store.Journey) gen.Journey {

	out := gen.Journey{
		Id: journey.ID, Name: journey.Name,
		Status:     gen.JourneyStatus(journey.Status),
		Recipients: journey.Recipients,
		CreatedAt:  journey.CreatedAt, ActivatedAt: journey.ActivatedAt,
		Steps: []gen.JourneyStep{},
	}

	var trigger gen.JourneyTrigger
	if journey.TriggerType == "scheduled" && journey.TriggerRunAt != nil {
		listID := ""
		if journey.TriggerListID != nil {
			listID = journey.TriggerListID.String()
		}
		_ = trigger.FromJourneyTriggerScheduled(gen.JourneyTriggerScheduled{
			Type: "scheduled", ListId: listID, RunAt: *journey.TriggerRunAt,
		})
	} else {
		listID := ""
		if journey.TriggerListID != nil {
			listID = journey.TriggerListID.String()
		}
		_ = trigger.FromJourneyTriggerListEntry(gen.JourneyTriggerListEntry{
			Type: "list_entry", ListId: listID,
		})
	}
	out.Trigger = trigger

	// Steps round-trip through the stored jsonb rather than being rebuilt
	// field by field, so a step shape the contract adds later survives without
	// a backend change.
	var raw []json.RawMessage
	if len(journey.Steps) > 0 {
		_ = json.Unmarshal(journey.Steps, &raw)
	}
	for _, item := range raw {
		var step gen.JourneyStep
		if err := json.Unmarshal(item, &step); err == nil {
			out.Steps = append(out.Steps, step)
		}
	}

	// A journey that never activated has enrolled nobody, so both derived
	// counts are zero rather than a fraction of the cohort.
	if journey.Status == "active" || journey.Status == "paused" {
		completed, suppressed := s.journeyCounts(ctx, identity, journey)
		out.CompletedCount = completed
		out.ExitedSuppressedCount = suppressed
	}
	return out
}

// journeyCounts derives the funnel from the enrolment cohort and the current
// suppression list. Anyone suppressed since enrolling has exited; the rest are
// counted as having completed the sequence.
func (s *Server) journeyCounts(ctx context.Context, identity store.Identity,
	journey store.Journey) (completed int, exitedSuppressed int) {

	if journey.TriggerListID == nil {
		return 0, 0
	}
	contacts, _, _, err := store.ListContacts(ctx, s.DB, identity, journey.TriggerListID, "", 1000)
	if err != nil {
		return 0, 0
	}
	identities := make([]string, 0, len(contacts))
	for _, contact := range contacts {
		identities = append(identities, contact.Msisdn)
	}
	suppressed, err := store.SuppressedSet(ctx, s.DB, identity, identities)
	if err != nil {
		return len(contacts), 0
	}
	for _, contact := range contacts {
		if suppressed[contact.Msisdn] {
			exitedSuppressed++
			continue
		}
		completed++
	}
	return completed, exitedSuppressed
}

func (s *Server) ListJourneys(ctx context.Context, _ gen.ListJourneysRequestObject) (gen.ListJourneysResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	journeys, err := store.ListJourneys(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.Journey, 0, len(journeys))
	for _, journey := range journeys {
		out = append(out, s.toJourney(ctx, identity, journey))
	}
	return gen.ListJourneys200JSONResponse(out), nil
}

func (s *Server) GetJourney(ctx context.Context, request gen.GetJourneyRequestObject) (gen.GetJourneyResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	journeyID, valid := parsePathID(request.Id)
	if !valid {
		return gen.GetJourney404JSONResponse(errorBody("not_found", "No such journey.")), nil
	}
	journey, err := store.GetJourney(ctx, s.DB, identity, journeyID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.GetJourney404JSONResponse(errorBody("not_found", "No such journey.")), nil
	}
	if err != nil {
		return nil, err
	}

	base := s.toJourney(ctx, identity, journey)
	stepCounts := make([]gen.JourneyStepCount, 0, len(base.Steps))
	for _, step := range base.Steps {
		if send, err := step.AsJourneyStepSend(); err == nil && send.Id != "" {
			stepCounts = append(stepCounts, gen.JourneyStepCount{
				StepId: send.Id, Count: base.CompletedCount,
			})
			continue
		}
		if wait, err := step.AsJourneyStepWait(); err == nil && wait.Id != "" {
			stepCounts = append(stepCounts, gen.JourneyStepCount{
				StepId: wait.Id, Count: base.CompletedCount,
			})
		}
	}

	return gen.GetJourney200JSONResponse(gen.JourneyDetail{
		Id: base.Id, Name: base.Name, Status: base.Status, Trigger: base.Trigger,
		Steps: base.Steps, Recipients: base.Recipients, CreatedAt: base.CreatedAt,
		ActivatedAt: base.ActivatedAt, CompletedCount: base.CompletedCount,
		ExitedSuppressedCount: base.ExitedSuppressedCount,
		Funnel: gen.JourneyFunnel{
			StepCounts: stepCounts, Completed: base.CompletedCount,
			ExitedSuppressed: base.ExitedSuppressedCount,
			TotalEnrolled:    base.Recipients,
		},
	}), nil
}

func (s *Server) CreateJourney(ctx context.Context, request gen.CreateJourneyRequestObject) (gen.CreateJourneyResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	body := request.Body
	if body.Name == "" {
		return gen.CreateJourney422JSONResponse(errorBody(codeValidation,
			"A journey name is required.")), nil
	}
	// A journey with no steps does nothing when activated. Refusing here beats
	// letting someone activate an empty sequence and wonder why nothing sent.
	if len(body.Steps) == 0 {
		return gen.CreateJourney422JSONResponse(errorBody(codeValidation,
			"A journey needs at least one step.")), nil
	}

	journey := store.Journey{Name: body.Name, TriggerType: "list_entry"}

	if scheduled, err := body.Trigger.AsJourneyTriggerScheduled(); err == nil && scheduled.Type == "scheduled" {
		journey.TriggerType = "scheduled"
		runAt := scheduled.RunAt
		journey.TriggerRunAt = &runAt
		if listID, valid := parsePathID(scheduled.ListId); valid {
			journey.TriggerListID = &listID
		}
	} else if entry, err := body.Trigger.AsJourneyTriggerListEntry(); err == nil {
		if listID, valid := parsePathID(entry.ListId); valid {
			journey.TriggerListID = &listID
		}
	}
	if journey.TriggerListID == nil {
		return gen.CreateJourney422JSONResponse(errorBody(codeValidation,
			"A journey trigger needs a contact list.")), nil
	}

	// Validate every send step before storing: a step pointing at an
	// unapproved sender would fail at the first enrolment, long after the
	// person who built it has moved on.
	for _, step := range body.Steps {
		send, err := step.AsJourneyStepSend()
		if err != nil || send.Type != "send" {
			continue
		}
		senderID, valid := parsePathID(send.SenderId)
		if !valid {
			return gen.CreateJourney422JSONResponse(errorBody(codeValidation,
				"A send step has an invalid sender.")), nil
		}
		if _, err := store.GetSenderID(ctx, s.DB, identity, senderID); err != nil {
			return gen.CreateJourney422JSONResponse(errorBody(codeValidation,
				"A send step points at a sender that does not exist.")), nil
		}
		templateID, valid := parsePathID(send.TemplateId)
		if !valid {
			return gen.CreateJourney422JSONResponse(errorBody(codeValidation,
				"A send step has an invalid template.")), nil
		}
		if _, err := store.GetTemplate(ctx, s.DB, identity, templateID); err != nil {
			return gen.CreateJourney422JSONResponse(errorBody(codeValidation,
				"A send step points at a template that does not exist.")), nil
		}
	}

	encoded, err := json.Marshal(body.Steps)
	if err != nil {
		return gen.CreateJourney422JSONResponse(errorBody(codeValidation,
			"Those steps could not be read.")), nil
	}
	journey.Steps = encoded

	// Freeze the cohort size, same role as a campaign's estimate.
	if _, total, _, err := store.ListContacts(ctx, s.DB, identity,
		journey.TriggerListID, "", 1); err == nil {
		journey.Recipients = total
	}

	created, err := store.CreateJourney(ctx, s.DB, identity, journey)
	if err != nil {
		return nil, err
	}
	return gen.CreateJourney201JSONResponse(s.toJourney(ctx, identity, created)), nil
}

// transitionJourney is shared by activate, pause, resume and archive. The legal
// moves live in one place so the four endpoints cannot disagree about what is
// allowed.
func (s *Server) transitionJourney(ctx context.Context, id string,
	to string, allowedFrom []string) (gen.Journey, string, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.Journey{}, "", errUnauthenticated
	}
	journeyID, valid := parsePathID(id)
	if !valid {
		return gen.Journey{}, "not_found", nil
	}
	current, err := store.GetJourney(ctx, s.DB, identity, journeyID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.Journey{}, "not_found", nil
	}
	if err != nil {
		return gen.Journey{}, "", err
	}

	permitted := false
	for _, from := range allowedFrom {
		if current.Status == from {
			permitted = true
		}
	}
	if !permitted {
		return gen.Journey{}, "illegal", nil
	}

	updated, err := store.SetJourneyStatus(ctx, s.DB, identity, journeyID, to)
	if err != nil {
		return gen.Journey{}, "", err
	}
	return s.toJourney(ctx, identity, updated), "", nil
}

func (s *Server) ActivateJourney(ctx context.Context, request gen.ActivateJourneyRequestObject) (gen.ActivateJourneyResponseObject, error) {
	journey, problem, err := s.transitionJourney(ctx, request.Id, "active", []string{"draft", "paused"})
	if err != nil {
		return nil, err
	}
	switch problem {
	case "not_found":
		return gen.ActivateJourney404JSONResponse(errorBody("not_found", "No such journey.")), nil
	case "illegal":
		return gen.ActivateJourney422JSONResponse(errorBody(codeValidation,
			"Only a draft or paused journey can be activated.")), nil
	}
	return gen.ActivateJourney200JSONResponse(journey), nil
}

func (s *Server) PauseJourney(ctx context.Context, request gen.PauseJourneyRequestObject) (gen.PauseJourneyResponseObject, error) {
	journey, problem, err := s.transitionJourney(ctx, request.Id, "paused", []string{"active"})
	if err != nil {
		return nil, err
	}
	switch problem {
	case "not_found":
		return gen.PauseJourney404JSONResponse(errorBody("not_found", "No such journey.")), nil
	case "illegal":
		return gen.PauseJourney422JSONResponse(errorBody(codeValidation,
			"Only an active journey can be paused.")), nil
	}
	return gen.PauseJourney200JSONResponse(journey), nil
}

func (s *Server) ResumeJourney(ctx context.Context, request gen.ResumeJourneyRequestObject) (gen.ResumeJourneyResponseObject, error) {
	journey, problem, err := s.transitionJourney(ctx, request.Id, "active", []string{"paused"})
	if err != nil {
		return nil, err
	}
	switch problem {
	case "not_found":
		return gen.ResumeJourney404JSONResponse(errorBody("not_found", "No such journey.")), nil
	case "illegal":
		return gen.ResumeJourney422JSONResponse(errorBody(codeValidation,
			"Only a paused journey can be resumed.")), nil
	}
	return gen.ResumeJourney200JSONResponse(journey), nil
}

// ArchiveJourney is deliberately terminal — an archived journey cannot be
// reactivated. Bringing one back would re-enrol a cohort that was frozen at a
// different time, and the safe move is to copy it into a new journey instead.
func (s *Server) ArchiveJourney(ctx context.Context, request gen.ArchiveJourneyRequestObject) (gen.ArchiveJourneyResponseObject, error) {
	journey, problem, err := s.transitionJourney(ctx, request.Id, "archived",
		[]string{"draft", "active", "paused"})
	if err != nil {
		return nil, err
	}
	switch problem {
	case "not_found":
		return gen.ArchiveJourney404JSONResponse(errorBody("not_found", "No such journey.")), nil
	case "illegal":
		return gen.ArchiveJourney422JSONResponse(errorBody(codeValidation,
			"That journey is already archived.")), nil
	}
	return gen.ArchiveJourney200JSONResponse(journey), nil
}

// UnarchiveJourney restores an archived journey.
//
// Sits beside ArchiveJourney and shares its authentication, tenant scoping and
// serializer — but not transitionJourney, and that is the point. Every other
// verb moves to one named status; this one lands on draft or paused depending
// on whether the journey ever ran, and it decides that inside the same
// statement that writes it. See store.UnarchiveJourney.
//
// Archived was terminal until now: a customer who archived a journey by mistake
// had no route back, in the product or the API.
func (s *Server) UnarchiveJourney(ctx context.Context, request gen.UnarchiveJourneyRequestObject) (
	gen.UnarchiveJourneyResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.UnarchiveJourney401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	journeyID, valid := parsePathID(request.Id)
	if !valid {
		return gen.UnarchiveJourney404JSONResponse(
			errorBody(codeNotFound, "No such journey.")), nil
	}

	// Scoped to the caller's tenant by WithTenant, so another tenant's journey
	// is not found rather than forbidden — a 403 would confirm it exists.
	journey, err := store.UnarchiveJourney(ctx, s.DB, identity, journeyID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return gen.UnarchiveJourney404JSONResponse(
			errorBody(codeNotFound, "No such journey.")), nil
	case errors.Is(err, store.ErrConflict):
		// Read by a person, not parsed by a machine.
		return gen.UnarchiveJourney422JSONResponse(errorBody("invalid_status",
			"Only an archived journey can be unarchived.")), nil
	case err != nil:
		return nil, err
	}
	return gen.UnarchiveJourney200JSONResponse(s.toJourney(ctx, identity, journey)), nil
}

// UpdateJourney renames a journey or replaces its steps or trigger.
//
// Every field is optional, so the automation screen can rename without
// resending the whole step list. Status is not among them: it moves through
// SetJourneyStatus, which owns the rule that activated_at is stamped once and
// never rewritten.
func (s *Server) UpdateJourney(ctx context.Context, request gen.UpdateJourneyRequestObject) (gen.UpdateJourneyResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	journeyID, valid := parsePathID(request.Id)
	if !valid {
		return gen.UpdateJourney404JSONResponse(errorBody(codeNotFound, "No such journey.")), nil
	}
	body := request.Body
	if body == nil {
		return gen.UpdateJourney422JSONResponse(errorBody(codeValidation,
			"Nothing to update.")), nil
	}

	var name *string
	if body.Name != nil {
		trimmed := strings.TrimSpace(*body.Name)
		// An empty name is a rename to nothing, which leaves a row nobody can
		// identify in a list. Rejected rather than stored.
		if trimmed == "" {
			return gen.UpdateJourney422JSONResponse(errorBody(codeValidation,
				"A journey name is required.")), nil
		}
		name = &trimmed
	}

	var steps []byte
	if body.Steps != nil {
		if len(*body.Steps) == 0 {
			return gen.UpdateJourney422JSONResponse(errorBody(codeValidation,
				"A journey needs at least one step.")), nil
		}
		// Send steps are validated against real senders and templates for the
		// same reason they are on create: a step pointing at something that
		// does not exist fails at the first enrolment, long after whoever
		// built it has moved on.
		for _, step := range *body.Steps {
			send, err := step.AsJourneyStepSend()
			if err != nil || send.Type != "send" {
				continue
			}
			senderID, ok := parsePathID(send.SenderId)
			if !ok {
				return gen.UpdateJourney422JSONResponse(errorBody(codeValidation,
					"A send step has an invalid sender.")), nil
			}
			if _, err := store.GetSenderID(ctx, s.DB, identity, senderID); err != nil {
				return gen.UpdateJourney422JSONResponse(errorBody(codeValidation,
					"A send step points at a sender that does not exist.")), nil
			}
			templateID, ok := parsePathID(send.TemplateId)
			if !ok {
				return gen.UpdateJourney422JSONResponse(errorBody(codeValidation,
					"A send step has an invalid template.")), nil
			}
			if _, err := store.GetTemplate(ctx, s.DB, identity, templateID); err != nil {
				return gen.UpdateJourney422JSONResponse(errorBody(codeValidation,
					"A send step points at a template that does not exist.")), nil
			}
		}
		encoded, err := json.Marshal(*body.Steps)
		if err != nil {
			return gen.UpdateJourney422JSONResponse(errorBody(codeValidation,
				"Those steps could not be read.")), nil
		}
		steps = encoded
	}

	var triggerType *string
	var triggerListID *uuid.UUID
	if body.Trigger != nil {
		if scheduled, err := body.Trigger.AsJourneyTriggerScheduled(); err == nil && scheduled.Type == "scheduled" {
			kind := "scheduled"
			triggerType = &kind
			if listID, ok := parsePathID(scheduled.ListId); ok {
				triggerListID = &listID
			}
		} else if entry, err := body.Trigger.AsJourneyTriggerListEntry(); err == nil {
			kind := "list_entry"
			triggerType = &kind
			if listID, ok := parsePathID(entry.ListId); ok {
				triggerListID = &listID
			}
		}
		if triggerListID == nil {
			return gen.UpdateJourney422JSONResponse(errorBody(codeValidation,
				"A journey trigger needs a contact list.")), nil
		}
	}

	journey, err := store.UpdateJourney(ctx, s.DB, identity, journeyID,
		name, steps, triggerType, triggerListID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.UpdateJourney404JSONResponse(errorBody(codeNotFound, "No such journey.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.UpdateJourney200JSONResponse(s.toJourney(ctx, identity, journey)), nil
}
