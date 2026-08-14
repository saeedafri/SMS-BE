package api

import (
	"context"
	"fmt"

	"github.com/saeedafri/sms-be/internal/domain/messaging"
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
