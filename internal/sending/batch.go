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
	// message is this recipient's whole message, filled: the body above, the
	// rich document, and the template's declared slots resolved for them. Kept
	// so an RCS submission can fill the CARRIER's template with the same values
	// that personalised the body — without them a campaign renders "Hi Priya"
	// in the log and sends the handset "Hi ", because the carrier holds the
	// template and fills the slots from what we pass, not from the body.
	message renderedMessage
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
//
// spent is the money this page actually took from the wallet: what was held,
// less what was released for messages that never went out. The caller spends
// its running balance down by THIS rather than re-deriving it — re-deriving it
// as messages x per-segment rate under-counted every multi-segment SMS, and
// let a campaign's running balance drift further from the ledger's each page.
func (s *Service) SendBatch(ctx context.Context, identity store.Identity,
	context batchContext, contacts []store.Contact) (sent int, failed int, spent int64, err error) {

	if len(contacts) == 0 {
		return 0, 0, 0, nil
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
		return 0, 0, 0, err
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

		// A contact the template cannot be personalised for is SKIPPED: no
		// message, no charge, counted. Third gate, after addressability and
		// before the send gate — somebody already excluded for opting out must
		// not ALSO be counted as missing a first name, which would explain one
		// person twice in two different numbers.
		//
		// One walk over the WHOLE message, not just its body: an RCS template
		// leaves body null on purpose and keeps its words in a card, so reading
		// the body alone found no slots and skipped nobody on exactly the
		// channel where the carrier renders from what we pass.
		message := renderMessage(context.template, context.body, contact.Fields,
			context.variableMapping)
		if len(message.missing) > 0 {
			context.skipped.add(message.missing)
			continue
		}
		body := message.body
		segments := billing.SegmentCount(body)
		cost := int64(segments) * context.rate.PerSegmentMinor

		email := contactEmail(contact)
		plan := sendPlan{
			messageID: uuid.New(), msisdn: msisdn, email: email, body: body,
			cost: cost, segments: segments, message: message,
		}

		dndBlocked, dndUnavailable := s.dndStatus(ctx, context.sender.Channel, msisdn, context.template)
		gateErr := messaging.Check(messaging.GateInput{
			TenantStatus: context.tenantStatus, SenderStatus: context.sender.Status,
			SenderID: context.sender.ID.String(), TemplateStatus: context.templateStatus,
			TemplateSender: context.templateSender, Suppressed: suppressed[msisdn],
			// The carrier's own approval, which is separate from ours. Checked
			// per recipient like everything else in the gate, even though it is
			// identical across the page — the gate's whole value is that no
			// message skips a rule because it looked the same as its
			// neighbour's.
			CarrierTemplateStatus: s.carrierTemplateStatusFor(
				context.sender.Channel, context.rcsCarrier, context.template),
			// A campaign's body IS the template, personalised, so this passes by
			// construction — which is the point. The same rule applies to every
			// dispatch path, and a campaign that somehow sent text unrelated to
			// its own template would be refused here too.
			RegisteredTemplateRequired: RegisteredTemplateRequired(context.sender.Country),
			OutsidePromotionalWindow:   s.outsidePromotionalWindow(context.sender.Country, context.template),
			DNDBlocked:                 dndBlocked,
			DNDCheckUnavailable:        dndUnavailable,
			TemplateBody:               templateBody(context.template),
			Body:                       body,
			// The dispatch invariant, and the last thing standing between an
			// unfilled {{token}} and a handset. The skip above already refused
			// this recipient, so in a working system it never fires — it reads
			// the rendered strings rather than that skip's own verdict, because
			// a bug in the fill is what it is here to catch.
			UnresolvedVariables: message.unresolved(),
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
			// The brand the handset draws. Refused here rather than at the
			// carrier so a campaign with no identity to send under costs
			// nothing instead of taking and releasing a hold per recipient.
			RCSAgentRequired: context.sender.Channel == "RCS" && context.rcsCarrier != "",
			RCSAgentResolved: context.agentID != "",
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
			Description: fmt.Sprintf("%s hold (%d messages)", context.holdKind(), len(plans)),
			CampaignID:  context.campaignID,
			JourneyID:   context.journeyID, JourneyName: context.journeyName,
		}); err != nil {
			return 0, 0, 0, err
		}
	}

	// Record everything as queued in one write, then submit.
	records := make([]store.MessageRecord, 0, len(plans))
	events := make([]store.MessageEvent, 0, len(plans))
	rollups := make([]store.RollupRow, 0, len(plans))
	submissions := make([]connector.Submission, 0, len(plans))

	// Once per batch: every message in it shares a tenant, sender and template.
	entityID, dltTemplateID := s.dltIDs(ctx, identity, context.sender.Channel,
		context.sender.Country, context.template)
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
				Sender:  context.sender.Header,
				Body:    plan.message.text(context.sender.Channel),
				Channel: context.sender.Channel, Country: context.sender.Country,
				Carrier:           context.carrier,
				Promotional:       context.template.DltCategory != nil && *context.template.DltCategory == "PROMOTIONAL",
				DLTEntityID:       entityID,
				DLTTemplateID:     dltTemplateID,
				CarrierTemplateID: context.carrierTemplateID(),
				AgentID:           context.agentID,
				// The SAME contact fields that personalised the body above.
				// The carrier holds the template and renders it from these, so
				// a campaign that personalises its body and sends the carrier
				// nothing would put "Hi Priya" in our log and "Hi " on the
				// handset.
				TemplateVariables: TemplateVariables(context.template, plan.message.variables),
			})
		}
		record := store.MessageRecord{
			TenantID: identity.TenantID, ID: plan.messageID,
			Channel: context.recordedChannel(), Country: context.sender.Country,
			SenderHeader: context.sender.Header, TemplateID: &context.templateID,
			Msisdn: plan.msisdn, Email: plan.emailForChannel(context.sender.Channel),
			Status: string(state), ErrorCode: errorCode,
			FraudFlag: "none", Segments: uint8(plan.segments), CostMinor: cost,
			Currency: context.rate.Currency, CampaignID: context.campaignID,
			JourneyID: context.journeyID, JourneyName: context.journeyName,
			Carrier: context.carrier, RouteID: context.routeID,
			DeliveredChannel: carriedBy(context.sender.Channel, plan.refusal != ""),
			CreatedAt:        now, UpdatedAt: now, Version: 1,
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
		return 0, failed, holdTotal, err
	}

	if len(submissions) == 0 {
		return 0, failed, holdTotal, nil
	}

	receipts, err := s.carrierFor(context.sender.Channel).Submit(ctx, submissions)
	if err != nil {
		return 0, failed, holdTotal, fmt.Errorf("sending: submit batch: %w", err)
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
		outcome := outcomeOf(receipt, found)
		if outcome.pending {
			// Still queued with its hold; the bind reports it later.
			continue
		}
		state, cost, carrierRef := outcome.state, plan.cost, outcome.ref
		errorCode, errorClass := outcome.codes()
		if outcome.release {
			// Nothing was delivered, so nothing is owed.
			cost = 0
			releaseTotal += plan.cost
			failed++
		} else {
			sent++
		}

		records = append(records, store.MessageRecord{
			TenantID: identity.TenantID, ID: plan.messageID,
			Channel: context.recordedChannel(), Country: context.sender.Country,
			SenderHeader: context.sender.Header, TemplateID: &context.templateID,
			Msisdn: plan.msisdn, Email: plan.emailForChannel(context.sender.Channel),
			Status: string(state), ErrorCode: errorCode,
			ErrorClass: errorClass, FraudFlag: "none", Segments: uint8(plan.segments),
			CostMinor: cost, Currency: context.rate.Currency,
			CampaignID: context.campaignID, CarrierRef: carrierRef,
			JourneyID: context.journeyID, JourneyName: context.journeyName,
			Carrier: context.carrier, RouteID: context.routeID,
			DeliveredChannel: carriedBy(context.sender.Channel, false),
			CreatedAt:        now, SentAt: &settled, UpdatedAt: settled, Version: 2,
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
			Description: "Released holds for messages that were not sent",
			CampaignID:  context.campaignID,
			JourneyID:   context.journeyID, JourneyName: context.journeyName,
		}); err != nil {
			// The refund did not land, so the whole hold is still out of the wallet.
			return sent, failed, holdTotal, err
		}
	}

	return sent, failed, holdTotal - releaseTotal, s.writeBatch(ctx, records, events, rollups)
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
	// template carries the carrier's registration — its id and its separate
	// approval. Resolved once with everything else identical across recipients.
	template store.Template
	body     string
	// variableMapping joins a template's slots to the customer's own column
	// names. It belongs to the LIST, and the fan-out is handed a list id, which
	// is precisely why the skip has to live here: only the code that walks the
	// list can decide who it cannot personalise.
	variableMapping map[string]string
	// skipped accumulates who could not be personalised, across every page of
	// the fan-out. A pointer because batchContext is copied per page and the
	// campaign wants one total; nil on the single-send path, which has no list
	// to walk and refuses at the gate instead.
	skipped      *skipTally
	rate         store.PricingRate
	tenantStatus string
	balance      int64
	campaignID   *uuid.UUID
	// journeyID and journeyName tag a journey send step's messages. Nil on
	// every campaign leg: the two are never both set.
	journeyID   *uuid.UUID
	journeyName *string
	// carrier and routeID are the path this campaign takes, resolved ONCE with
	// everything else that is identical across recipients. Empty when the
	// corridor has no active route, which is normal — Email and WhatsApp do not
	// go over a carrier at all.
	carrier string
	routeID *string

	// rcsCarrier is the gateway RCS has to itself, empty when there is none,
	// and agentID the brand this campaign's sender goes out under on it. Both
	// resolved once: a campaign has one sender, so every message in it carries
	// the same identity.
	rcsCarrier string
	agentID    string

	// campaignChannel is the campaign's OWN channel, which every row it writes
	// is filed under. It differs from sender.Channel only on the fallback leg:
	// a recipient carried by SMS in an RCS campaign is still that campaign's
	// message, and the row says so in `channel` while delivered_channel says
	// what actually carried it. Rollups stay on sender.Channel, because the
	// cost and segments belong to the leg that sent them.
	campaignChannel string
}

// holdKind names what a wallet hold was taken for.
//
// Not decoration. The wallet screen renders this description as the entry's own
// label whenever there is no journey name to link instead, so the hardcoded
// "Campaign hold" this replaces was a journey send telling the customer it was
// a campaign — a false sentence on the screen about their money, which is a
// different class of wrong from a missing id.
//
// journeyID and campaignID are never both set, so the branch is the whole
// question. A leg with neither is the single-send path, which does not take a
// page hold at all.
func (c batchContext) holdKind() string {
	if c.journeyID != nil {
		return "Journey"
	}
	return "Campaign"
}

// recordedChannel is the channel a message row is filed under.
func (c batchContext) recordedChannel() string {
	if c.campaignChannel != "" {
		return c.campaignChannel
	}
	return c.sender.Channel
}

// carriedBy is the delivered_channel a row records: the leg's own channel once
// the message is dispatched, and nothing for a refusal that never left.
func carriedBy(channel string, refused bool) *string {
	if refused {
		return nil
	}
	return &channel
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

// carrierTemplateID is the id the carrier issued for this campaign's template,
// empty when it has none.
func (c batchContext) carrierTemplateID() string {
	if c.template.CarrierTemplateID == nil {
		return ""
	}
	return *c.template.CarrierTemplateID
}

// skipTally counts the contacts a campaign could not personalise, and why.
//
// "12 skipped" is a fact; "12 have no first name" is an instruction — so the
// names are kept, not just the number. A contact missing two slots counts once
// in Total and once under each name, because Total counts people and the names
// explain them.
type skipTally struct {
	Total  int
	ByName map[string]int
}

func (t *skipTally) add(missing []string) {
	if t == nil {
		return
	}
	t.Total++
	if t.ByName == nil {
		t.ByName = map[string]int{}
	}
	for _, slot := range missing {
		t.ByName[slot]++
	}
}
