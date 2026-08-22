package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/messaging"
	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// ListMessages is the logs explorer. It reads ClickHouse, not Postgres: at the
// target volume these rows are ~30 bytes each there and ~900 in Postgres.
func (s *Server) ListMessages(ctx context.Context, request gen.ListMessagesRequestObject) (gen.ListMessagesResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	// Message logs need ClickHouse. Saying so plainly beats returning an empty
	// page that reads as "you have never sent anything".
	if _, err := s.clickhouse(ctx); err != nil {
		return nil, err
	}

	// Enum query parameters are validated explicitly. The generated binder
	// checks TYPES but not enum membership on query params, so an unknown
	// value like ?status=nonsense used to fall through and return the
	// UNFILTERED list with a 200 — a typo in a filter silently answering a
	// different question than the one asked.
	filter := store.MessageFilter{Limit: 50}
	if request.Params.Status != nil {
		if !request.Params.Status.Valid() {
			return nil, fmt.Errorf("%w: %q is not a valid message status",
				errInvalidFilter, string(*request.Params.Status))
		}
		filter.Status = contractStatusToState(string(*request.Params.Status))
	}
	if request.Params.Channel != nil {
		if !request.Params.Channel.Valid() {
			return nil, fmt.Errorf("%w: %q is not a valid channel",
				errInvalidFilter, string(*request.Params.Channel))
		}
		filter.Channel = string(*request.Params.Channel)
	}
	if request.Params.ErrorClass != nil {
		if !request.Params.ErrorClass.Valid() {
			return nil, fmt.Errorf("%w: %q is not a valid error class",
				errInvalidFilter, string(*request.Params.ErrorClass))
		}
		filter.ErrorClass = string(*request.Params.ErrorClass)
	}
	if request.Params.CampaignId != nil {
		filter.CampaignID = request.Params.CampaignId
	}
	if request.Params.Cursor != nil {
		filter.Cursor = *request.Params.Cursor
	}
	if request.Params.Limit != nil {
		filter.Limit = *request.Params.Limit
	}

	page, err := s.messagePage(ctx, identity, filter)
	if err != nil {
		return nil, err
	}
	return gen.ListMessages200JSONResponse(page), nil
}

// messagePage renders a filtered page of message logs. Shared with the campaign
// detail view so one message can never be described two different ways.
func (s *Server) messagePage(ctx context.Context, identity store.Identity,
	filter store.MessageFilter) (gen.MessageLogPage, error) {

	clickhouse, err := s.clickhouse(ctx)
	if err != nil {
		return gen.MessageLogPage{}, err
	}
	records, total, next, err := store.QueryMessages(ctx, clickhouse, identity.TenantID, filter)
	if err != nil {
		return gen.MessageLogPage{}, s.clickhouseFailed(err)
	}

	entries := make([]gen.MessageLogEntry, 0, len(records))
	for _, record := range records {
		entry := gen.MessageLogEntry{
			Id:          record.ID,
			Msisdn:      record.Msisdn,
			Email:       record.Email,
			Segments:    int(record.Segments),
			UpdatedAt:   record.UpdatedAt,
			SentAt:      record.SentAt,
			DeliveredAt: record.DeliveredAt,
			ErrorCode:   record.ErrorCode,
		}
		// The whole honesty claim in one line: an internal "accepted" surfaces
		// as "sent", never "delivered".
		entry.Status = gen.MessageStatus(messaging.ContractStatus(messaging.State(record.Status)))
		entry.CampaignId = record.CampaignID
		entry.CampaignName = record.CampaignName
		entry.Channel = gen.ChannelId(record.Channel)
		if record.ErrorClass != nil && *record.ErrorClass != "" {
			// Nullable oneOf in the contract, so a generated union type.
			var class gen.MessageLogEntry_ErrorClass
			_ = class.FromMessageErrorClass(gen.MessageErrorClass(*record.ErrorClass))
			entry.ErrorClass = &class
		}
		if record.FraudFlag != "" {
			flag := gen.MessageFraudFlag(record.FraudFlag)
			entry.FraudFlag = &flag
		}
		entries = append(entries, entry)
	}

	page := gen.MessageLogPage{Messages: entries, Total: int(total)}
	if next != "" {
		page.NextCursor = &next
	}
	return page, nil
}

// contractStatusToState maps the contract's coarse filter value back to the
// internal states it covers. "sent" spans submitted and accepted, so a filter
// on it must not silently match only one.
func contractStatusToState(status string) string {
	switch status {
	case "queued":
		return string(messaging.StateQueued)
	case "sent":
		return string(messaging.StateAccepted)
	case "delivered":
		return string(messaging.StateDelivered)
	case "failed":
		return string(messaging.StateUndelivered)
	default:
		return ""
	}
}

// sendScopeFor maps a sender's channel to the scope an API key must carry.
//
// The vocabulary is the frontend's registry verbatim (send:sms, send:rcs and
// so on), for the same reason ListApiScopes uses it: a scope string is part of
// the public API surface that customers paste into their own code, and two
// vocabularies is how a key created in the dashboard fails against the API.
func sendScopeFor(channel string) string {
	return "send:" + strings.ToLower(channel)
}

// SendMessage sends one message, now.
//
// The gap this closes: /v1/messages was read-only, so the only way to send
// anything through this platform was to build a campaign in the dashboard. A
// CPaaS whose customers cannot send one message from their own code is missing
// the product. The API keys were the other half of it — mintable since the
// dashboard shipped and wired to nothing, so every sk_live_ key a customer
// pasted into their code authenticated exactly nothing.
//
// Every rule a campaign send passes, this passes: same Service.Send, same gate,
// same hold on the wallet, same message row in the log. That is deliberate and
// it is the whole reason the data plane was never an HTTP handler — a send API
// that relaxed one check is how a suppressed contact eventually gets messaged.
func (s *Server) SendMessage(ctx context.Context, request gen.SendMessageRequestObject) (
	gen.SendMessageResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.SendMessage401JSONResponse(errorBody(codeUnauthenticated,
			"Send an API key as a bearer token, or sign in.")), nil
	}
	if request.Body == nil {
		return gen.SendMessage422JSONResponse(errorBody(codeValidation,
			"A JSON body with senderId, to and body is required.")), nil
	}
	to := strings.TrimSpace(request.Body.To)
	body := strings.TrimSpace(request.Body.Body)
	if to == "" || body == "" {
		return gen.SendMessage422JSONResponse(errorBody(codeValidation,
			"Both to and body must be present and non-empty.")), nil
	}

	// The sender is read before the scope check because the scope depends on
	// the channel, and a key must not learn which sender ids exist by watching
	// 403 turn into 404.
	sender, err := store.GetSenderID(ctx, s.DB, identity, request.Body.SenderId)
	if errors.Is(err, store.ErrNotFound) {
		return gen.SendMessage422JSONResponse(errorBody(codeValidation,
			"No such sender id on this account.")), nil
	}
	if err != nil {
		return nil, err
	}

	// Scopes apply to API keys only. A dashboard session is authorised by role,
	// and treating a session as "a key with every scope" would silently grant
	// send rights the key model exists to limit.
	if scopes, isKey := scopesFrom(ctx); isKey {
		if !oneOf(sendScopeFor(sender.Channel), scopes) {
			return gen.SendMessage403JSONResponse(errorBody(codeForbidden,
				fmt.Sprintf("This API key does not carry the %s scope.",
					sendScopeFor(sender.Channel)))), nil
		}
	}

	if !s.allowSend(ctx, identity.TenantID, keyEnvironment(ctx)) {
		return gen.SendMessage429JSONResponse(errorBody("rate_limited",
			"Too many sends. See GET /v1/developer/rate-limit for this "+
				"environment's budget, and retry in a moment.")), nil
	}

	service := s.sendingService(ctx)
	if service == nil {
		return gen.SendMessage422JSONResponse(errorBody(codeValidation,
			"Sending is not available on this deployment.")), nil
	}

	result, err := service.Send(ctx, identity, sending.SendRequest{
		SenderID: request.Body.SenderId, TemplateID: request.Body.TemplateId,
		Msisdn: to, Body: body,
	})
	if err != nil {
		return nil, err
	}

	// 202 whatever the verdict, including a rejection. The request was
	// well-formed and the outcome is in the body — a caller integrating against
	// this needs the message id and the reason far more than it needs a status
	// code that collapses "we refused it" and "you sent us nonsense" into one.
	out := gen.SendMessageResult{
		Status:    gen.SendMessageResultStatus(result.Status),
		Segments:  result.Segments,
		CostMinor: result.CostMinor,
		Currency:  result.Currency,
	}
	if result.MessageID != uuid.Nil {
		id := result.MessageID
		out.Id = &id
	}
	if result.FailureCode != "" {
		code := result.FailureCode
		out.ErrorCode = &code
	}
	return gen.SendMessage202JSONResponse(out), nil
}
