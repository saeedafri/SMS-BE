package api

import (
	"context"
	"errors"

	"github.com/saeedafri/sms-be/internal/domain/billing"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func toSupportMessages(messages []store.SupportMessage) []gen.SupportMessage {
	out := make([]gen.SupportMessage, 0, len(messages))
	for _, message := range messages {
		out = append(out, gen.SupportMessage{
			Id: message.ID.String(), Author: gen.SupportMessageAuthor(message.Author),
			AuthorName: message.AuthorName, Body: message.Body,
			CreatedAt: message.CreatedAt,
		})
	}
	return out
}

func toTicketDetail(ticket store.SupportTicket, messages []store.SupportMessage) gen.SupportTicketDetail {
	return gen.SupportTicketDetail{
		Id: ticket.ID, TenantId: ticket.TenantID, TenantName: ticket.TenantName,
		Subject: ticket.Subject, Category: gen.TicketCategory(ticket.Category),
		Status: gen.TicketStatus(ticket.Status), CreatedAt: ticket.CreatedAt,
		UpdatedAt: ticket.UpdatedAt, Messages: toSupportMessages(messages),
	}
}

func (s *Server) GetSupportTickets(ctx context.Context, _ gen.GetSupportTicketsRequestObject) (gen.GetSupportTicketsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	tickets, err := store.ListSupportTickets(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.SupportTicket, 0, len(tickets))
	for _, ticket := range tickets {
		out = append(out, gen.SupportTicket{
			Id: ticket.ID, TenantId: ticket.TenantID, TenantName: ticket.TenantName,
			Subject: ticket.Subject, Category: gen.TicketCategory(ticket.Category),
			Status:    gen.TicketStatus(ticket.Status),
			CreatedAt: ticket.CreatedAt, UpdatedAt: ticket.UpdatedAt,
		})
	}
	return gen.GetSupportTickets200JSONResponse(gen.SupportTicketPage{Tickets: out}), nil
}

func (s *Server) GetSupportTicket(ctx context.Context, request gen.GetSupportTicketRequestObject) (gen.GetSupportTicketResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	ticketID, valid := parsePathID(request.Id)
	if !valid {
		return gen.GetSupportTicket404JSONResponse(errorBody("not_found", "No such ticket.")), nil
	}
	ticket, messages, err := store.GetSupportTicket(ctx, s.DB, identity, ticketID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.GetSupportTicket404JSONResponse(errorBody("not_found", "No such ticket.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetSupportTicket200JSONResponse(toTicketDetail(ticket, messages)), nil
}

func (s *Server) CreateSupportTicket(ctx context.Context, request gen.CreateSupportTicketRequestObject) (gen.CreateSupportTicketResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if request.Body.Subject == "" || request.Body.Body == "" {
		return gen.CreateSupportTicket422JSONResponse(errorBody(codeValidation,
			"A subject and a message are both required.")), nil
	}
	ticket, err := store.CreateSupportTicket(ctx, s.DB, identity,
		request.Body.Subject, string(request.Body.Category),
		request.Body.Body, identity.Name)
	if err != nil {
		return nil, err
	}
	_, messages, err := store.GetSupportTicket(ctx, s.DB, identity, ticket.ID)
	if err != nil {
		return nil, err
	}
	return gen.CreateSupportTicket201JSONResponse(toTicketDetail(ticket, messages)), nil
}

func (s *Server) AddSupportMessage(ctx context.Context, request gen.AddSupportMessageRequestObject) (gen.AddSupportMessageResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	ticketID, valid := parsePathID(request.Id)
	if !valid {
		return gen.AddSupportMessage404JSONResponse(errorBody("not_found", "No such ticket.")), nil
	}
	if request.Body.Body == "" {
		return gen.AddSupportMessage422JSONResponse(errorBody(codeValidation,
			"A message body is required.")), nil
	}
	if _, err := store.AddSupportMessage(ctx, s.DB, identity, ticketID,
		"customer", identity.Name, request.Body.Body); errors.Is(err, store.ErrNotFound) {
		return gen.AddSupportMessage404JSONResponse(errorBody("not_found", "No such ticket.")), nil
	} else if err != nil {
		return nil, err
	}
	ticket, messages, err := store.GetSupportTicket(ctx, s.DB, identity, ticketID)
	if err != nil {
		return nil, err
	}
	return gen.AddSupportMessage200JSONResponse(toTicketDetail(ticket, messages)), nil
}

func toConversation(conversation store.Conversation) gen.Conversation {
	return gen.Conversation{
		Id: conversation.ID, ContactId: conversation.ContactID,
		ContactName: conversation.ContactName, Identity: conversation.Identity,
		Channel: gen.ConversationChannelId(conversation.Channel),
		Status:  gen.ConversationStatus(conversation.Status),
		Unread:  conversation.Unread, Suppressed: conversation.Suppressed,
		LastMessagePreview: conversation.LastMessagePreview,
		CreatedAt:          conversation.CreatedAt, UpdatedAt: conversation.UpdatedAt,
	}
}

func toConversationDetail(conversation store.Conversation,
	messages []store.ConversationMessage) gen.ConversationDetail {

	out := gen.ConversationDetail{
		Id: conversation.ID, ContactId: conversation.ContactID,
		ContactName: conversation.ContactName, Identity: conversation.Identity,
		Channel: gen.ConversationChannelId(conversation.Channel),
		Status:  gen.ConversationStatus(conversation.Status),
		Unread:  conversation.Unread, Suppressed: conversation.Suppressed,
		LastMessagePreview: conversation.LastMessagePreview,
		CreatedAt:          conversation.CreatedAt, UpdatedAt: conversation.UpdatedAt,
		Messages: make([]gen.ConversationMessage, 0, len(messages)),
	}
	for _, message := range messages {
		entry := gen.ConversationMessage{
			Id: message.ID, ConversationId: message.ConversationID,
			Direction: gen.ConversationMessageDirection(message.Direction),
			Body:      message.Body, CreatedAt: message.CreatedAt,
			KeywordMatched: message.KeywordMatched, Segments: message.Segments,
		}
		if message.Status != nil {
			// Nullable oneOf in the contract, so a generated union type.
			var status gen.ConversationMessage_Status
			_ = status.FromMessageStatus(gen.MessageStatus(*message.Status))
			entry.Status = &status
		}
		out.Messages = append(out.Messages, entry)
	}
	return out
}

func (s *Server) ListConversations(ctx context.Context, _ gen.ListConversationsRequestObject) (gen.ListConversationsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	conversations, err := store.ListConversations(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.Conversation, 0, len(conversations))
	for _, conversation := range conversations {
		out = append(out, toConversation(conversation))
	}
	return gen.ListConversations200JSONResponse(gen.ConversationPage{
		Conversations: out, Total: len(out),
	}), nil
}

// conversationDetail is the shared read-back every conversation mutation ends
// with, so a reply and a close return the same shape from the same source.
func (s *Server) conversationDetail(ctx context.Context, identity store.Identity,
	id string) (gen.ConversationDetail, bool, error) {

	conversationID, valid := parsePathID(id)
	if !valid {
		return gen.ConversationDetail{}, false, nil
	}
	conversation, messages, err := store.GetConversation(ctx, s.DB, identity, conversationID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.ConversationDetail{}, false, nil
	}
	if err != nil {
		return gen.ConversationDetail{}, false, err
	}
	return toConversationDetail(conversation, messages), true, nil
}

func (s *Server) GetConversation(ctx context.Context, request gen.GetConversationRequestObject) (gen.GetConversationResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	detail, found, err := s.conversationDetail(ctx, identity, request.Id)
	if err != nil {
		return nil, err
	}
	if !found {
		return gen.GetConversation404JSONResponse(errorBody("not_found", "No such conversation.")), nil
	}
	return gen.GetConversation200JSONResponse(detail), nil
}

// ReplyToConversation sends an outbound message in a thread.
//
// A suppressed contact is refused here even though the send gate would also
// refuse it. Failing at the API boundary gives the operator an immediate,
// specific reason instead of a message that silently appears in the thread and
// then fails — and this is the one place a human is deliberately messaging
// someone who asked not to be contacted.
func (s *Server) ReplyToConversation(ctx context.Context, request gen.ReplyToConversationRequestObject) (gen.ReplyToConversationResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if request.Body.Body == "" {
		return gen.ReplyToConversation422JSONResponse(errorBody(codeValidation,
			"A reply body is required.")), nil
	}
	conversationID, valid := parsePathID(request.Id)
	if !valid {
		return gen.ReplyToConversation404JSONResponse(errorBody("not_found", "No such conversation.")), nil
	}
	conversation, _, err := store.GetConversation(ctx, s.DB, identity, conversationID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.ReplyToConversation404JSONResponse(errorBody("not_found", "No such conversation.")), nil
	}
	if err != nil {
		return nil, err
	}
	if conversation.Suppressed {
		return gen.ReplyToConversation422JSONResponse(errorBody(codeValidation,
			"This contact has opted out, so they cannot be messaged.")), nil
	}

	segments := billing.SegmentCount(request.Body.Body)
	status := "queued"
	if _, err := store.AppendConversationMessage(ctx, s.DB, identity, store.ConversationMessage{
		ConversationID: conversationID, Direction: "outbound",
		Body: request.Body.Body, Segments: &segments, Status: &status,
	}); err != nil {
		return nil, err
	}

	detail, found, err := s.conversationDetail(ctx, identity, request.Id)
	if err != nil {
		return nil, err
	}
	if !found {
		return gen.ReplyToConversation404JSONResponse(errorBody("not_found", "No such conversation.")), nil
	}
	return gen.ReplyToConversation200JSONResponse(detail), nil
}

func (s *Server) CloseConversation(ctx context.Context, request gen.CloseConversationRequestObject) (gen.CloseConversationResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	conversationID, valid := parsePathID(request.Id)
	if !valid {
		return gen.CloseConversation404JSONResponse(errorBody("not_found", "No such conversation.")), nil
	}
	if err := store.SetConversationStatus(ctx, s.DB, identity, conversationID, "closed"); errors.Is(err, store.ErrNotFound) {
		return gen.CloseConversation404JSONResponse(errorBody("not_found", "No such conversation.")), nil
	} else if err != nil {
		return nil, err
	}
	detail, _, err := s.conversationDetail(ctx, identity, request.Id)
	if err != nil {
		return nil, err
	}
	return gen.CloseConversation200JSONResponse(detail), nil
}

func (s *Server) ReopenConversation(ctx context.Context, request gen.ReopenConversationRequestObject) (gen.ReopenConversationResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	conversationID, valid := parsePathID(request.Id)
	if !valid {
		return gen.ReopenConversation404JSONResponse(errorBody("not_found", "No such conversation.")), nil
	}
	if err := store.SetConversationStatus(ctx, s.DB, identity, conversationID, "open"); errors.Is(err, store.ErrNotFound) {
		return gen.ReopenConversation404JSONResponse(errorBody("not_found", "No such conversation.")), nil
	} else if err != nil {
		return nil, err
	}
	detail, _, err := s.conversationDetail(ctx, identity, request.Id)
	if err != nil {
		return nil, err
	}
	return gen.ReopenConversation200JSONResponse(detail), nil
}

func (s *Server) MarkConversationRead(ctx context.Context, request gen.MarkConversationReadRequestObject) (gen.MarkConversationReadResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	conversationID, valid := parsePathID(request.Id)
	if !valid {
		return gen.MarkConversationRead404JSONResponse(errorBody("not_found", "No such conversation.")), nil
	}
	if err := store.MarkConversationRead(ctx, s.DB, identity, conversationID); errors.Is(err, store.ErrNotFound) {
		return gen.MarkConversationRead404JSONResponse(errorBody("not_found", "No such conversation.")), nil
	} else if err != nil {
		return nil, err
	}
	detail, _, err := s.conversationDetail(ctx, identity, request.Id)
	if err != nil {
		return nil, err
	}
	return gen.MarkConversationRead200JSONResponse(detail), nil
}
