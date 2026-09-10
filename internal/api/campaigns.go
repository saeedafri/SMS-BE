package api

import (
	"context"
	"errors"
	"fmt"

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
func (s *Server) toCampaign(campaign store.Campaign,
	counts store.CampaignCounts) gen.Campaign {

	out := gen.Campaign{
		Id:                    campaign.ID,
		Name:                  campaign.Name,
		Channel:               gen.ChannelId(campaign.Channel),
		Country:               gen.CountryCode(campaign.Country),
		SenderId:              campaign.SenderID.String(),
		TemplateId:            campaign.TemplateID.String(),
		Status:                gen.CampaignStatus(campaign.Status),
		Recipients:            campaign.Recipients,
		SegmentsPerMessageMin: campaign.SegmentsPerMessageMin,
		SegmentsPerMessageMax: campaign.SegmentsPerMessageMax,
		CostMinorMin:          int(campaign.CostMinorMin),
		CostMinorMax:          int(campaign.CostMinorMax),
		Currency:              gen.CurrencyCode(campaign.Currency),
		CreatedAt:             campaign.CreatedAt,
		ScheduledAt:           campaign.ScheduledAt,
		SendStartedAt:         campaign.SendStartedAt,
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

	// Always emitted, null when unset. The contract makes both required keys
	// with nullable values, so omitempty here would drop the key entirely and
	// break the generated client that expects it.
	out.PausedAt = campaign.PausedAt
	out.CancelledAt = campaign.CancelledAt

	out.Counts = gen.CampaignCounts{
		Queued: counts.Queued, Sent: counts.Sent,
		Delivered: counts.Delivered, Failed: counts.Failed,
		Rejected: counts.Rejected, Read: counts.Read,
		Cancelled: cancelledCount(campaign, counts),
	}
	out.Delivered = counts.Delivered
	out.Failed = counts.Failed
	return out
}

// campaignResponse renders one campaign, reading its own counts. The list has
// its own path because a page of twenty must not cost twenty reads.
func (s *Server) campaignResponse(ctx context.Context, identity store.Identity,
	campaign store.Campaign) gen.Campaign {

	counts := s.campaignCounts(ctx, identity, []store.Campaign{campaign})
	return s.toCampaign(campaign, counts[campaign.ID])
}

// campaignCounts reads the delivery breakdown for a whole page of campaigns.
//
// One query for the page rather than one per campaign. Rendering a page of
// twenty used to cost twenty ClickHouse round trips, which is invisible with
// one reader and fatal with a hundred: measured on production, /v1/campaigns
// fell from 565 requests a second at 16 concurrent readers to 2.2 at 128, while
// the same page over Postgres alone held 630.
//
// Best-effort, as before: if ClickHouse is unreachable the campaigns still
// render, just without their breakdowns. A 500 here would hide every campaign
// over a missing number.
func (s *Server) campaignCounts(ctx context.Context, identity store.Identity,
	campaigns []store.Campaign) map[uuid.UUID]store.CampaignCounts {

	conn, err := s.clickhouse(ctx)
	if err != nil {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(campaigns))
	for _, campaign := range campaigns {
		ids = append(ids, campaign.ID)
	}
	counts, err := store.CountCampaignMessagesForPage(ctx, conn, identity.TenantID, ids)
	if err != nil {
		// Dropped so the NEXT request redials rather than reusing a handle to a
		// server that has gone. Swallowing the error without this is how a
		// restarted ClickHouse used to need a restart of the API to notice.
		_ = s.clickhouseFailed(err)
		return nil
	}
	return counts
}

// cancelledCount is how many of a campaign's recipients never went anywhere.
//
// It is DERIVED, not stored, and that is a deliberate choice worth stating.
// Fan-out creates a message row when it reaches a recipient, so a campaign
// cancelled at 30,000 of 100,000 has 30,000 rows and the other 70,000 have no
// row at all — they were never dispatched, never charged, and never queued.
// Writing 70,000 rows to say so would add no information the subtraction does
// not already carry, and would make cancelling a large campaign an expensive
// write amplification at exactly the moment someone is trying to stop it.
//
// The number the funnel needs is therefore the remainder, and it is zero for
// every campaign that was not cancelled.
func cancelledCount(campaign store.Campaign, counts store.CampaignCounts) int {
	if campaign.Status != "cancelled" {
		return 0
	}
	recorded := counts.Queued + counts.Sent + counts.Delivered + counts.Failed +
		counts.Rejected + counts.Read
	if remaining := campaign.Recipients - recorded; remaining > 0 {
		return remaining
	}
	return 0
}

func (s *Server) ListCampaigns(ctx context.Context, request gen.ListCampaignsRequestObject) (gen.ListCampaignsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	page, ok2 := pageNumber(request.Params.Page)
	if !ok2 {
		return gen.ListCampaigns422JSONResponse(
			errorBody(codeValidation, pageTooLow)), nil
	}
	filter := store.CampaignFilter{Page: page, Search: searchTerm(request.Params.Q)}
	limit, limitOK := pageSize(request.Params.Limit)
	if !limitOK {
		return gen.ListCampaigns422JSONResponse(
			errorBody(codeValidation, limitOutOfRange)), nil
	}
	filter.Limit = limit
	if request.Params.Status != nil {
		value := string(*request.Params.Status)
		filter.Status = &value
	}
	if request.Params.Channel != nil {
		value := string(*request.Params.Channel)
		filter.Channel = &value
	}
	campaigns, total, err := store.ListCampaigns(ctx, s.DB, identity, filter)
	if err != nil {
		return nil, err
	}
	counts := s.campaignCounts(ctx, identity, campaigns)
	out := make([]gen.Campaign, 0, len(campaigns))
	for _, campaign := range campaigns {
		out = append(out, s.toCampaign(campaign, counts[campaign.ID]))
	}
	return gen.ListCampaigns200JSONResponse(gen.CampaignPage{
		Campaigns: out, Total: total,
	}), nil
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
	return gen.GetCampaign200JSONResponse(s.campaignResponse(ctx, identity, campaign)), nil
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
		templateCategory := ""
		if template.Category != nil {
			templateCategory = *template.Category
		}
		estimate, err := service.EstimateCampaign(ctx, identity, campaign.ListID,
			campaign.Country, campaign.Channel, templateBody, templateCategory)
		if err == nil {
			campaign.Recipients = estimate.Recipients
			campaign.SegmentsPerMessageMin = estimate.SegmentsPerMessageMin
			campaign.SegmentsPerMessageMax = estimate.SegmentsPerMessageMax
			campaign.CostMinorMin = estimate.CostMinorMin
			campaign.CostMinorMax = estimate.CostMinorMax
			campaign.Currency = estimate.Currency
		}
	}

	// Refuse the campaign if the wallet cannot cover the estimate.
	//
	// The contract declares 402 for exactly this and we never returned it, so a
	// tenant with an empty wallet got a 201 and a campaign that then failed
	// message by message on a balance check deep in the send path. The customer
	// saw a campaign that had "started" and was quietly failing, instead of one
	// sentence telling them to top up.
	//
	// Checked against the HIGH end of the estimate. Segment counts vary per
	// recipient, so clearing the low end only means a campaign can still run
	// dry halfway — and half a campaign is worse than none, because the half
	// that sent cannot be unsent.
	if campaign.CostMinorMax > 0 && campaign.Currency != "" {
		// Read wallet_balances, not the sum of the ledger.
		//
		// wallet_balances is the authoritative figure the send path itself
		// checks and the one the billing screen shows. Summing the ledger looks
		// equivalent and is not: anything that moves the balance without
		// writing history — including the dev drain hook the specs use — leaves
		// the two disagreeing, and a guard reading the wrong one would wave
		// through a campaign the send path then refuses message by message.
		balances, err := store.ListWalletBalances(ctx, s.DB, identity)
		if err != nil {
			return nil, err
		}
		var balance int64
		for _, wallet := range balances {
			if wallet.Currency == campaign.Currency {
				balance = wallet.BalanceMinor
				break
			}
		}
		if balance < campaign.CostMinorMax {
			// INSUFFICIENT_BALANCE, upper case. The contract does not fix the
			// casing, and the dashboard keys its "Top up wallet" link off this
			// exact string — a lower-case code produced a bare error message
			// with no way to act on it, which is the one thing this response
			// exists to provide.
			return gen.CreateCampaign402JSONResponse(errorBody("INSUFFICIENT_BALANCE",
				fmt.Sprintf("Insufficient %s balance for this campaign's estimated cost.",
					campaign.Currency))), nil
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

	return gen.CreateCampaign201JSONResponse(s.campaignResponse(ctx, identity, created)), nil
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
	templateCategory := ""
	if body.TemplateId != "" {
		templateID, err := uuid.Parse(body.TemplateId)
		if err != nil {
			return gen.EstimateCampaign422JSONResponse(errorBody(codeValidation, "templateId must be a uuid")), nil
		}
		template, err := store.GetTemplate(ctx, s.DB, identity, templateID)
		if err == nil {
			if template.Body != nil {
				templateBody = *template.Body
			}
			// The category is what Email, WhatsApp and Voice are actually
			// priced on, so an estimate that ignored it quoted the wrong rate
			// — or, where the corridor had no channel-level row at all, failed
			// outright.
			if template.Category != nil {
				templateCategory = *template.Category
			}
		}
	}

	estimate, err := service.EstimateCampaign(ctx, identity, listID,
		string(body.Country), string(body.Channel), templateBody, templateCategory)
	if errors.Is(err, sending.ErrNoRate) {
		return gen.EstimateCampaign422JSONResponse(errorBody(codeValidation,
			"We do not have a rate for that country and channel yet.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.EstimateCampaign200JSONResponse(gen.CampaignEstimate{
		Recipients:            estimate.Recipients,
		SegmentsPerMessageMin: estimate.SegmentsPerMessageMin,
		SegmentsPerMessageMax: estimate.SegmentsPerMessageMax,
		CostMinorMin:          int(estimate.CostMinorMin),
		CostMinorMax:          int(estimate.CostMinorMax),
		Currency:              gen.CurrencyCode(estimate.Currency),
	}), nil
}

func (s *Server) ListCampaignMessages(ctx context.Context, request gen.ListCampaignMessagesRequestObject) (gen.ListCampaignMessagesResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	campaignID := request.Id
	filter := store.MessageFilter{CampaignID: &campaignID, Limit: 50}
	page, ok := pageNumber(request.Params.Page)
	if !ok {
		return gen.ListCampaignMessages422JSONResponse(
			errorBody(codeValidation, pageTooLow)), nil
	}
	filter.Page = page
	limit, limitOK := pageSize(request.Params.Limit)
	if !limitOK {
		return gen.ListCampaignMessages422JSONResponse(
			errorBody(codeValidation, limitOutOfRange)), nil
	}
	filter.Limit = limit
	if request.Params.Status != nil {
		filter.Statuses = contractStatusToStates(string(*request.Params.Status))
	}

	// After the request is validated, for the reason ListMessages gives: a
	// caller that asked for limit=201 made a mistake it can fix, and reporting
	// an unreachable log store instead sends it to look at our infrastructure
	// for its own typo.
	clickhouse, err := s.clickhouse(ctx)
	if err != nil {
		return nil, err
	}

	records, total, err := store.QueryMessages(ctx, clickhouse, identity.TenantID, filter)
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

	result := gen.MessagePage{Messages: messages, Total: int(total)}
	return gen.ListCampaignMessages200JSONResponse(result), nil
}

// sendingService builds the data-plane service. Campaigns need ClickHouse for
// message rows, so a missing one means no send path rather than a partial one.
func (s *Server) sendingService(ctx context.Context) *sending.Service {
	service := s.rawSendingService(ctx)
	if service != nil {
		service.Coalescer = s.Sends
	}
	return service
}

// rawSendingService is the same service without the coalescer attached. The
// coalescer builds its per-batch service from this one: giving a batch's own
// service a coalescer would be a loop waiting to be written.
func (s *Server) rawSendingService(ctx context.Context) *sending.Service {
	clickhouse, err := s.clickhouse(ctx)
	if err != nil || s.Connector == nil {
		return nil
	}
	return &sending.Service{DB: s.DB, ClickHouse: clickhouse, Connector: s.Connector,
		Carriers: s.Carriers, Logger: s.Logger, Hot: s.Hot}
}

// StartSendCoalescer turns on batching for the transactional send API. Called
// once at startup; until it is, every send takes its own round trips, which is
// correct and slower.
func (s *Server) StartSendCoalescer() {
	s.Sends = sending.NewCoalescer(s.rawSendingService)
	s.Sends.Start()
}

// StopSendCoalescer drains the batches in flight. Sends still queued are
// answered with an error rather than left waiting for a reply that is not
// coming.
func (s *Server) StopSendCoalescer() {
	if s.Sends != nil {
		s.Sends.Stop()
	}
}

// ListCampaignRecipients names a campaign's recipients: the ones it reached,
// the ones a cancellation caught, or both.
//
// THE TWO HALVES COME FROM DIFFERENT PLACES ON PURPOSE, and the reason is the
// difference between a record and a projection.
//
// A dispatched recipient is READ from the message log — the row it became is
// the evidence it was reached, and nothing that happens afterwards revises it.
// A cancelled recipient has no row by design, so it is DERIVED from the
// audience list and the dispatch cursor, and it carries the full audience rule
// because it answers a counterfactual: who this run would have reached.
//
// Deriving both halves the same way was wrong in a way worth recording. The
// audience rule includes consent, consent is mutable, and the derivation reads
// it as it stands now — so a contact who opted out after being messaged
// vanished from the campaign's own account of what it sent, and a campaign that
// ran before the consent rule existed reported 0 recipients against 2,500
// message rows. A compliance question is answered by what was sent.
//
// The two blocks are contiguous — dispatched first, then cancelled — so
// `total` is the sum and a walk of every page sees each recipient once.
// defaultRecipientPage is this route's page size when the caller names none.
const defaultRecipientPage = 20

func (s *Server) ListCampaignRecipients(ctx context.Context, request gen.ListCampaignRecipientsRequestObject) (
	gen.ListCampaignRecipientsResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	page, ok2 := pageNumber(request.Params.Page)
	if !ok2 {
		return gen.ListCampaignRecipients422JSONResponse(
			errorBody(codeValidation, pageTooLow)), nil
	}
	limit, limitOK := pageSize(request.Params.Limit)
	if !limitOK {
		return gen.ListCampaignRecipients422JSONResponse(
			errorBody(codeValidation, limitOutOfRange)), nil
	}
	if limit == 0 {
		limit = defaultRecipientPage
	}
	// This handler splits one page across two stores, so it has to know how
	// wide the page is rather than leaving the default to either of them.
	offset := (page - 1) * limit

	campaign, err := store.GetCampaign(ctx, s.DB, identity, request.Id)
	// A 404 rather than an empty page: "no such campaign" and "a campaign whose
	// filter matched nobody" are different answers and used to be the same one.
	if errors.Is(err, store.ErrNotFound) {
		return gen.ListCampaignRecipients404JSONResponse(errorBody(codeNotFound,
			"No campaign with that id.")), nil
	}
	if err != nil {
		return nil, err
	}
	state := ""
	if request.Params.State != nil {
		state = string(*request.Params.State)
	}

	out := []gen.CampaignRecipient{}
	total := 0

	if state != "cancelled" {
		rows, dispatchedTotal, err := s.dispatchedRecipients(ctx, identity, campaign, offset, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		total += dispatchedTotal
		// The cancelled block starts where the dispatched one ends, so its own
		// offset is whatever is left of the request after that block, and it is
		// asked only for the rows this page still has room for. A limit of 0
		// asks for the total alone, which the envelope needs even on a page the
		// dispatched half filled completely.
		offset -= dispatchedTotal
		limit -= len(rows)
	}

	if state != "dispatched" {
		cancelled, cancelledTotal, err := store.ListCancelledRecipients(ctx, s.DB, identity,
			campaign, offset, limit)
		if err != nil {
			return nil, err
		}
		for _, recipient := range cancelled {
			out = append(out, gen.CampaignRecipient{
				ContactId: recipient.ContactID,
				Identity:  recipient.Identity,
				State:     gen.CampaignRecipientStateCancelled,
			})
		}
		total += cancelledTotal
	}

	return gen.ListCampaignRecipients200JSONResponse(gen.CampaignRecipientPage{
		Recipients: out, Total: total,
	}), nil
}

// dispatchedRecipients reads one page of the message log and puts the contact
// each address belongs to back on it.
//
// A message whose contact has since gone carries a nil contact id rather than
// being dropped: the send happened, and losing the row would understate what
// the campaign did on the screen whose job is to say so.
func (s *Server) dispatchedRecipients(ctx context.Context, identity store.Identity,
	campaign store.Campaign, offset, limit int) ([]gen.CampaignRecipient, int, error) {

	// No fallback when the log is unreachable. What a campaign dispatched is a
	// question only the log can answer, and answering it with an empty set
	// would be a lie the caller cannot detect.
	conn, err := s.clickhouse(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, total, err := store.DispatchedRecipients(ctx, conn, identity.TenantID,
		campaign.ID, offset, limit)
	if err != nil {
		return nil, 0, s.clickhouseFailed(err)
	}
	identities := make([]string, 0, len(rows))
	for _, row := range rows {
		identities = append(identities, row.Identity)
	}
	contactIDs, err := store.ContactIDsForIdentities(ctx, s.DB, identity, identities)
	if err != nil {
		return nil, 0, err
	}
	out := make([]gen.CampaignRecipient, 0, len(rows))
	for _, row := range rows {
		messageID := row.MessageID
		out = append(out, gen.CampaignRecipient{
			ContactId: contactIDs[row.Identity],
			Identity:  row.Identity,
			State:     gen.CampaignRecipientStateDispatched,
			MessageId: &messageID,
		})
	}
	return out, total, nil
}
