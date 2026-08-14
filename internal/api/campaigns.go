package api

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/messaging"
	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// toCampaign maps the stored campaign onto the contract shape.
//
// Counts come from ClickHouse rather than from columns on the campaign row:
// a counter incremented at send time drifts the moment a delivery report
// changes a message's state, and then the campaign page and the logs disagree
// about the same message. Deriving them means they cannot.
func (s *Server) toCampaign(ctx context.Context, identity store.Identity,
	campaign store.Campaign) gen.Campaign {

	out := gen.Campaign{
		Id:                 campaign.ID,
		Name:               campaign.Name,
		Channel:            gen.ChannelId(campaign.Channel),
		Country:            gen.CountryCode(campaign.Country),
		SenderId:           campaign.SenderID.String(),
		TemplateId:         campaign.TemplateID.String(),
		Status:             gen.CampaignStatus(campaign.Status),
		Recipients:         campaign.Recipients,
		SegmentsPerMessage: campaign.SegmentsPerMessage,
		CostMinorMin:       int(campaign.CostMinorMin),
		CostMinorMax:       int(campaign.CostMinorMax),
		Currency:           gen.CurrencyCode(campaign.Currency),
		CreatedAt:          campaign.CreatedAt,
		ScheduledAt:        campaign.ScheduledAt,
		SendStartedAt:      campaign.SendStartedAt,
	}
	if campaign.ListID != nil {
		out.ListId = campaign.ListID.String()
	}
	if campaign.RetryOf != nil {
		value := campaign.RetryOf.String()
		out.RetryOf = &value
	}
	if campaign.RetriedByCampaignID != nil {
		value := campaign.RetriedByCampaignID.String()
		out.RetriedByCampaignId = &value
	}
	if campaign.FallbackChannel != nil && campaign.FallbackSenderID != nil &&
		campaign.FallbackTemplateID != nil {
		var fallback gen.Campaign_Fallback
		_ = fallback.FromCampaignFallback(gen.CampaignFallback{
			Channel:    gen.ChannelId(*campaign.FallbackChannel),
			SenderId:   campaign.FallbackSenderID.String(),
			TemplateId: campaign.FallbackTemplateID.String(),
		})
		out.Fallback = &fallback
	}

	// Counts are best-effort: if ClickHouse is unreachable the campaign row
	// still renders, just without its delivery breakdown. A 500 here would
	// hide the campaign entirely over a missing number.
	if clickhouse, err := s.clickhouse(ctx); err == nil {
		if counts, err := store.CountCampaignMessages(ctx, clickhouse,
			identity.TenantID, campaign.ID); err == nil {
			out.Counts = gen.CampaignCounts{
				Queued: counts.Queued, Sent: counts.Sent,
				Delivered: counts.Delivered, Failed: counts.Failed, Read: counts.Read,
			}
			out.Delivered = counts.Delivered
			out.Failed = counts.Failed
		}
	}
	return out
}

func (s *Server) ListCampaigns(ctx context.Context, _ gen.ListCampaignsRequestObject) (gen.ListCampaignsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	campaigns, err := store.ListCampaigns(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.Campaign, 0, len(campaigns))
	for _, campaign := range campaigns {
		out = append(out, s.toCampaign(ctx, identity, campaign))
	}
	return gen.ListCampaigns200JSONResponse(out), nil
}

func (s *Server) GetCampaign(ctx context.Context, request gen.GetCampaignRequestObject) (gen.GetCampaignResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	campaign, err := store.GetCampaign(ctx, s.DB, identity, request.Id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.GetCampaign404JSONResponse{}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetCampaign200JSONResponse(s.toCampaign(ctx, identity, campaign)), nil
}

// CreateCampaign creates the campaign and launches it.
//
// Fan-out runs inline rather than on a queue. That is honest for the list sizes
// this handles today and keeps the failure mode simple — the caller learns
// immediately if the send could not start. It is the piece to move onto River
// before campaigns reach the hundreds of thousands.
func (s *Server) CreateCampaign(ctx context.Context, request gen.CreateCampaignRequestObject) (gen.CreateCampaignResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	body := request.Body

	senderID, err := uuid.Parse(body.SenderId)
	if err != nil {
		return gen.CreateCampaign422JSONResponse(errorBody(codeValidation, "senderId must be a uuid")), nil
	}
	templateID, err := uuid.Parse(body.TemplateId)
	if err != nil {
		return gen.CreateCampaign422JSONResponse(errorBody(codeValidation, "templateId must be a uuid")), nil
	}

	campaign := store.Campaign{
		Name:       body.Name,
		Channel:    string(body.Channel),
		Country:    string(body.Country),
		SenderID:   senderID,
		TemplateID: templateID,
		Status:     "queued",
		Currency:   "INR",
	}
	if body.ListId != nil && *body.ListId != "" {
		listID, err := uuid.Parse(*body.ListId)
		if err != nil {
			return gen.CreateCampaign422JSONResponse(errorBody(codeValidation, "listId must be a uuid")), nil
		}
		campaign.ListID = &listID
	}
	if body.RetryOf != nil && *body.RetryOf != "" {
		retryOf, err := uuid.Parse(*body.RetryOf)
		if err != nil {
			return gen.CreateCampaign422JSONResponse(errorBody(codeValidation, "retryOf must be a uuid")), nil
		}
		campaign.RetryOf = &retryOf
	}
	if body.ScheduledAt != nil {
		campaign.ScheduledAt = body.ScheduledAt
		// A scheduled campaign must not send now. Recording it as queued and
		// launching anyway would ignore the one instruction the user gave.
		campaign.Status = "scheduled"
	}
	if body.Fallback != nil {
		if fallback, err := body.Fallback.AsCampaignFallback(); err == nil && fallback.SenderId != "" {
			channel := string(fallback.Channel)
			fallbackSender, senderErr := uuid.Parse(fallback.SenderId)
			fallbackTemplate, templateErr := uuid.Parse(fallback.TemplateId)
			if senderErr == nil && templateErr == nil {
				campaign.FallbackChannel = &channel
				campaign.FallbackSenderID = &fallbackSender
				campaign.FallbackTemplateID = &fallbackTemplate
			}
		}
	}

	// Freeze the estimate at creation: the user approved this number, and a
	// rate change afterwards must not rewrite what they agreed to.
	service := s.sendingService(ctx)
	if service != nil {
		template, err := store.GetTemplate(ctx, s.DB, identity, templateID)
		if errors.Is(err, store.ErrNotFound) {
			return gen.CreateCampaign422JSONResponse(errorBody(codeValidation, "templateId template not found")), nil
		}
		if err != nil {
			return nil, err
		}
		templateBody := ""
		if template.Body != nil {
			templateBody = *template.Body
		}
		estimate, err := service.EstimateCampaign(ctx, identity, campaign.ListID,
			campaign.Country, campaign.Channel, templateBody)
		if err == nil {
			campaign.Recipients = estimate.Recipients
			campaign.SegmentsPerMessage = estimate.SegmentsPerMessage
			campaign.CostMinorMin = estimate.CostMinorMin
			campaign.CostMinorMax = estimate.CostMinorMax
			campaign.Currency = estimate.Currency
		}
	}

	created, err := store.CreateCampaign(ctx, s.DB, identity, campaign)
	if err != nil {
		return nil, err
	}

	if service != nil && created.Status == "queued" {
		if _, _, err := service.LaunchCampaign(ctx, identity, created); err != nil {
			return nil, err
		}
		// Re-read so the response carries the post-send status and counts
		// rather than the pre-send ones the caller would otherwise cache.
		if refreshed, err := store.GetCampaign(ctx, s.DB, identity, created.ID); err == nil {
			created = refreshed
		}
	}

	return gen.CreateCampaign201JSONResponse(s.toCampaign(ctx, identity, created)), nil
}

func (s *Server) EstimateCampaign(ctx context.Context, request gen.EstimateCampaignRequestObject) (gen.EstimateCampaignResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	service := s.sendingService(ctx)
	if service == nil {
		return nil, errClickHouseUnavailable
	}
	body := request.Body

	var listID *uuid.UUID
	if body.ListId != "" {
		parsed, err := uuid.Parse(body.ListId)
		if err != nil {
			return gen.EstimateCampaign422JSONResponse(errorBody(codeValidation, "listId must be a uuid")), nil
		}
		listID = &parsed
	}

	templateBody := ""
	if body.TemplateId != "" {
		templateID, err := uuid.Parse(body.TemplateId)
		if err != nil {
			return gen.EstimateCampaign422JSONResponse(errorBody(codeValidation, "templateId must be a uuid")), nil
		}
		template, err := store.GetTemplate(ctx, s.DB, identity, templateID)
		if err == nil && template.Body != nil {
			templateBody = *template.Body
		}
	}

	estimate, err := service.EstimateCampaign(ctx, identity, listID,
		string(body.Country), string(body.Channel), templateBody)
	if err != nil {
		return nil, err
	}
	return gen.EstimateCampaign200JSONResponse(gen.CampaignEstimate{
		Recipients:         estimate.Recipients,
		SegmentsPerMessage: estimate.SegmentsPerMessage,
		CostMinorMin:       int(estimate.CostMinorMin),
		CostMinorMax:       int(estimate.CostMinorMax),
		Currency:           gen.CurrencyCode(estimate.Currency),
	}), nil
}

func (s *Server) ListCampaignMessages(ctx context.Context, request gen.ListCampaignMessagesRequestObject) (gen.ListCampaignMessagesResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	clickhouse, err := s.clickhouse(ctx)
	if err != nil {
		return nil, err
	}

	campaignID := request.Id
	filter := store.MessageFilter{CampaignID: &campaignID, Limit: 50}
	if request.Params.Cursor != nil {
		filter.Cursor = *request.Params.Cursor
	}
	if request.Params.Limit != nil {
		filter.Limit = *request.Params.Limit
	}
	if request.Params.Status != nil {
		filter.Status = contractStatusToState(string(*request.Params.Status))
	}

	records, total, next, err := store.QueryMessages(ctx, clickhouse, identity.TenantID, filter)
	if err != nil {
		return nil, err
	}

	// The campaign detail view uses Message, not the logs explorer's
	// MessageLogEntry — a narrower shape for a page that already knows which
	// campaign it is showing.
	messages := make([]gen.Message, 0, len(records))
	for _, record := range records {
		message := gen.Message{
			Id:          record.ID,
			Msisdn:      record.Msisdn,
			Email:       record.Email,
			Segments:    int(record.Segments),
			UpdatedAt:   record.UpdatedAt,
			SentAt:      record.SentAt,
			DeliveredAt: record.DeliveredAt,
			ErrorCode:   record.ErrorCode,
			CampaignId:  record.CampaignID,
			Status:      gen.MessageStatus(messaging.ContractStatus(messaging.State(record.Status))),
		}
		if record.ErrorClass != nil && *record.ErrorClass != "" {
			var class gen.Message_ErrorClass
			_ = class.FromMessageErrorClass(gen.MessageErrorClass(*record.ErrorClass))
			message.ErrorClass = &class
		}
		if record.FraudFlag != "" {
			flag := gen.MessageFraudFlag(record.FraudFlag)
			message.FraudFlag = &flag
		}
		messages = append(messages, message)
	}

	page := gen.MessagePage{Messages: messages, Total: int(total)}
	if next != "" {
		page.NextCursor = &next
	}
	return gen.ListCampaignMessages200JSONResponse(page), nil
}

// sendingService builds the data-plane service. Campaigns need ClickHouse for
// message rows, so a missing one means no send path rather than a partial one.
func (s *Server) sendingService(ctx context.Context) *sending.Service {
	clickhouse, err := s.clickhouse(ctx)
	if err != nil || s.Connector == nil {
		return nil
	}
	return &sending.Service{DB: s.DB, ClickHouse: clickhouse, Connector: s.Connector}
}
