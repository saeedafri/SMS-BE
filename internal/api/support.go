package api

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
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

func (s *Server) GetSupportTickets(ctx context.Context, request gen.GetSupportTicketsRequestObject) (gen.GetSupportTicketsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	var status, category *string
	if request.Params.Status != nil {
		value := string(*request.Params.Status)
		status = &value
	}
	if request.Params.Category != nil {
		value := string(*request.Params.Category)
		category = &value
	}
	tickets, err := store.ListSupportTickets(ctx, s.DB, identity, status, category)
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

func (s *Server) ListConversations(ctx context.Context, request gen.ListConversationsRequestObject) (gen.ListConversationsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	filter := store.ConversationFilter{Unread: request.Params.Unread}
	if request.Params.Channel != nil {
		value := string(*request.Params.Channel)
		filter.Channel = &value
	}
	if request.Params.Status != nil {
		value := string(*request.Params.Status)
		filter.Status = &value
	}
	conversations, err := store.ListConversations(ctx, s.DB, identity, filter)
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

	// Submit the reply to the carrier, exactly as a campaign message is.
	//
	// A reply used to be written straight to the thread as "queued" and left
	// there forever — no carrier ever saw it, and no delivery report ever came
	// back, so the agent watched a message sit in limbo with no way to tell a
	// stuck send from a delivered one. It was, in effect, not sent at all.
	//
	// The sandbox connector resolves deterministically from the recipient's
	// number, so the same thread always behaves the same way and a failing case
	// is reproducible rather than a coin toss.
	status := "queued"
	messageID := uuid.NewString()
	if s.Connector != nil {
		receipts, err := s.Connector.Submit(ctx, []connector.Submission{{
			MessageID: messageID, Msisdn: conversation.Identity,
			Body: request.Body.Body, Channel: conversation.Channel,
			Country: conversation.Country,
		}})
		if err != nil || len(receipts) == 0 {
			// The carrier being unreachable is not the customer's mistake, and
			// losing their typing is the worst possible response to it.
			s.Logger.Warn("conversation reply not submitted to carrier",
				"conversation", conversationID, "error", err)
		} else if !receipts[0].Accepted {
			status = "failed"
		} else {
			status = "sent"
			// The sandbox posts its delivery reports to an internal queue
			// rather than calling us back. Draining here keeps a reply's final
			// state honest — delivered means a report said so, not that we
			// assumed it — without waiting on a worker tick the agent watching
			// the thread would have to sit through.
			if sandbox, ok := s.Connector.(*connector.Sandbox); ok {
				for _, report := range sandbox.DrainReports() {
					if report.MessageID != messageID {
						continue
					}
					if report.Delivered {
						status = "delivered"
					} else {
						status = "failed"
					}
				}
			}
		}
	}

	if _, err := store.AppendConversationMessage(ctx, s.DB, identity, store.ConversationMessage{
		ConversationID: conversationID, Direction: "outbound",
		Body: request.Body.Body, Segments: &segments, Status: &status,
	}); err != nil {
		return nil, err
	}

	// An inbox reply is an outbound message on a paid channel, so it costs the
	// same as any other. This was not being charged at all: a customer could
	// answer a thousand conversations and be billed for none of them.
	//
	// Priced from the same rate card campaigns use, per segment. A reply that
	// runs to two segments costs twice as much for the same reason a campaign
	// message does — the carrier charges per segment, not per intention.
	//
	// A missing rate is not fatal. The reply has already been written and the
	// customer has been told it sent; refusing to record the charge is better
	// than refusing the reply, and it is visible in the log rather than silent.
	// Only a message a carrier actually took is charged. Billing a reply the
	// carrier rejected would charge the customer for nothing — the same
	// distinction between accepted and delivered that the rest of this system
	// is built around.
	if rate, err := store.FindPricingRate(ctx, s.DB, identity.TenantID,
		conversation.Country, conversation.Channel, ""); status != "failed" &&
		err == nil && rate.PerSegmentMinor > 0 {
		amount := rate.PerSegmentMinor * int64(segments)
		if _, err := store.AppendLedgerEntry(ctx, s.DB, identity, store.LedgerEntry{
			Currency: rate.Currency, Type: "charge", AmountMinor: amount,
			// The channel is in the description because the ledger is read by
			// someone reconciling a bill, and "Inbox reply" alone does not say
			// why one line cost 12 paise and the next 55.
			Description: "Inbox reply (" + conversation.Channel + ")",
		}); err != nil {
			s.Logger.Warn("inbox reply sent but not billed",
				"conversation", conversationID, "error", err)
		}
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
