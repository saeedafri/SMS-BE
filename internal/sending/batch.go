package sending

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/domain/audience"
	"github.com/saeedafri/sms-be/internal/domain/billing"
	"github.com/saeedafri/sms-be/internal/domain/messaging"
	"github.com/saeedafri/sms-be/internal/store"
)

// batchSize is how many recipients are processed per round trip. 500 keeps the
// ClickHouse insert comfortably large while bounding how much money one wallet
// hold covers, so a crash mid-campaign strands at most this many messages'
// worth of held funds.
const batchSize = 500

// sendPlan is one recipient's resolved, priced, gate-checked state. Building
// the whole page's plans before touching the database is what makes a single
// batched write possible.
type sendPlan struct {
	messageID uuid.UUID
	msisdn    string
	// email is the recipient address for channels addressed by email rather
	// than by phone number. Recorded alongside msisdn rather than instead of
	// it: a contact has both, and the logs explorer and campaign detail show
	// whichever one the message was actually sent to.
	email    string
	body     string
	cost     int64
	segments int
	// refusal is set when the gate refused this recipient. Refused messages
	// are still recorded — a tenant asking "why didn't this arrive" deserves an
	// answer — but no money is held for them.
	refusal string
}

// SendBatch runs a whole page of recipients through the pipeline with a fixed
// number of round trips instead of a fixed number PER MESSAGE.
//
// The unbatched path costs about 8 Postgres queries and 6 ClickHouse inserts
// for every single message. Measured on this machine that caps out near 68
// messages/second, which is 5.9M/day — far short of the target. Almost all of
// that work is identical for every recipient in a campaign: the same sender,
// template, rate and tenant status, resolved once here instead of N times.
//
// What is deliberately NOT batched is correctness: every recipient still goes
// through the same gate with the same rules, and the money still moves before
// anything reaches a carrier.
func (s *Service) SendBatch(ctx context.Context, identity store.Identity,
	context batchContext, contacts []store.Contact) (sent int, failed int, err error) {

	if len(contacts) == 0 {
		return 0, 0, nil
	}

	// One suppression query for the whole page rather than one per recipient.
	identities := make([]string, 0, len(contacts))
	normalised := make([]string, len(contacts))
	for i, contact := range contacts {
		msisdn, valid := audience.NormaliseMsisdn(contact.Msisdn, context.sender.Country)
		if !valid {
			msisdn, _ = audience.NormaliseE164(contact.Msisdn)
		}
		normalised[i] = msisdn
		identities = append(identities, msisdn)
	}
	suppressed, err := store.SuppressedSet(ctx, s.DB, identity, identities)
	if err != nil {
		return 0, 0, err
	}

	now := time.Now().UTC()
	plans := make([]sendPlan, 0, len(contacts))
	var holdTotal int64

	for i, contact := range contacts {
		msisdn := normalised[i]

		// Contacts this channel cannot reach are skipped outright, not recorded
		// as failures.
		//
		// A contact with no email address is not a failed Email delivery — they
		// were never in the audience. Recording them would charge nothing but
		// would wreck the two numbers the customer reads: a campaign to 40
		// reachable contacts out of 1,000 would report a 4% delivery rate, and
		// the message list would show phone numbers in the recipient column of
		// an email campaign. This matches the estimate they approved, which
		// counts the same set — see store.ReachableOnChannel.
		if !recipientAddressable(context.sender.Channel, msisdn, contactEmail(contact)) {
			continue
		}

		body := personalise(context.body, contact)
		segments := billing.SegmentCount(body)
		cost := int64(segments) * context.rate.PerSegmentMinor

		email := contactEmail(contact)
		plan := sendPlan{
			messageID: uuid.New(), msisdn: msisdn, email: email, body: body,
			cost: cost, segments: segments,
		}

		gateErr := messaging.Check(messaging.GateInput{
			TenantStatus: context.tenantStatus, SenderStatus: context.sender.Status,
			SenderID: context.sender.ID.String(), TemplateStatus: context.templateStatus,
			TemplateSender: context.templateSender, Suppressed: suppressed[msisdn],
			// The balance check uses the running total for this batch, so a
			// wallet that runs dry mid-page refuses the rest instead of going
			// negative.
			BalanceMinor: context.balance - holdTotal, CostMinor: cost,
			// Valid means addressable ON THIS CHANNEL. An Email send to a
			// contact with no email address is not a delivery failure to be
			// retried, it is a message that was never sendable — and charging
			// for it, or counting it against the delivery rate, would be wrong
			// in both directions.
			RecipientValid: recipientAddressable(context.sender.Channel, msisdn, email),
		})
		if gateErr != nil {
			plan.refusal = messaging.GateFailureCode(gateErr)
			plans = append(plans, plan)
			continue
		}
		holdTotal += cost
		plans = append(plans, plan)
	}

	// ONE wallet movement for the page. The per-message path took a row lock
	// per message, which serialised the entire campaign behind a single row —
	// the dominant cost at scale.
	if holdTotal > 0 {
		if _, err := store.AppendLedgerEntry(ctx, s.DB, identity, store.LedgerEntry{
			Currency: context.rate.Currency, Type: "charge", AmountMinor: holdTotal,
			Description: fmt.Sprintf("Campaign hold (%d messages)", len(plans)),
			CampaignID:  context.campaignID,
		}); err != nil {
			return 0, 0, err
		}
	}

	// Record everything as queued in one write, then submit.
	records := make([]store.MessageRecord, 0, len(plans))
	events := make([]store.MessageEvent, 0, len(plans))
	rollups := make([]store.RollupRow, 0, len(plans))
	submissions := make([]connector.Submission, 0, len(plans))

	for _, plan := range plans {
		state := messaging.StateQueued
		cost := plan.cost
		var errorCode *string
		if plan.refusal != "" {
			state = messaging.StateRejected
			cost = 0
			code := plan.refusal
			errorCode = &code
			failed++
		} else {
			submissions = append(submissions, connector.Submission{
				MessageID: plan.messageID.String(), Msisdn: plan.msisdn,
				Sender: context.sender.Header, Body: plan.body,
				Channel: context.sender.Channel, Country: context.sender.Country,
			})
		}
		record := store.MessageRecord{
			TenantID: identity.TenantID, ID: plan.messageID,
			Channel: context.sender.Channel, Country: context.sender.Country,
			SenderHeader: context.sender.Header, TemplateID: &context.templateID,
			Msisdn: plan.msisdn, Email: plan.emailForChannel(context.sender.Channel),
			Status: string(state), ErrorCode: errorCode,
			FraudFlag: "none", Segments: uint8(plan.segments), CostMinor: cost,
			Currency: context.rate.Currency, CampaignID: context.campaignID,
			Carrier: context.carrier, RouteID: context.routeID,
			CreatedAt: now, UpdatedAt: now, Version: 1,
		}
		records = append(records, record)
		events = append(events, store.MessageEvent{
			TenantID: identity.TenantID, MessageID: plan.messageID,
			ToState: string(state), ErrorCode: errorCode, OccurredAt: now,
		})
		rollups = append(rollups, store.RollupRow{
			TenantID: identity.TenantID, Hour: now.Truncate(time.Hour),
			Channel: context.sender.Channel, Country: context.sender.Country,
			Status: string(state), MessageCount: 1,
			SegmentCount: uint64(plan.segments), CostMinor: cost,
			Currency: context.rate.Currency,
		})
	}

	if err := s.writeBatch(ctx, records, events, rollups); err != nil {
		return 0, failed, err
	}

	if len(submissions) == 0 {
		return 0, failed, nil
	}

	receipts, err := s.Connector.Submit(ctx, submissions)
	if err != nil {
		return 0, failed, fmt.Errorf("sending: submit batch: %w", err)
	}

	// Apply the receipts as a second batched write.
	byID := make(map[string]connector.Receipt, len(receipts))
	for _, receipt := range receipts {
		byID[receipt.MessageID] = receipt
	}

	records = records[:0]
	events = events[:0]
	rollups = rollups[:0]
	var releaseTotal int64
	settled := time.Now().UTC()

	for _, plan := range plans {
		if plan.refusal != "" {
			continue
		}
		receipt, found := byID[plan.messageID.String()]
		state := messaging.StateAccepted
		cost := plan.cost
		var errorCode *string
		var errorClass *string
		var carrierRef *string

		if !found || !receipt.Accepted {
			state = messaging.StateRejected
			cost = 0
			code := "SUBMIT_FAILED"
			if found {
				code = receipt.ErrorCode
			}
			class, _ := messaging.ClassifyCarrierError(code)
			classValue := string(class)
			errorCode, errorClass = &code, &classValue
			// A carrier refusal releases the hold immediately: nothing was
			// delivered, so nothing is owed.
			releaseTotal += plan.cost
			failed++
		} else {
			ref := receipt.CarrierRef
			carrierRef = &ref
			sent++
		}

		records = append(records, store.MessageRecord{
			TenantID: identity.TenantID, ID: plan.messageID,
			Channel: context.sender.Channel, Country: context.sender.Country,
			SenderHeader: context.sender.Header, TemplateID: &context.templateID,
			Msisdn: plan.msisdn, Email: plan.emailForChannel(context.sender.Channel),
			Status: string(state), ErrorCode: errorCode,
			ErrorClass: errorClass, FraudFlag: "none", Segments: uint8(plan.segments),
			CostMinor: cost, Currency: context.rate.Currency,
			CampaignID: context.campaignID, CarrierRef: carrierRef,
			Carrier: context.carrier, RouteID: context.routeID,
			CreatedAt: now, SentAt: &settled, UpdatedAt: settled, Version: 2,
		})
		events = append(events, store.MessageEvent{
			TenantID: identity.TenantID, MessageID: plan.messageID,
			FromState: string(messaging.StateQueued), ToState: string(state),
			ErrorCode: errorCode, OccurredAt: settled,
		})
		rollups = append(rollups, store.RollupRow{
			TenantID: identity.TenantID, Hour: settled.Truncate(time.Hour),
			Channel: context.sender.Channel, Country: context.sender.Country,
			Status: string(state), MessageCount: 1,
			SegmentCount: uint64(plan.segments), CostMinor: cost,
			Currency: context.rate.Currency,
		})
	}

	if releaseTotal > 0 {
		if _, err := store.AppendLedgerEntry(ctx, s.DB, identity, store.LedgerEntry{
			Currency: context.rate.Currency, Type: "refund", AmountMinor: releaseTotal,
			Description: "Released holds for carrier-rejected messages",
			CampaignID:  context.campaignID,
		}); err != nil {
			return sent, failed, err
		}
	}

	return sent, failed, s.writeBatch(ctx, records, events, rollups)
}

// writeBatch performs the three ClickHouse writes for a page. Three round
// trips per 500 messages instead of six per message.
func (s *Service) writeBatch(ctx context.Context, records []store.MessageRecord,
	events []store.MessageEvent, rollups []store.RollupRow) error {

	if err := store.InsertMessages(ctx, s.ClickHouse, records); err != nil {
		return err
	}
	if err := store.InsertMessageEvents(ctx, s.ClickHouse, events); err != nil {
		return err
	}
	return store.InsertRollups(ctx, s.ClickHouse, rollups)
}

// batchContext is everything identical across a campaign's recipients,
// resolved once instead of per message.
type batchContext struct {
	sender         store.SenderID
	templateID     uuid.UUID
	templateStatus string
	templateSender string
	body           string
	rate           store.PricingRate
	tenantStatus   string
	balance        int64
	campaignID     *uuid.UUID
	// carrier and routeID are the path this campaign takes, resolved ONCE with
	// everything else that is identical across recipients. Empty when the
	// corridor has no active route, which is normal — Email and WhatsApp do not
	// go over a carrier at all.
	carrier string
	routeID *string
}

// emailForChannel returns the address to record as the recipient, and only for
// channels that are addressed by one.
//
// Every other channel keeps this nil so the read side falls back to the msisdn:
// an SMS row carrying an email address would be a lie about where the message
// went, even when the contact happens to have both.
func (p sendPlan) emailForChannel(channel string) *string {
	if channel != "EMAIL" || p.email == "" {
		return nil
	}
	return &p.email
}

// recipientAddressable reports whether a contact carries the identity this
// channel is addressed by. Email needs an address; every other channel needs a
// number.
func recipientAddressable(channel, msisdn, email string) bool {
	if channel == "EMAIL" {
		return email != ""
	}
	return msisdn != ""
}

// contactEmail flattens the optional address to a plain string, so the
// addressability checks above read the same for both identity kinds.
func contactEmail(contact store.Contact) string {
	if contact.Email == nil {
		return ""
	}
	return *contact.Email
}
