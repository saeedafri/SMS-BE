package api

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/billing"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func invoiceResponse(inv store.Invoice) gen.Invoice {
	items := make([]gen.InvoiceLineItem, 0, len(inv.LineItems))
	for _, item := range inv.LineItems {
		line := gen.InvoiceLineItem{
			CampaignId:   item.CampaignID,
			CampaignName: item.CampaignName,
			JourneyId:    item.JourneyID,
			JourneyName:  item.JourneyName,
			AmountMinor:  int(item.AmountMinor),
			CreatedAt:    item.CreatedAt,
		}
		// Nullable oneOf in the contract, so a generated union rather than a
		// plain enum.
		var channel gen.InvoiceLineItem_Channel
		_ = channel.FromChannelId(gen.ChannelId(item.Channel))
		line.Channel = &channel
		items = append(items, line)
	}
	return gen.Invoice{
		Id:             inv.ID.String(),
		Currency:       gen.CurrencyCode(inv.Currency),
		PeriodStart:    inv.PeriodStart,
		PeriodEnd:      inv.PeriodEnd,
		Status:         gen.InvoiceStatus(inv.Status),
		SubtotalMinor:  int(inv.SubtotalMinor),
		TaxRatePercent: inv.TaxRatePercent,
		TaxMinor:       int(inv.TaxMinor),
		TotalMinor:     int(inv.TotalMinor),
		LineItems:      items,
	}
}

func (s *Server) ListInvoices(ctx context.Context, request gen.ListInvoicesRequestObject) (gen.ListInvoicesResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.ListInvoices401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if !canManageSettings(identity.Role) {
		return gen.ListInvoices403JSONResponse(
			errorBody(codeForbidden, "Member role has no access to billing.")), nil
	}

	cursor, limit := "", 50
	if request.Params.Cursor != nil {
		cursor = *request.Params.Cursor
	}
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}

	invoices, next, err := store.ListInvoices(ctx, s.DB, identity, cursor, limit)
	if errors.Is(err, store.ErrInvalidCursor) {
		return gen.ListInvoices401JSONResponse(
			errorBody(codeValidation, "That page cursor is not valid.")), nil
	}
	if err != nil {
		return nil, err
	}

	out := make([]gen.Invoice, 0, len(invoices))
	for _, invoice := range invoices {
		out = append(out, invoiceResponse(invoice))
	}
	page := gen.InvoicePage{Invoices: out}
	if next != "" {
		page.NextCursor = &next
	}
	return gen.ListInvoices200JSONResponse(page), nil
}

func (s *Server) GetInvoice(ctx context.Context, request gen.GetInvoiceRequestObject) (gen.GetInvoiceResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.GetInvoice401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if !canManageSettings(identity.Role) {
		return gen.GetInvoice403JSONResponse(
			errorBody(codeForbidden, "Member role has no access to billing.")), nil
	}
	invoiceID, err := uuid.Parse(request.Id)
	if err != nil {
		return gen.GetInvoice404JSONResponse(errorBody(codeNotFound, "No such invoice.")), nil
	}
	invoice, err := store.GetInvoice(ctx, s.DB, identity, invoiceID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.GetInvoice404JSONResponse(errorBody(codeNotFound, "No such invoice.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetInvoice200JSONResponse(invoiceResponse(invoice)), nil
}

// GetUsage reports spend groupings.
//
// Channel totals come from the wallet, campaign and journey attribution from
// the message rows: the ledger knows money moved, but only a message knows what
// moved it. ClickHouse being unavailable costs the two attributed sections and
// leaves the channel totals intact, rather than failing the whole screen.
func (s *Server) GetUsage(ctx context.Context, request gen.GetUsageRequestObject) (gen.GetUsageResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.GetUsage401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}

	currency := ""
	if request.Params.Currency != nil {
		currency = string(*request.Params.Currency)
	}
	// The contract declares `range` on this endpoint and it was read by nothing,
	// so every window returned the same all-time numbers. Same default as
	// /v1/analytics: absent means 30d.
	since := rangeSince("30d")
	if request.Params.Range != nil {
		since = rangeSince(string(*request.Params.Range))
	}

	byChannel, err := store.UsageByChannel(ctx, s.DB, identity, currency)
	if err != nil {
		return nil, err
	}

	channels := make([]gen.UsageByChannel, 0, len(byChannel))
	for _, usage := range byChannel {
		channels = append(channels, gen.UsageByChannel{
			Channel:      gen.ChannelId(usage.Channel),
			Currency:     gen.CurrencyCode(usage.Currency),
			MessageCount: usage.MessageCount,
			AmountMinor:  int(usage.AmountMinor),
		})
	}
	campaigns := []gen.UsageByCampaign{}
	journeys := []gen.UsageByJourney{}
	if clickhouse, chErr := s.clickhouse(ctx); chErr == nil {
		byCampaign, err := store.UsageByCampaign(ctx, clickhouse, identity.TenantID, since, currency)
		if err != nil {
			return nil, s.clickhouseFailed(err)
		}
		for _, row := range byCampaign {
			id, parseErr := uuid.Parse(row.ID)
			if parseErr != nil {
				continue
			}
			campaigns = append(campaigns, gen.UsageByCampaign{
				CampaignId: id.String(), CampaignName: row.Name,
				Channel:      gen.ChannelId(row.Channel),
				Currency:     gen.CurrencyCode(row.Currency),
				MessageCount: row.MessageCount,
				AmountMinor:  int(row.Amount),
			})
		}
		byJourney, err := store.UsageByJourney(ctx, clickhouse, identity.TenantID, since, currency)
		if err != nil {
			return nil, s.clickhouseFailed(err)
		}
		for _, row := range byJourney {
			id, parseErr := uuid.Parse(row.ID)
			if parseErr != nil {
				continue
			}
			journeys = append(journeys, gen.UsageByJourney{
				JourneyId: id.String(), JourneyName: row.Name,
				Channel:      gen.ChannelId(row.Channel),
				Currency:     gen.CurrencyCode(row.Currency),
				MessageCount: row.MessageCount,
				AmountMinor:  int(row.Amount),
			})
		}
	}

	return gen.GetUsage200JSONResponse(gen.UsageReport{
		ByChannel:  channels,
		ByCampaign: campaigns,
		ByJourney:  journeys,
	}), nil
}

var _ = billing.ErrCaptureDeclined
