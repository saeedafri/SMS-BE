package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/audience"
	"github.com/saeedafri/sms-be/internal/domain/messaging"
	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"
)

// Test send: the same message to a handful of the customer's own handsets
// before a campaign goes to everyone. It is a real send through the real gate,
// billed like any other, so a test that passes means the campaign will.
//
// At most five recipients, so it cannot be used as a bulk-send route that skips
// a campaign's audience rules.
func (s *Server) mountTestSendRoutes(r chi.Router) {
	r.Post("/v1/messages/test", s.testSend)
}

const maxTestRecipients = 5

type testSendBody struct {
	SenderID   uuid.UUID         `json:"senderId"`
	TemplateID *uuid.UUID        `json:"templateId"`
	Body       string            `json:"body"`
	Variables  map[string]string `json:"variables"`
	Recipients []string          `json:"recipients"`
	// Link is where a {{link}} in this message goes, raw so the same rules and
	// words as campaign create apply to it.
	Link json.RawMessage `json:"link"`
}

// testLinkLife is how long a test send's real link works: long enough to tap
// it on the handset and show a colleague, short enough not to linger.
const testLinkLife = 7 * 24 * time.Hour

type testSendResult struct {
	Recipient   string     `json:"recipient"`
	MessageID   *uuid.UUID `json:"messageId"`
	Status      string     `json:"status"`
	FailureCode string     `json:"failureCode,omitempty"`
}

func (s *Server) testSend(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Missing or invalid bearer token")
		return
	}
	var body testSendBody
	if !decodeStrict(w, r, &body) {
		return
	}
	body.Body = strings.TrimSpace(body.Body)
	if body.SenderID == uuid.Nil || (body.Body == "" && body.TemplateID == nil) {
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			"senderId and a body (or a templateId) are required.")
		return
	}
	if len(body.Recipients) == 0 || len(body.Recipients) > maxTestRecipients {
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			"recipients must hold between 1 and 5 numbers.")
		return
	}
	sender, err := store.CachedSenderID(r.Context(), s.DB, s.Hot, identity, body.SenderID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, "No such sender id on this account.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	numbers := make([]string, 0, len(body.Recipients))
	seen := map[string]bool{}
	for _, raw := range body.Recipients {
		number, valid := audience.NormaliseMsisdn(raw, sender.Country)
		if !valid {
			number, valid = audience.NormaliseE164(raw)
		}
		if !valid {
			writeError(w, http.StatusUnprocessableEntity, codeValidation,
				raw+" is not a phone number.")
			return
		}
		if !seen[number] {
			seen[number] = true
			numbers = append(numbers, number)
		}
	}
	// The link, judged exactly as campaign create judges it: required when the
	// message uses {{link}}, refused when it does not.
	messageParts := []store.Template{{Body: &body.Body}}
	if body.TemplateID != nil {
		template, err := store.GetTemplate(r.Context(), s.DB, identity, *body.TemplateID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
			return
		}
		messageParts = append(messageParts, template)
	}
	link, problem := s.linkFor(identity, r.Host, body.Link, messageParts...)
	if problem != "" {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, problem)
		return
	}
	service := s.sendingService(r.Context())
	if service == nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			"Sending is not available on this deployment.")
		return
	}
	results := make([]testSendResult, 0, len(numbers))
	for _, number := range numbers {
		if !s.allowSend(r.Context(), identity.TenantID, keyEnvironment(r.Context())) {
			results = append(results, testSendResult{Recipient: number, Status: "rejected",
				FailureCode: "rate_limited"})
			continue
		}
		variables, made, err := s.testLink(r, identity, link, number, body.Variables)
		if err != nil {
			s.Logger.Error("test send link", "error", err.Error())
			writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
			return
		}
		result, err := service.Send(r.Context(), identity, sending.SendRequest{
			SenderID: body.SenderID, TemplateID: body.TemplateID, Msisdn: number,
			Body: body.Body, Variables: variables,
		})
		// Refused, or never sent: the link was for a message that does not
		// exist, so it goes too. A carrier rejection is not this — that message
		// left, and its link is the one it carried.
		if err != nil && made != "" {
			if deleteErr := store.DeleteShortLink(context.WithoutCancel(r.Context()), s.DB,
				identity, made); deleteErr != nil {
				s.Logger.Warn("test send link left behind", "code", made, "error", deleteErr.Error())
			}
		}
		if err != nil && !messaging.IsRefusal(err) {
			s.Logger.Error("test send", "error", err.Error())
			writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
			return
		}
		entry := testSendResult{Recipient: number, Status: result.Status, FailureCode: result.FailureCode}
		if result.MessageID != uuid.Nil {
			id := result.MessageID
			entry.MessageID = &id
		}
		results = append(results, entry)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"results": results})
}

// testLink makes the real link a test send's {{link}} is filled with — for this
// number, no campaign, labelled a test, a week to live — and returns the
// variables carrying it with the link's code. No link, nothing made.
//
// The customer's own variables never supply it: the builder sends no sample for
// {{link}}, and one that arrived anyway would put an untracked URL on the
// handset of the person checking that tracking works.
func (s *Server) testLink(r *http.Request, identity store.Identity, link *store.LinkSpec,
	number string, variables map[string]string) (map[string]string, string, error) {

	if link == nil {
		return variables, "", nil
	}
	label := "test send"
	expires := time.Now().Add(testLinkLife)
	made, err := store.CreateShortLinks(r.Context(), s.DB, identity, []store.ShortLink{{
		Destination: link.Destination, Recipient: &number, Label: &label,
		AppendClickID: link.AppendClickID, ExpiresAt: &expires}})
	if err != nil {
		return nil, "", err
	}
	filled := make(map[string]string, len(variables)+1)
	for name, value := range variables {
		filled[name] = value
	}
	filled[audience.LinkSlot] = s.linkBaseFor(r) + "/l/" + made[0].Code
	return filled, made[0].Code, nil
}
