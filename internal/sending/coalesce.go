package sending

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/domain/audience"
	"github.com/saeedafri/sms-be/internal/domain/billing"
	"github.com/saeedafri/sms-be/internal/domain/compliance"
	"github.com/saeedafri/sms-be/internal/domain/messaging"
	"github.com/saeedafri/sms-be/internal/store"
)

// The transactional send API pays per message what a campaign pays per five
// hundred.
//
// SendBatch already proved where the throughput is: one suppression query, one
// wallet movement, one carrier call and three ClickHouse writes for a whole
// page, instead of that many round trips for every recipient. It could only do
// that for campaigns, because a campaign is one sender and one body repeated —
// so everything expensive resolves once.
//
// API sends are not that. Every caller brings its own sender, body and
// recipient, so there is no shared context to resolve. What they do share is a
// moment in time: under load there are always dozens of them in flight at once,
// and the expensive work is per ROUND TRIP, not per message. Two hundred
// messages from two hundred different callers still need only one suppression
// query, one wallet movement per tenant and one ClickHouse insert between them.
//
// So this collects the sends that are already in flight and runs them through
// the pipeline together. Nothing is deferred and nothing is acknowledged early:
// a caller still waits for its own real outcome — the message id, the cost, the
// carrier's verdict — and still gets it before the 202. The batch is an
// implementation of the same send, not a promise to do it later.
//
// WHAT THIS DELIBERATELY IS NOT: an accept-and-enqueue queue. That design
// returns 202 the moment a message is validated and settles the money later,
// which is faster still and introduces a state this product does not have — a
// message that is accepted but not yet charged. Getting that wrong means
// double-charging or sending for free, and it cannot be added behind a flag.
// Batching buys most of the throughput with none of that risk, because every
// safety property is unchanged: same gate, same order, money before carrier.
const (
	// coalesceWorkers is how many batches run at once. Each one holds a wallet
	// row lock for its tenants while it charges, so this is also the number of
	// ways a single busy tenant's charges can contend.
	coalesceWorkers = 8

	// coalesceMaxBatch bounds how much money one wallet hold covers, the same
	// reasoning as batchSize for campaigns — a crash mid-batch strands at most
	// this many messages' worth of held funds.
	coalesceMaxBatch = 200

	// coalesceQueueDepth is the backlog before callers start waiting to be
	// admitted. Past this, blocking the caller IS the answer: it is the
	// backpressure that keeps a burst from becoming unbounded memory.
	coalesceQueueDepth = 4096

	// coalesceTimeout caps one batch. It is generous because the batch serves
	// many callers and abandoning it halfway would leave holds taken against
	// messages nobody was told about.
	coalesceTimeout = 30 * time.Second
)

// Coalescer runs the send pipeline in batches assembled from whatever is in
// flight. The zero value is not usable; call NewCoalescer and Start.
type Coalescer struct {
	// resolve builds the Service for one batch. It is a function rather than a
	// value because the ClickHouse handle behind it is a pool that drops and
	// reopens: a Service captured once would keep using a connection that had
	// already been abandoned.
	resolve  func(context.Context) *Service
	incoming chan *pendingSend
	stop     chan struct{}
	stopOnce sync.Once
	workers  sync.WaitGroup
}

// pendingSend is one caller waiting for its own message's outcome.
type pendingSend struct {
	ctx      context.Context
	identity store.Identity
	request  SendRequest
	outcome  chan sendOutcome
}

type sendOutcome struct {
	result SendResult
	err    error
}

// answer hands one caller its result. The channel is buffered, so a caller that
// has already given up never blocks the batch that is serving everyone else.
func (p *pendingSend) answer(result SendResult, err error) {
	p.outcome <- sendOutcome{result: result, err: err}
}

func NewCoalescer(resolve func(context.Context) *Service) *Coalescer {
	return &Coalescer{
		resolve:  resolve,
		incoming: make(chan *pendingSend, coalesceQueueDepth),
		stop:     make(chan struct{}),
	}
}

func (c *Coalescer) Start() {
	for i := 0; i < coalesceWorkers; i++ {
		c.workers.Add(1)
		go c.work()
	}
}

// Stop ends the workers and waits for the batches already in flight. Sends
// still queued when this is called are answered with an error rather than
// dropped silently — a caller that never hears back is worse than one that
// hears no.
func (c *Coalescer) Stop() {
	c.stopOnce.Do(func() { close(c.stop) })
	c.workers.Wait()
	for {
		select {
		case pending := <-c.incoming:
			pending.answer(SendResult{}, errors.New("sending: shutting down"))
		default:
			return
		}
	}
}

// work takes one send, then takes every other send already waiting, and runs
// them together.
//
// There is no timer and no batch window. Under load the batch fills on its own,
// because assembling the next one takes as long as processing the last one and
// arrivals pile up meanwhile. With one caller and nothing else in flight the
// drain finds nothing and the batch is one message with no added latency —
// which is the failure mode a fixed window has and this does not.
func (c *Coalescer) work() {
	defer c.workers.Done()
	for {
		var batch []*pendingSend
		select {
		case <-c.stop:
			return
		case first := <-c.incoming:
			batch = append(batch, first)
		}

	drain:
		for len(batch) < coalesceMaxBatch {
			select {
			case next := <-c.incoming:
				batch = append(batch, next)
			default:
				break drain
			}
		}

		c.process(batch)
	}
}

// process runs one assembled batch.
func (c *Coalescer) process(batch []*pendingSend) {
	// One context for the batch. A caller that hangs up must not cancel the
	// work being done for everyone else — and must not cancel it halfway
	// between the wallet hold and the carrier submission in particular.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(batch[0].ctx), coalesceTimeout)
	defer cancel()

	service := c.resolve(ctx)
	if service == nil {
		failPending(batch, errors.New("sending: not available on this deployment"))
		return
	}
	service.sendMixedBatch(ctx, batch)
}

// failPending answers callers whose batch could not be started at all.
func failPending(batch []*pendingSend, err error) {
	for _, pending := range batch {
		pending.answer(SendResult{}, err)
	}
}

// submit queues one send and waits for its outcome.
func (c *Coalescer) submit(ctx context.Context, identity store.Identity,
	request SendRequest) (SendResult, error) {

	pending := &pendingSend{
		ctx: ctx, identity: identity, request: request,
		outcome: make(chan sendOutcome, 1),
	}
	select {
	case c.incoming <- pending:
	case <-ctx.Done():
		return SendResult{}, ctx.Err()
	}
	select {
	case outcome := <-pending.outcome:
		return outcome.result, outcome.err
	case <-ctx.Done():
		return SendResult{}, ctx.Err()
	}
}

// mixedPlan is one message's resolved state inside a batch of unrelated sends.
// The campaign equivalent is sendPlan; this one carries the per-message sender,
// template and rate that a campaign resolves once for the whole page.
type mixedPlan struct {
	pending   *pendingSend
	messageID uuid.UUID
	sender    store.SenderID
	template  store.Template
	rate      store.PricingRate
	msisdn    string
	valid     bool
	segments  int
	cost      int64
	carrier   string
	routeID   *string

	// createdAt is when the message was recorded as queued. The settled row
	// carries it forward: messages is a ReplacingMergeTree collapsing on
	// version, so a version-2 row with a fresh created_at does not add a
	// timestamp — it REPLACES the real one, and every message would then look
	// like it was created at the instant its carrier answered.
	createdAt time.Time

	// refusal is the gate's verdict. Set means recorded as rejected, no money
	// held, and the caller gets the reason.
	refusal    string
	refusalErr error
}

// walletKey groups the messages whose holds can move as one ledger entry.
type walletKey struct {
	tenantID uuid.UUID
	currency string
}

// sendMixedBatch runs a batch of unrelated sends through the same pipeline
// Send runs one through, with the per-round-trip work done once for all of
// them.
//
// The order is the order of Send and of SendBatch, and it is the safety
// property: everything that can refuse happens before money moves, and money
// moves before anything reaches a carrier.
func (s *Service) sendMixedBatch(ctx context.Context, batch []*pendingSend) {
	if len(batch) == 0 {
		return
	}

	plans, recipients := s.planMixedBatch(ctx, batch)
	if len(plans) == 0 {
		return
	}

	// Everything the gate needs that is per tenant rather than per message:
	// one suppression query, one balance read and one status read each, however
	// many messages that tenant has in this batch.
	tenants, err := s.loadTenantStates(ctx, plans, recipients)
	if err != nil {
		failAll(plans, err)
		return
	}

	holds := map[walletKey]int64{}
	for _, plan := range plans {
		state := tenants[plan.pending.identity.TenantID]
		gateErr := messaging.Check(messaging.GateInput{
			TenantStatus: state.status, SenderStatus: plan.sender.Status,
			SenderID:       plan.sender.ID.String(),
			TemplateStatus: plan.templateStatus(), TemplateSender: plan.templateSender(),
			Suppressed:            state.suppressed[plan.msisdn],
			CarrierTemplateStatus: s.carrierTemplateStatusFor(plan.sender.Channel, plan.template),
			// The destination regime's template binding, applied identically to
			// the batched path. A gate that is weaker when messages arrive in
			// company is not a gate.
			RegisteredTemplateRequired: registeredTemplateRequired(plan.sender.Country),
			TemplateBody:               templateBody(plan.template),
			Body:                       plan.pending.request.Body,
			// The balance already committed to earlier messages in this batch is
			// subtracted, so a wallet that runs dry mid-batch refuses the rest
			// instead of going negative — the same running total SendBatch keeps.
			BalanceMinor: state.balance(plan.rate.Currency) - holds[plan.walletKey()],
			CostMinor:    plan.cost, RecipientValid: plan.valid,
		})
		if gateErr != nil {
			plan.refusal = messaging.GateFailureCode(gateErr)
			plan.refusalErr = gateErr
			continue
		}
		holds[plan.walletKey()] += plan.cost
	}

	// One ledger entry per tenant and currency. The per-message path took a row
	// lock per message, which serialised every send for a tenant behind one row.
	plans = s.holdForBatch(ctx, plans, holds)
	if len(plans) == 0 {
		return
	}

	if err := s.recordMixedQueued(ctx, plans); err != nil {
		failAll(plans, err)
		return
	}

	receipts, err := s.submitMixedBatch(ctx, plans)
	if err != nil {
		failAll(plans, err)
		return
	}

	s.settleMixedBatch(ctx, plans, receipts)
}

// planMixedBatch resolves each message far enough to be gated, answering the
// callers whose send cannot proceed at all. It returns the plans that survived
// and the recipients each tenant needs checked against its suppression list.
func (s *Service) planMixedBatch(ctx context.Context, batch []*pendingSend) (
	[]*mixedPlan, map[uuid.UUID][]string) {

	plans := make([]*mixedPlan, 0, len(batch))
	recipients := map[uuid.UUID][]string{}

	// Templates and routes repeat heavily inside one batch even when senders do
	// not, so each distinct one is read once.
	templates := map[uuid.UUID]store.Template{}
	routes := map[string]routePath{}

	for _, pending := range batch {
		identity := pending.identity
		request := pending.request

		sender, err := store.CachedSenderID(ctx, s.DB, s.Hot, identity, request.SenderID)
		if errors.Is(err, store.ErrNotFound) {
			pending.answer(SendResult{Status: "rejected", FailureCode: "sender_not_found",
				Currency: refusalCurrency(identity, "")}, messaging.ErrSenderNotApproved)
			continue
		}
		if err != nil {
			pending.answer(SendResult{}, err)
			continue
		}

		// The country's own content rules, applied to the body before anything
		// else looks at it — same rule, same place in the order, as Send.
		if regime, known := compliance.For(sender.Country); known {
			refused := false
			for _, url := range compliance.ExtractURLs(request.Body) {
				if result := regime.ValidateCtaURL(url); !result.OK {
					pending.answer(
						SendResult{Status: "rejected", FailureCode: "content_not_allowed",
							Currency: refusalCurrency(identity, sender.Country)},
						fmt.Errorf("%w: %s", messaging.ErrContentNotAllowed, result.Reason))
					refused = true
					break
				}
			}
			if refused {
				continue
			}
		}

		msisdn, valid := audience.NormaliseMsisdn(request.Msisdn, sender.Country)
		if !valid {
			// An API caller may legitimately send outside the sender's home
			// country, so fall back to the country-agnostic rule.
			msisdn, valid = audience.NormaliseE164(request.Msisdn)
		}

		segments := billing.SegmentCount(request.Body)
		rate, err := store.CachedPricingRate(ctx, s.DB, s.Hot, identity.TenantID,
			sender.Country, sender.Channel, "")
		if err != nil {
			pending.answer(SendResult{Status: "rejected", FailureCode: "no_rate",
				Currency: refusalCurrency(identity, sender.Country)},
				fmt.Errorf("sending: no rate for %s/%s", sender.Country, sender.Channel))
			continue
		}

		plan := &mixedPlan{
			pending: pending, messageID: uuid.New(), sender: sender, rate: rate,
			msisdn: msisdn, valid: valid, segments: segments,
			cost: int64(segments) * rate.PerSegmentMinor,
		}

		if request.TemplateID != nil {
			template, cached := templates[*request.TemplateID]
			if !cached {
				template, err = store.GetTemplate(ctx, s.DB, identity, *request.TemplateID)
				if errors.Is(err, store.ErrNotFound) {
					pending.answer(
						SendResult{Status: "rejected", FailureCode: "template_not_found",
							Currency: refusalCurrency(identity, sender.Country)},
						messaging.ErrTemplateNotApproved)
					continue
				}
				if err != nil {
					pending.answer(SendResult{}, err)
					continue
				}
				templates[*request.TemplateID] = template
			}
			plan.template = template
		}

		corridor := sender.Country + "/" + sender.Channel
		path, cached := routes[corridor]
		if !cached {
			path.carrier, path.routeID = s.resolvePath(ctx, sender.Country, sender.Channel)
			routes[corridor] = path
		}
		plan.carrier, plan.routeID = path.carrier, path.routeID

		if valid {
			recipients[identity.TenantID] = append(recipients[identity.TenantID], msisdn)
		}
		plans = append(plans, plan)
	}
	return plans, recipients
}

// routePath is the carrier and route a corridor resolves to, cached for the
// life of one batch.
type routePath struct {
	carrier string
	routeID *string
}

// tenantState is the per-tenant half of the gate's input, read once per batch.
type tenantState struct {
	identity   store.Identity
	status     string
	suppressed map[string]bool
	balances   map[string]int64
}

func (t *tenantState) balance(currency string) int64 { return t.balances[currency] }

func (s *Service) loadTenantStates(ctx context.Context, plans []*mixedPlan,
	recipients map[uuid.UUID][]string) (map[uuid.UUID]*tenantState, error) {

	states := map[uuid.UUID]*tenantState{}
	for _, plan := range plans {
		identity := plan.pending.identity
		if _, done := states[identity.TenantID]; done {
			continue
		}
		state := &tenantState{identity: identity, balances: map[string]int64{}}

		// One query for every recipient this tenant is sending to, rather than
		// one per message. Suppression is never cached — a stale "not
		// suppressed" messages someone who opted out.
		suppressed, err := store.SuppressedSet(ctx, s.DB, identity, recipients[identity.TenantID])
		if err != nil {
			return nil, err
		}
		state.suppressed = suppressed

		balances, err := store.ListWalletBalances(ctx, s.DB, identity)
		if err != nil {
			return nil, err
		}
		for _, entry := range balances {
			state.balances[entry.Currency] = entry.BalanceMinor
		}

		status, err := store.CachedTenantStatus(ctx, s.DB, s.Hot, identity)
		if err != nil {
			return nil, err
		}
		state.status = status

		states[identity.TenantID] = state
	}
	return states, nil
}

// holdForBatch takes one wallet movement per tenant and currency and returns
// the plans still in play. A group whose hold fails is answered and dropped:
// nothing was charged, so — exactly as on the single-send path — nothing is
// recorded either.
func (s *Service) holdForBatch(ctx context.Context, plans []*mixedPlan,
	holds map[walletKey]int64) []*mixedPlan {

	failed := map[walletKey]error{}
	for key, total := range holds {
		if total <= 0 {
			continue
		}
		var identity store.Identity
		count := 0
		for _, plan := range plans {
			if plan.refusal == "" && plan.walletKey() == key {
				identity = plan.pending.identity
				count++
			}
		}
		if _, err := store.AppendLedgerEntry(ctx, s.DB, identity, store.LedgerEntry{
			Currency: key.currency, Type: "charge", AmountMinor: total,
			Description: fmt.Sprintf("Message hold (%d messages)", count),
		}); err != nil {
			failed[key] = err
		}
	}
	if len(failed) == 0 {
		return plans
	}

	kept := plans[:0]
	for _, plan := range plans {
		err, groupFailed := failed[plan.walletKey()]
		if !groupFailed || plan.refusal != "" {
			kept = append(kept, plan)
			continue
		}
		if errors.Is(err, store.ErrInsufficientFunds) {
			plan.pending.answer(
				SendResult{Status: "rejected", FailureCode: "insufficient_balance"},
				messaging.ErrInsufficientFunds)
			continue
		}
		plan.pending.answer(SendResult{}, err)
	}
	return kept
}

// recordMixedQueued writes every message in the batch — queued and refused
// alike — in one ClickHouse round trip per table.
func (s *Service) recordMixedQueued(ctx context.Context, plans []*mixedPlan) error {
	now := time.Now().UTC()
	records := make([]store.MessageRecord, 0, len(plans))
	events := make([]store.MessageEvent, 0, len(plans))
	rollups := make([]store.RollupRow, 0, len(plans))

	for _, plan := range plans {
		state := messaging.StateQueued
		cost := plan.cost
		msisdn := plan.msisdn
		var errorCode *string
		if plan.refusal != "" {
			state = messaging.StateRejected
			cost = 0
			code := plan.refusal
			errorCode = &code
			// A refused message is recorded against what the caller actually
			// asked for, not against a normalised form of a number we would not
			// send to — the same choice the single-send path makes.
			msisdn = plan.pending.request.Msisdn
		}
		plan.createdAt = now
		records = append(records, store.MessageRecord{
			TenantID: plan.pending.identity.TenantID, ID: plan.messageID,
			Channel: plan.sender.Channel, Country: plan.sender.Country,
			SenderHeader: plan.sender.Header, TemplateID: plan.pending.request.TemplateID,
			Msisdn: msisdn, Status: string(state), ErrorCode: errorCode,
			FraudFlag: "none", Segments: uint8(plan.segments), CostMinor: cost,
			Currency: plan.rate.Currency, CampaignID: plan.pending.request.CampaignID,
			Carrier: plan.carrier, RouteID: plan.routeID,
			CreatedAt: now, UpdatedAt: now, Version: 1,
		})
		events = append(events, store.MessageEvent{
			TenantID: plan.pending.identity.TenantID, MessageID: plan.messageID,
			ToState: string(state), ErrorCode: errorCode, OccurredAt: now,
		})
		rollups = append(rollups, store.RollupRow{
			TenantID: plan.pending.identity.TenantID, Hour: now.Truncate(time.Hour),
			Channel: plan.sender.Channel, Country: plan.sender.Country,
			Status: string(state), MessageCount: 1,
			SegmentCount: uint64(plan.segments), CostMinor: cost,
			Currency: plan.rate.Currency,
		})
	}
	return s.writeBatch(ctx, records, events, rollups)
}

// submitMixedBatch hands the accepted messages to their carriers, one call per
// channel rather than one per message.
func (s *Service) submitMixedBatch(ctx context.Context, plans []*mixedPlan) (
	map[string]connector.Receipt, error) {

	byChannel := map[string][]connector.Submission{}
	for _, plan := range plans {
		if plan.refusal != "" {
			continue
		}
		carrierTemplateID := ""
		if plan.template.CarrierTemplateID != nil {
			carrierTemplateID = *plan.template.CarrierTemplateID
		}
		byChannel[plan.sender.Channel] = append(byChannel[plan.sender.Channel],
			connector.Submission{
				MessageID: plan.messageID.String(), Msisdn: plan.msisdn,
				Sender: plan.sender.Header, Body: plan.pending.request.Body,
				Channel: plan.sender.Channel, Country: plan.sender.Country,
				CarrierTemplateID: carrierTemplateID,
				TemplateVariables: TemplateVariables(plan.template,
					plan.pending.request.Variables),
			})
	}

	receipts := map[string]connector.Receipt{}
	for channel, submissions := range byChannel {
		batch, err := s.carrierFor(channel).Submit(ctx, submissions)
		if err != nil {
			return nil, fmt.Errorf("sending: submit batch: %w", err)
		}
		for _, receipt := range batch {
			receipts[receipt.MessageID] = receipt
		}
	}
	return receipts, nil
}

// settleMixedBatch applies the carriers' verdicts, releases the holds for the
// messages they refused, and answers every caller.
func (s *Service) settleMixedBatch(ctx context.Context, plans []*mixedPlan,
	receipts map[string]connector.Receipt) {

	settled := time.Now().UTC()
	records := make([]store.MessageRecord, 0, len(plans))
	events := make([]store.MessageEvent, 0, len(plans))
	rollups := make([]store.RollupRow, 0, len(plans))
	releases := map[walletKey]int64{}
	outcomes := make(map[*mixedPlan]sendOutcome, len(plans))

	for _, plan := range plans {
		if plan.refusal != "" {
			outcomes[plan] = sendOutcome{
				result: SendResult{
					MessageID: plan.messageID, Status: "rejected",
					FailureCode: plan.refusal, Segments: plan.segments,
					Currency: plan.rate.Currency,
				},
				err: plan.refusalErr,
			}
			continue
		}

		receipt, found := receipts[plan.messageID.String()]
		state := messaging.StateAccepted
		cost := plan.cost
		var errorCode, errorClass, carrierRef *string

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
			releases[plan.walletKey()] += plan.cost
		} else {
			ref := receipt.CarrierRef
			carrierRef = &ref
		}

		records = append(records, store.MessageRecord{
			TenantID: plan.pending.identity.TenantID, ID: plan.messageID,
			Channel: plan.sender.Channel, Country: plan.sender.Country,
			SenderHeader: plan.sender.Header, TemplateID: plan.pending.request.TemplateID,
			Msisdn: plan.msisdn, Status: string(state), ErrorCode: errorCode,
			ErrorClass: errorClass, FraudFlag: "none", Segments: uint8(plan.segments),
			CostMinor: cost, Currency: plan.rate.Currency,
			CampaignID: plan.pending.request.CampaignID, CarrierRef: carrierRef,
			Carrier: plan.carrier, RouteID: plan.routeID,
			CreatedAt: plan.createdAt, SentAt: &settled, UpdatedAt: settled, Version: 2,
		})
		events = append(events, store.MessageEvent{
			TenantID: plan.pending.identity.TenantID, MessageID: plan.messageID,
			FromState: string(messaging.StateQueued), ToState: string(state),
			ErrorCode: errorCode, OccurredAt: settled,
		})
		rollups = append(rollups, store.RollupRow{
			TenantID: plan.pending.identity.TenantID, Hour: settled.Truncate(time.Hour),
			Channel: plan.sender.Channel, Country: plan.sender.Country,
			Status: string(state), MessageCount: 1,
			SegmentCount: uint64(plan.segments), CostMinor: cost,
			Currency: plan.rate.Currency,
		})
		outcomes[plan] = sendOutcome{result: SendResult{
			MessageID: plan.messageID, Status: messaging.ContractStatus(state),
			CostMinor: cost, Currency: plan.rate.Currency, Segments: plan.segments,
		}}
	}

	// Refunds before the callers are told anything, so a caller that reads its
	// balance the instant it gets a rejection sees the money back.
	for key, amount := range releases {
		if amount <= 0 {
			continue
		}
		var identity store.Identity
		for _, plan := range plans {
			if plan.walletKey() == key {
				identity = plan.pending.identity
				break
			}
		}
		if _, err := store.AppendLedgerEntry(ctx, s.DB, identity, store.LedgerEntry{
			Currency: key.currency, Type: "refund", AmountMinor: amount,
			Description: "Released holds for carrier-rejected messages",
		}); err != nil {
			failAll(plans, err)
			return
		}
	}

	if err := s.writeBatch(ctx, records, events, rollups); err != nil {
		failAll(plans, err)
		return
	}

	for plan, outcome := range outcomes {
		plan.pending.answer(outcome.result, outcome.err)
	}
}

func (p *mixedPlan) walletKey() walletKey {
	return walletKey{tenantID: p.pending.identity.TenantID, currency: p.rate.Currency}
}

func (p *mixedPlan) templateStatus() string {
	if p.pending.request.TemplateID == nil {
		return ""
	}
	return p.template.Status
}

func (p *mixedPlan) templateSender() string {
	if p.pending.request.TemplateID == nil {
		return ""
	}
	return p.template.SenderID.String()
}

// failAll hands the same failure to every caller still waiting on a batch. A
// batch that dies partway leaves the same trace a single send that dies partway
// leaves: the messages already written stay written, and nobody is told their
// message succeeded.
func failAll(plans []*mixedPlan, err error) {
	for _, plan := range plans {
		plan.pending.answer(SendResult{}, err)
	}
}
