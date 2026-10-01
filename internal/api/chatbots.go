package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/audience"
	"github.com/saeedafri/sms-be/internal/domain/messaging"
	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"
)

// Keyword chatbots: a customer texts OFFER and the tenant's flow answers it.
// Mounted directly until the contract declares them; see
// docs/HANDOFF_TO_UI_2026-10-02-sigmo-gaps.md for the shapes.
func (s *Server) mountChatbotRoutes(r chi.Router) {
	r.Get("/v1/chatbots", s.listChatbots)
	r.Post("/v1/chatbots", s.createChatbot)
	r.Get("/v1/chatbots/{id}", s.getChatbot)
	r.Patch("/v1/chatbots/{id}", s.updateChatbot)
	r.Delete("/v1/chatbots/{id}", s.deleteChatbot)
}

const (
	maxKeywordsPerFlow = 20
	maxKeywordLength   = 40
	maxReplyLength     = 1600
	// A person who sends the keyword twice gets one answer a minute: an
	// auto-reply that answers every message is a loop the moment the other end
	// is also a machine.
	chatbotCooldown = time.Minute
)

type chatbotOut struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Channel    string     `json:"channel"`
	SenderID   uuid.UUID  `json:"senderId"`
	TemplateID *uuid.UUID `json:"templateId"`
	ReplyBody  *string    `json:"replyBody"`
	Active     bool       `json:"active"`
	Keywords   []string   `json:"keywords"`
	CreatedAt  time.Time  `json:"createdAt"`
}

func toChatbotOut(f store.ChatbotFlow) chatbotOut {
	keywords := f.Keywords
	if keywords == nil {
		keywords = []string{}
	}
	return chatbotOut{f.ID, f.Name, f.Channel, f.SenderID, f.TemplateID, f.ReplyBody,
		f.Active, keywords, f.CreatedAt}
}

type chatbotBody struct {
	Name       *string    `json:"name"`
	Channel    *string    `json:"channel"`
	Keywords   *[]string  `json:"keywords"`
	SenderID   *uuid.UUID `json:"senderId"`
	TemplateID *uuid.UUID `json:"templateId"`
	ReplyBody  *string    `json:"replyBody"`
	Active     *bool      `json:"active"`
}

func (s *Server) chatbotAdmin(w http.ResponseWriter, r *http.Request, write bool) (store.Identity, bool) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Missing or invalid bearer token")
		return identity, false
	}
	// A flow spends the tenant's wallet every time it fires, so changing one is a
	// settings decision.
	if write && !canManageSettings(identity.Role) {
		writeError(w, http.StatusForbidden, codeForbidden, "Member role cannot change chatbots.")
		return identity, false
	}
	return identity, true
}

func cleanKeywords(raw []string) ([]string, string) {
	if len(raw) == 0 || len(raw) > maxKeywordsPerFlow {
		return nil, "keywords must hold between 1 and 20 words."
	}
	out := make([]string, 0, len(raw))
	for _, text := range raw {
		keyword := store.NormaliseKeyword(text)
		switch {
		case keyword == "" || len(keyword) > maxKeywordLength:
			return nil, "each keyword must be 1-40 characters."
		case strings.IndexFunc(keyword, unicode.IsControl) >= 0:
			return nil, "keywords cannot contain control characters."
		case store.IsStopKeyword(keyword):
			// Answering STOP with an offer is the compliance failure the opt-out
			// handling exists to prevent.
			return nil, strings.ToUpper(keyword) + " is reserved: it opts the person out."
		}
		if !slices.Contains(out, keyword) {
			out = append(out, keyword)
		}
	}
	slices.Sort(out)
	return out, ""
}

// validateFlow checks what only the database can know: that the sender and
// template are the tenant's, on the flow's channel, and approved.
func (s *Server) validateFlow(ctx context.Context, identity store.Identity, flow store.ChatbotFlow) string {
	if !slices.Contains([]string{"SMS", "RCS", "WHATSAPP"}, flow.Channel) {
		return "channel must be one of: SMS, RCS, WHATSAPP."
	}
	if flow.TemplateID == nil && (flow.ReplyBody == nil || strings.TrimSpace(*flow.ReplyBody) == "") {
		return "a flow needs a templateId or a replyBody."
	}
	if flow.ReplyBody != nil && len(*flow.ReplyBody) > maxReplyLength {
		return "replyBody is at most 1600 characters."
	}
	sender, err := store.GetSenderID(ctx, s.DB, identity, flow.SenderID)
	if err != nil {
		return "No such sender id on this account."
	}
	if sender.Status != "approved" {
		return "The sender must be approved before a chatbot can answer from it."
	}
	if sender.Channel != flow.Channel {
		return "The sender is on " + sender.Channel + ", not " + flow.Channel + "."
	}
	// India sends nothing that is not an instance of a registered template, so
	// a plain-text reply there would be refused every time it fired.
	if sending.RegisteredTemplateRequired(sender.Country) && flow.TemplateID == nil {
		return "Messages from a " + sender.Country + " sender must use a registered template: choose a templateId."
	}
	if flow.TemplateID != nil {
		template, err := store.GetTemplate(ctx, s.DB, identity, *flow.TemplateID)
		if err != nil {
			return "No such template on this account."
		}
		if template.Status != "approved" || template.SenderID != flow.SenderID {
			return "The template must be approved and belong to the sender."
		}
		// A keyword reply has nobody's details to fill a slot with: the sender
		// may not even be a contact yet. A template with variables would be
		// refused every time it fired, which nobody would see.
		slots := slices.Clone(template.Variables)
		if template.Body != nil {
			slots = append(slots, audience.Fill(*template.Body, map[string]string{}, nil).Missing...)
		}
		if len(slots) > 0 {
			return "A chatbot reply cannot use a template with variables: " +
				strings.Join(slots, ", ") + "."
		}
	}
	return ""
}

func (s *Server) listChatbots(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.chatbotAdmin(w, r, false)
	if !ok {
		return
	}
	flows, err := store.ListChatbotFlows(r.Context(), s.DB, identity)
	if err != nil {
		s.Logger.Error("list chatbots", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	out := make([]chatbotOut, len(flows))
	for i, f := range flows {
		out[i] = toChatbotOut(f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"chatbots": out})
}

func (s *Server) getChatbot(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.chatbotAdmin(w, r, false)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, "id must be a uuid.")
		return
	}
	flow, err := store.GetChatbotFlow(r.Context(), s.DB, identity, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "No such chatbot.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	writeJSON(w, http.StatusOK, toChatbotOut(flow))
}

func (s *Server) createChatbot(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.chatbotAdmin(w, r, true)
	if !ok {
		return
	}
	var body chatbotBody
	if !decodeStrict(w, r, &body) {
		return
	}
	if body.Name == nil || strings.TrimSpace(*body.Name) == "" || len(*body.Name) > 120 ||
		body.Channel == nil || body.SenderID == nil || body.Keywords == nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			"name (1-120 characters), channel, senderId and keywords are required.")
		return
	}
	keywords, problem := cleanKeywords(*body.Keywords)
	if problem != "" {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, problem)
		return
	}
	flow := store.ChatbotFlow{Name: strings.TrimSpace(*body.Name), Channel: *body.Channel,
		SenderID: *body.SenderID, TemplateID: body.TemplateID, ReplyBody: body.ReplyBody,
		Active: body.Active == nil || *body.Active, Keywords: keywords}
	if problem := s.validateFlow(r.Context(), identity, flow); problem != "" {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, problem)
		return
	}
	created, err := store.CreateChatbotFlow(r.Context(), s.DB, identity, flow)
	if errors.Is(err, store.ErrKeywordTaken) {
		writeError(w, http.StatusConflict, "conflict",
			"Another chatbot on this channel already answers one of those keywords.")
		return
	}
	if err != nil {
		s.Logger.Error("create chatbot", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	writeJSON(w, http.StatusCreated, toChatbotOut(created))
}

func (s *Server) updateChatbot(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.chatbotAdmin(w, r, true)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, "id must be a uuid.")
		return
	}
	var body chatbotBody
	if !decodeStrict(w, r, &body) {
		return
	}
	if body.Channel != nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			"A chatbot's channel cannot be changed; create a new one.")
		return
	}
	flow, err := store.GetChatbotFlow(r.Context(), s.DB, identity, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "No such chatbot.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	if body.Name != nil {
		if strings.TrimSpace(*body.Name) == "" || len(*body.Name) > 120 {
			writeError(w, http.StatusUnprocessableEntity, codeValidation, "name must be 1-120 characters.")
			return
		}
		flow.Name = strings.TrimSpace(*body.Name)
	}
	if body.Keywords != nil {
		keywords, problem := cleanKeywords(*body.Keywords)
		if problem != "" {
			writeError(w, http.StatusUnprocessableEntity, codeValidation, problem)
			return
		}
		flow.Keywords = keywords
	}
	if body.SenderID != nil {
		flow.SenderID = *body.SenderID
	}
	if body.TemplateID != nil {
		flow.TemplateID = body.TemplateID
	}
	if body.ReplyBody != nil {
		flow.ReplyBody = body.ReplyBody
	}
	if body.Active != nil {
		flow.Active = *body.Active
	}
	if problem := s.validateFlow(r.Context(), identity, flow); problem != "" {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, problem)
		return
	}
	err = store.UpdateChatbotFlow(r.Context(), s.DB, identity, flow)
	if errors.Is(err, store.ErrKeywordTaken) {
		writeError(w, http.StatusConflict, "conflict",
			"Another chatbot on this channel already answers one of those keywords.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	writeJSON(w, http.StatusOK, toChatbotOut(flow))
}

func (s *Server) deleteChatbot(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.chatbotAdmin(w, r, true)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, "id must be a uuid.")
		return
	}
	if err := store.DeleteChatbotFlow(r.Context(), s.DB, identity, id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "No such chatbot.")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// answerKeyword sends a flow's reply to a contact who just wrote one of its
// keywords. Never on a STOP, which the caller has already acted on.
//
// Without Redis it does not reply at all: the cooldown is the only thing
// between an auto-reply and a loop with another machine, and a missing guard is
// a worse failure than a missing reply.
func (s *Server) answerKeyword(ctx context.Context, identity store.Identity, contactID uuid.UUID,
	msisdn, channel, text string) {

	flow, found, err := store.FlowForKeyword(ctx, s.DB, identity, channel, text)
	if err != nil {
		s.Logger.Error("chatbot lookup", "error", err.Error())
		return
	}
	if !found || s.Redis == nil {
		return
	}
	key := "chatbot:" + identity.TenantID.String() + ":" + flow.ID.String() + ":" + msisdn
	first, err := s.Redis.SetNX(ctx, key, 1, chatbotCooldown).Result()
	if err != nil || !first {
		return
	}
	s.runAsync(func(ctx context.Context) {
		service := s.sendingService(ctx)
		if service == nil {
			s.Logger.Warn("chatbot reply not sent: sending is unavailable", "flow", flow.ID)
			return
		}
		contact, err := store.GetContact(ctx, s.DB, identity, contactID)
		if err != nil {
			s.Logger.Warn("chatbot reply not sent: contact unreadable", "flow", flow.ID, "error", err.Error())
			return
		}
		body := ""
		if flow.ReplyBody != nil {
			body = *flow.ReplyBody
		}
		if flow.TemplateID != nil && body == "" {
			if template, err := store.GetTemplate(ctx, s.DB, identity, *flow.TemplateID); err == nil &&
				template.Body != nil {
				body = *template.Body
			}
		}
		result, err := service.Send(ctx, identity, sending.SendRequest{
			SenderID: flow.SenderID, TemplateID: flow.TemplateID, Msisdn: msisdn,
			Body: body, Variables: contact.Fields,
		})
		s.Logger.Debug("chatbot send", "flow", flow.ID, "status", result.Status, "code", result.FailureCode)
		if err != nil && !messaging.IsRefusal(err) {
			s.Logger.Error("chatbot reply", "flow", flow.ID, "error", err.Error())
			return
		}
		if result.Status == "rejected" || result.Status == "failed" {
			s.Logger.Warn("chatbot reply not sent", "flow", flow.ID, "code", result.FailureCode)
			return
		}
		// The reply belongs in the thread, so an agent opening it sees what the
		// bot already said.
		conversationID, err := store.EnsureConversation(ctx, s.DB, identity, contactID, channel)
		if err != nil {
			s.Logger.Warn("chatbot reply sent but not filed in the thread", "flow", flow.ID, "error", err.Error())
			return
		}
		if _, err := store.AppendConversationMessage(ctx, s.DB, identity, store.ConversationMessage{
			ConversationID: conversationID, Direction: "outbound", Body: body}); err != nil {
			s.Logger.Warn("chatbot reply sent but not filed in the thread", "flow", flow.ID, "error", err.Error())
		}
	})
}
