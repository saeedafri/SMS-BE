package api

import (
	"errors"
	"net/http"
	"strings"

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
}

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
		result, err := service.Send(r.Context(), identity, sending.SendRequest{
			SenderID: body.SenderID, TemplateID: body.TemplateID, Msisdn: number,
			Body: body.Body, Variables: body.Variables,
		})
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
