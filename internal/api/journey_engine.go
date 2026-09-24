package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

const (
	// journeyLease is how long a claimed enrolment is held before another
	// cycle may take it: far longer than one contact's steps take to run.
	journeyLease = 5 * time.Minute
	// journeyRetry is how long a contact waits after a send step failed for a
	// reason that was not theirs — a missing rate, an unreachable database.
	journeyRetry = 5 * time.Minute
	// journeyHold is how long a contact waits when the tenant's daily send
	// ceiling is spent.
	journeyHold = time.Hour
	journeyPage = 200
)

// RunJourneys enrols each active journey's trigger list and moves every
// enrolled contact whose time has come through their next steps.
//
// Only ACTIVE journeys are touched. Pausing or archiving a journey freezes it:
// nobody new enrols and nobody in flight advances, and a contact mid-wait
// stays mid-wait. Resuming picks each contact up where they were, so a wait
// that ran out while paused ends on the first cycle after the resume.
func (s *Server) RunJourneys(ctx context.Context) error {
	service := s.sendingService(ctx)
	if service == nil || s.OperatorDB == nil {
		return nil
	}
	journeys, err := store.ActiveJourneys(ctx, s.OperatorDB)
	if err != nil {
		return err
	}
	var failures []error
	for _, active := range journeys {
		if err := s.runJourney(ctx, service, active); err != nil {
			failures = append(failures, fmt.Errorf("journey %s: %w", active.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (s *Server) runJourney(ctx context.Context, service *sending.Service,
	active store.ActiveJourney) error {

	identity := store.Identity{TenantID: active.TenantID}
	journey, err := store.GetJourney(ctx, s.DB, identity, active.ID)
	if err != nil {
		return err
	}
	if journey.Status != "active" {
		return nil
	}
	if _, err := store.EnrollJourney(ctx, s.DB, identity, journey, s.now()); err != nil {
		return err
	}
	steps := journeySteps(journey)

	var failures []error
	for {
		now := s.now()
		due, err := store.ClaimDueEnrollments(ctx, s.DB, identity, journey.ID,
			now, now.Add(journeyLease), journeyPage)
		if err != nil {
			return err
		}
		for _, enrollment := range due {
			if err := s.advanceEnrollment(ctx, service, identity, journey, steps, enrollment); err != nil {
				failures = append(failures, fmt.Errorf("enrollment %s: %w", enrollment.ID, err))
			}
		}
		if len(due) < journeyPage {
			return errors.Join(failures...)
		}
	}
}

// advanceEnrollment runs one contact forward from where they are: through
// every send step it reaches, until a wait holds them or the steps run out.
func (s *Server) advanceEnrollment(ctx context.Context, service *sending.Service,
	identity store.Identity, journey store.Journey, steps []gen.JourneyStep,
	e store.Enrollment) error {

	// Due while waiting means the wait is over.
	if e.Waiting {
		e.Waiting = false
		e.StepIndex++
	}
	for e.State == "active" {
		now := s.now()
		if e.StepIndex >= len(steps) {
			e.State = "completed"
			break
		}
		step := steps[e.StepIndex]
		if wait, err := step.AsJourneyStepWait(); err == nil && wait.Type == "wait" {
			e.Waiting = true
			e.NextRunAt = now.Add(time.Duration(wait.DurationMinutes) * time.Minute)
			break
		}
		send, err := step.AsJourneyStepSend()
		senderID, senderOK := parsePathID(send.SenderId)
		templateID, templateOK := parsePathID(send.TemplateId)
		if err != nil || send.Type != "send" || !senderOK || !templateOK {
			// A step this engine cannot read is passed over rather than
			// holding every contact behind it forever.
			s.Logger.Warn("journey step unreadable; skipped", "journey", journey.ID,
				"step", e.StepIndex)
			e.StepIndex++
			continue
		}

		contact, err := store.GetContact(ctx, s.DB, identity, e.ContactID)
		if err != nil {
			e.NextRunAt = now.Add(journeyRetry)
			return errors.Join(err, store.SaveEnrollment(ctx, s.DB, identity, e))
		}
		outcome, err := service.SendJourneyStep(ctx, identity, journey,
			senderID, templateID, contact)
		if err != nil {
			e.NextRunAt = now.Add(journeyRetry)
			return errors.Join(err, store.SaveEnrollment(ctx, s.DB, identity, e))
		}
		switch outcome {
		case sending.JourneySuppressed:
			e.State = "exited_suppressed"
		case sending.JourneyHeld:
			e.NextRunAt = now.Add(journeyHold)
			return store.SaveEnrollment(ctx, s.DB, identity, e)
		default:
			e.StepIndex++
		}
	}
	return store.SaveEnrollment(ctx, s.DB, identity, e)
}
