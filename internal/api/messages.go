package api

import (
	"context"

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
	if s.ClickHouse == nil {
		// Message logs need ClickHouse. Saying so plainly beats returning an
		// empty page that reads as "you have never sent anything".
		return nil, errClickHouseUnavailable
	}

	filter := store.MessageFilter{Limit: 50}
	if request.Params.Status != nil {
		filter.Status = contractStatusToState(string(*request.Params.Status))
	}
	if request.Params.Channel != nil {
		filter.Channel = string(*request.Params.Channel)
	}
	if request.Params.ErrorClass != nil {
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

	records, total, next, err := store.QueryMessages(ctx, s.ClickHouse, identity.TenantID, filter)
	if err != nil {
		return gen.MessageLogPage{}, err
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
