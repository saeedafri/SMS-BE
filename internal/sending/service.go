// Package sending is the data plane: it takes a send request through the gate,
// hands it to a connector, and applies the delivery report that comes back.
package sending

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/domain/audience"
	"github.com/saeedafri/sms-be/internal/domain/billing"
	"github.com/saeedafri/sms-be/internal/domain/compliance"
	"github.com/saeedafri/sms-be/internal/domain/messaging"
	"github.com/saeedafri/sms-be/internal/store"
)

// Service owns the send path. It is deliberately not an HTTP handler: campaign
// fan-out (Stage 6) and the public send API both call the same code, so the
// rules cannot drift between the two.
type Service struct {
	DB         *pgxpool.Pool
	ClickHouse driver.Conn
	Connector  connector.Connector

	// Carriers routes a channel to its own gateway. Zero value means every
	// channel goes to Connector, which is how this behaved before RCS had a
	// real carrier and is still right for a deployment with only the sandbox.
	Carriers connector.Registry
	// Logger is optional. It exists for the paths that must not fail loudly and
	// must not fail silently either — landing a campaign that fan-out abandoned
	// is the one that matters.
	Logger *slog.Logger

	// Hot is the configuration cache the send path reads through. Nil means
	// every lookup goes to Postgres, which is the behaviour before this existed
	// and what the tests that assert on freshness rely on.
	Hot *store.HotCache

	// Coalescer batches the transactional send path. Nil means every send pays
	// for its own round trips, which is correct and slow — see coalesce.go for
	// why the batched form is the same send rather than a deferred one.
	Coalescer *Coalescer
}

// carrierFor picks the gateway for a channel.
//
// The registry's Default is only consulted when it is set, so a Service built
// the old way — one Connector, no registry — behaves exactly as it did.
func (s *Service) carrierFor(channel string) connector.Connector {
	if carrier := s.Carriers.For(channel); carrier != nil {
		return carrier
	}
	return s.Connector
}

// SendRequest is one message to send.
type SendRequest struct {
	SenderID   uuid.UUID
	TemplateID *uuid.UUID
	Msisdn     string
	Body       string
	CampaignID *uuid.UUID

	// Variables fill a template's named slots. Ignored on channels that carry a
	// plain body; required on RCS, where the carrier holds the template and we
	// send it nothing but the id and these values.
	Variables map[string]string
}

// SendResult is what happened.
type SendResult struct {
	MessageID   uuid.UUID
	Status      string
	CostMinor   int64
	Currency    string
	Segments    int
	FailureCode string
}

// Send runs one message through the whole pipeline.
//
// The ordering here is the safety-critical part: everything that could refuse
// the send happens BEFORE money moves and before anything is handed to a
// carrier. A message that reached a carrier but failed to be recorded is
// unrecoverable — the recipient got it and we have no idea.
func (s *Service) Send(ctx context.Context, identity store.Identity, request SendRequest) (SendResult, error) {
	// Batched when a coalescer is running, one at a time when it is not. The
	// two paths apply the same rules in the same order and return the same
	// result; they differ only in how many round trips they spend doing it.
	// Tests that want the unbatched path leave Coalescer nil, which is also
	// what a deployment gets before Start is called.
	if s.Coalescer != nil {
		return s.Coalescer.submit(ctx, identity, request)
	}
	return s.sendOne(ctx, identity, request)
}

// sendOne is the unbatched pipeline: one message, its own round trips.
func (s *Service) sendOne(ctx context.Context, identity store.Identity, request SendRequest) (SendResult, error) {
	// 1. Resolve the sender and confirm it is approved for this tenant.
	sender, err := store.CachedSenderID(ctx, s.DB, s.Hot, identity, request.SenderID)
	if errors.Is(err, store.ErrNotFound) {
		return SendResult{Status: "rejected", FailureCode: "sender_not_found",
			Currency: refusalCurrency(identity, "")}, messaging.ErrSenderNotApproved
	}
	if err != nil {
		return SendResult{}, err
	}

	// 1a. Apply the country's own content rules to the body.
	//
	// India bans public URL shorteners under DLT. That rule lived in the
	// regime, was enforced when a TEMPLATE was created, and was absent from the
	// send path — so a bit.ly link went out fine as long as nobody put it in a
	// template first. The browser also checks, but a client-side rule is a hint;
	// this is the control, and it sits here rather than in the HTTP handler so
	// campaign sends and API sends cannot diverge.
	if regime, known := compliance.For(sender.Country); known {
		for _, url := range compliance.ExtractURLs(request.Body) {
			if result := regime.ValidateCtaURL(url); !result.OK {
				return SendResult{Status: "rejected", FailureCode: "content_not_allowed",
						Currency: refusalCurrency(identity, sender.Country)},
					fmt.Errorf("%w: %s", messaging.ErrContentNotAllowed, result.Reason)
			}
		}
	}

	// 2. Normalise the recipient the same way the audience import does, so a
	// number stored from a CSV matches a number sent to via the API.
	msisdn, recipientValid := audience.NormaliseMsisdn(request.Msisdn, sender.Country)
	if !recipientValid {
		// Fall back to the country-agnostic rule: an API caller may legitimately
		// send to a country other than the sender's home one.
		msisdn, recipientValid = audience.NormaliseE164(request.Msisdn)
	}

	// 3. Price it. Segment arithmetic is shared with the estimate endpoint, so
	// the quote a user saw and the charge they get cannot disagree.
	segments := billing.SegmentCount(request.Body)
	rate, err := store.CachedPricingRate(ctx, s.DB, s.Hot, identity.TenantID, sender.Country, sender.Channel, "")
	if err != nil {
		return SendResult{Status: "rejected", FailureCode: "no_rate",
				Currency: refusalCurrency(identity, sender.Country)},
			fmt.Errorf("sending: no rate for %s/%s", sender.Country, sender.Channel)
	}
	cost := int64(segments) * rate.PerSegmentMinor

	// 4. Gather the rest of the gate's inputs.
	templateStatus, templateSender := "", ""
	var template store.Template
	if request.TemplateID != nil {
		var err error
		template, err = store.GetTemplate(ctx, s.DB, identity, *request.TemplateID)
		if errors.Is(err, store.ErrNotFound) {
			return SendResult{Status: "rejected", FailureCode: "template_not_found",
				Currency: rate.Currency}, messaging.ErrTemplateNotApproved
		}
		if err != nil {
			return SendResult{}, err
		}
		templateStatus = template.Status
		templateSender = template.SenderID.String()
	}

	suppressed := false
	if recipientValid {
		if suppressed, err = store.IsSuppressed(ctx, s.DB, identity, msisdn); err != nil {
			return SendResult{}, err
		}
	}

	balance := int64(0)
	balances, err := store.ListWalletBalances(ctx, s.DB, identity)
	if err != nil {
		return SendResult{}, err
	}
	for _, entry := range balances {
		if entry.Currency == rate.Currency {
			balance = entry.BalanceMinor
		}
	}

	tenantStatus, err := store.CachedTenantStatus(ctx, s.DB, s.Hot, identity)
	if err != nil {
		return SendResult{}, err
	}

	// 5. The gate. Nothing has been charged and nothing has been sent yet, so
	// a refusal here costs the tenant nothing at all.
	gateErr := messaging.Check(messaging.GateInput{
		TenantStatus: tenantStatus, SenderStatus: sender.Status,
		SenderID: sender.ID.String(), TemplateStatus: templateStatus,
		TemplateSender: templateSender, Suppressed: suppressed,
		CarrierTemplateStatus: s.carrierTemplateStatusFor(sender.Channel, template),
		BalanceMinor:          balance, CostMinor: cost, RecipientValid: recipientValid,
		RegisteredTemplateRequired: registeredTemplateRequired(sender.Country),
		TemplateBody:               templateBody(template),
		Body:                       request.Body,
	})

	messageID := uuid.New()
	now := time.Now().UTC()

	if gateErr != nil {
		code := messaging.GateFailureCode(gateErr)
		// A refused send is still recorded. A tenant asking "why didn't this
		// arrive" deserves an answer, and silence at the gate is exactly the
		// opaque behaviour this product exists to replace.
		if err := s.record(ctx, identity, store.MessageRecord{
			ID: messageID, Channel: sender.Channel, Country: sender.Country,
			SenderHeader: sender.Header, Msisdn: request.Msisdn,
			Status: string(messaging.StateRejected), ErrorCode: &code,
			Segments: uint8(segments), CostMinor: 0, Currency: rate.Currency,
			CampaignID: request.CampaignID, CreatedAt: now, UpdatedAt: now, Version: 1,
		}, "", string(messaging.StateRejected), code); err != nil {
			return SendResult{}, err
		}
		return SendResult{MessageID: messageID, Status: "rejected", FailureCode: code,
			Segments: segments, Currency: rate.Currency}, gateErr
	}

	// 6. Hold the money. The hold is taken before submission so a carrier can
	// never receive a message we have not reserved payment for.
	if _, err := store.AppendLedgerEntry(ctx, s.DB, identity, store.LedgerEntry{
		Currency: rate.Currency, Type: "charge", AmountMinor: cost,
		Description: "Message hold " + messageID.String(),
	}); err != nil {
		if errors.Is(err, store.ErrInsufficientFunds) {
			return SendResult{Status: "rejected", FailureCode: "insufficient_balance"},
				messaging.ErrInsufficientFunds
		}
		return SendResult{}, err
	}

	// 7. Pick the path, record as queued, then submit.
	//
	// The routes table described the network and nothing read it: priorities
	// could be reordered and routes enabled or disabled without one message
	// changing, and every live message was recorded with no carrier, so the
	// deliverability-by-carrier screens worked only for seeded history.
	carrier, routeID := s.resolvePath(ctx, sender.Country, sender.Channel)

	if err := s.record(ctx, identity, store.MessageRecord{
		ID: messageID, Channel: sender.Channel, Country: sender.Country,
		SenderHeader: sender.Header, TemplateID: request.TemplateID,
		Msisdn: msisdn, Status: string(messaging.StateQueued),
		Segments: uint8(segments), CostMinor: cost, Currency: rate.Currency,
		CampaignID: request.CampaignID, Carrier: carrier, RouteID: routeID,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}, "", string(messaging.StateQueued), ""); err != nil {
		return SendResult{}, err
	}

	carrierTemplateID := ""
	if template.CarrierTemplateID != nil {
		carrierTemplateID = *template.CarrierTemplateID
	}

	receipts, err := s.carrierFor(sender.Channel).Submit(ctx, []connector.Submission{{
		MessageID: messageID.String(), Msisdn: msisdn, Sender: sender.Header,
		Body: request.Body, Channel: sender.Channel, Country: sender.Country,
		CarrierTemplateID: carrierTemplateID,
		TemplateVariables: TemplateVariables(template, request.Variables),
	}})
	if err != nil {
		return SendResult{}, fmt.Errorf("sending: submit: %w", err)
	}

	state, errorCode := messaging.StateAccepted, ""
	var carrierRef *string
	if len(receipts) > 0 {
		if receipts[0].Accepted {
			ref := receipts[0].CarrierRef
			carrierRef = &ref
		} else {
			state = messaging.StateRejected
			errorCode = receipts[0].ErrorCode
		}
	}

	// A carrier rejection releases the hold immediately: nothing was delivered,
	// so nothing is owed.
	if state == messaging.StateRejected {
		if err := s.release(ctx, identity, rate.Currency, cost, messageID); err != nil {
			return SendResult{}, err
		}
	}

	sentAt := now
	update := store.MessageRecord{
		ID: messageID, Channel: sender.Channel, Country: sender.Country,
		SenderHeader: sender.Header, TemplateID: request.TemplateID, Msisdn: msisdn,
		Status: string(state), Segments: uint8(segments), Currency: rate.Currency,
		CostMinor: cost, CampaignID: request.CampaignID, CarrierRef: carrierRef,
		Carrier: carrier, RouteID: routeID,
		CreatedAt: now, SentAt: &sentAt, UpdatedAt: time.Now().UTC(), Version: 2,
	}
	if errorCode != "" {
		update.ErrorCode = &errorCode
		class, _ := messaging.ClassifyCarrierError(errorCode)
		classValue := string(class)
		update.ErrorClass = &classValue
		update.CostMinor = 0
	}
	if err := s.record(ctx, identity, update,
		string(messaging.StateQueued), string(state), errorCode); err != nil {
		return SendResult{}, err
	}

	return SendResult{
		MessageID: messageID, Status: messaging.SubmitStatus(state),
		CostMinor: update.CostMinor, Currency: rate.Currency, Segments: segments,
	}, nil
}

// ApplyDeliveryReport settles a message against what the carrier eventually
// said. This is where "no charge for undelivered" is actually enforced.
func (s *Service) ApplyDeliveryReport(ctx context.Context, identity store.Identity,
	report connector.DeliveryReport) error {

	to := messaging.StateUndelivered
	if report.Delivered {
		to = messaging.StateDelivered
	}
	return s.settle(ctx, identity, report, to)
}

// settle applies a terminal outcome to a message. The target state is a
// parameter because the reconciler lands messages in `expired` rather than
// `undelivered` — "the carrier said it failed" and "the carrier never said
// anything" are different facts, and a tenant debugging a route needs to tell
// them apart. Both release the hold; only the label differs.
func (s *Service) settle(ctx context.Context, identity store.Identity,
	report connector.DeliveryReport, to messaging.State) error {

	messageID, err := uuid.Parse(report.MessageID)
	if err != nil {
		return fmt.Errorf("sending: bad message id in report: %w", err)
	}
	current, err := store.LoadMessageState(ctx, s.ClickHouse, identity.TenantID, messageID)
	if errors.Is(err, store.ErrNotFound) {
		// A report for a message we never sent is dropped rather than trusted.
		return nil
	}
	if err != nil {
		return err
	}

	from := messaging.State(current.Status)

	// Replayed receipts are common: carriers retry, and a terminal message must
	// not move again. Refusing the transition here is what makes the ingest
	// path idempotent.
	if !messaging.CanTransition(from, to) {
		return nil
	}

	switch messaging.EffectOf(to) {
	case messaging.EffectCharge:
		// The hold already debited the wallet at send time, so a delivery needs
		// no further ledger movement — the money simply stays spent.
	case messaging.EffectRelease:
		if err := s.release(ctx, identity, current.Currency, current.CostMinor, messageID); err != nil {
			return err
		}
	}

	occurred := report.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now().UTC()
	}
	// Carried forward, not dropped: this row REPLACES the previous version, so
	// anything not set here is erased rather than left alone.
	//
	// The carrier fields are the ones that used to go missing. Losing `carrier`
	// emptied the deliverability-by-carrier report of everything that had
	// actually been delivered, and losing `carrier_ref` broke the SECOND
	// webhook for a message: Airtel sends SENT, then DELIVERED, then sometimes
	// READ, and its payload carries no id of ours — the reference is the only
	// way back. After the first settle there was nothing left to match on.
	record := store.MessageRecord{
		ID: messageID, Channel: current.Channel, Country: current.Country,
		SenderHeader: current.SenderHeader, Msisdn: current.Msisdn,
		Email:      current.Email,
		TemplateID: current.TemplateID,
		Carrier:    current.Carrier, RouteID: current.RouteID,
		CarrierRef: current.CarrierRef, SentAt: current.SentAt,
		CampaignName: current.CampaignName,
		Status:       string(to), Segments: current.Segments, Currency: current.Currency,
		CostMinor: current.CostMinor, CampaignID: current.CampaignID,
		CreatedAt: current.CreatedAt, UpdatedAt: occurred, Version: current.Version + 1,
	}
	if report.Delivered {
		record.DeliveredAt = &occurred
	} else {
		record.CostMinor = 0
		if report.ErrorCode != "" {
			code := report.ErrorCode
			class, _ := messaging.ClassifyCarrierError(code)
			classValue := string(class)
			record.ErrorCode, record.ErrorClass = &code, &classValue
		}
	}
	return s.record(ctx, identity, record, string(from), string(to), report.ErrorCode)
}

// release returns held money to the wallet.
func (s *Service) release(ctx context.Context, identity store.Identity,
	currency string, amount int64, messageID uuid.UUID) error {

	if amount <= 0 {
		return nil
	}
	_, err := store.AppendLedgerEntry(ctx, s.DB, identity, store.LedgerEntry{
		Currency: currency, Type: "refund", AmountMinor: amount,
		Description: "Released hold for undelivered message " + messageID.String(),
	})
	return err
}

// record writes the message row, its transition event, and the rollup in one
// place, so the three can never disagree about what happened.
func (s *Service) record(ctx context.Context, identity store.Identity,
	record store.MessageRecord, from, to, errorCode string) error {

	record.TenantID = identity.TenantID
	if record.FraudFlag == "" {
		record.FraudFlag = "none"
	}
	if err := store.InsertMessages(ctx, s.ClickHouse, []store.MessageRecord{record}); err != nil {
		return err
	}

	var code *string
	if errorCode != "" {
		code = &errorCode
	}
	if err := store.InsertMessageEvents(ctx, s.ClickHouse, []store.MessageEvent{{
		TenantID: identity.TenantID, MessageID: record.ID,
		FromState: from, ToState: to, ErrorCode: code, OccurredAt: record.UpdatedAt,
	}}); err != nil {
		return err
	}

	// Rollups are permanent while raw rows age out, so they are written on the
	// same path rather than derived later from data that may be gone.
	return store.InsertRollups(ctx, s.ClickHouse, []store.RollupRow{{
		TenantID: identity.TenantID, Hour: record.UpdatedAt.Truncate(time.Hour),
		Channel: record.Channel, Country: record.Country, Status: to,
		MessageCount: 1, SegmentCount: uint64(record.Segments),
		CostMinor: record.CostMinor, Currency: record.Currency,
	}})
}

// carrierTemplateStatusFor returns the carrier's verdict on a template, and
// only where that verdict is a real precondition for the send.
//
// Two conditions, and both matter. Returning it for every channel would refuse
// every WhatsApp and Email send the moment the column defaulted to
// 'not_submitted', which is what it defaults to for every existing row. And
// returning it when no real gateway is configured would refuse every RCS send
// on a sandbox-only deployment — where there is no carrier to have approved
// anything, and the send is going nowhere near one.
//
// So: the carrier's approval is required exactly when a carrier will receive
// the message.
func (s *Service) carrierTemplateStatusFor(channel string, template store.Template) string {
	if template.ID == uuid.Nil {
		return ""
	}
	if _, dedicated := s.Carriers.Dedicated(channel); !dedicated {
		return ""
	}
	return template.CarrierStatus
}

// TemplateVariables puts a caller's named values into the order the template
// declares them.
//
// The order is the template's, not the caller's map iteration, because Airtel's
// placeholders are positional: {{1}} is whichever variable the template listed
// first, and filling them in Go's randomised map order would put the discount
// code in the customer's name — differently on every send.
//
// A variable the caller did not supply becomes an empty string rather than
// being skipped, so positions never shift. Airtel refuses a send with fewer
// values than the template declares, and a silently shortened list would move
// every later variable one slot to the left.
func TemplateVariables(template store.Template, values map[string]string) []connector.TemplateVariable {
	if len(template.Variables) == 0 {
		return nil
	}
	filled := make([]connector.TemplateVariable, 0, len(template.Variables))
	for _, name := range template.Variables {
		filled = append(filled, connector.TemplateVariable{
			Name: name, Value: values[name],
		})
	}
	return filled
}

// resolvePath records which carrier is about to carry this message, and over
// which route row.
//
// Two sources, and they are not equal. When a channel has its own gateway —
// RCS goes to whichever of Airtel or Vi this deployment holds credentials for —
// that gateway IS the carrier, whatever the routes table would have picked. The
// routes table is consulted second, only to find the row describing that same
// carrier so the corridor's commercial terms stay attached.
//
// Recording the routes table's highest-priority carrier instead was wrong in a
// way that hides: the message goes to Airtel and the log says Jio, so the
// deliverability-by-carrier report blames the wrong network for every failure.
//
// Absence is normal, not an error. Email and WhatsApp do not go over a carrier
// at all, and a corridor with no active route sends exactly as before.
func (s *Service) resolvePath(ctx context.Context, country, channel string) (string, *string) {
	if dedicated, ok := s.Carriers.Dedicated(channel); ok {
		carrier := strings.ToUpper(dedicated.Name())
		route, err := store.SelectRouteForCarrier(ctx, s.DB, country, channel, carrier)
		if err != nil {
			// No route row for this carrier is worth recording as-is rather
			// than falling back to another carrier's row: the message really
			// did go through this gateway, and a route id pointing at a
			// different carrier would be worse than none.
			return carrier, nil
		}
		id := route.ID.String()
		return carrier, &id
	}

	route, err := store.SelectRoute(ctx, s.DB, country, channel)
	if err != nil {
		return "", nil
	}
	id := route.ID.String()
	return route.Carrier, &id
}

// refusalCurrency is the currency to report on a refusal that never got as far
// as looking up a rate.
//
// An empty string is not a currency code, and it is what a caller saw on every
// early refusal: no such sender, no rate for the corridor, content the country
// bans. The contract makes currency a required non-null string, so the answer
// is the regime's currency for whichever country we do know — the sender's when
// there is one, the tenant's otherwise.
func refusalCurrency(identity store.Identity, country string) string {
	for _, candidate := range []string{country, identity.Country} {
		if candidate == "" {
			continue
		}
		if regime, known := compliance.For(candidate); known {
			return regime.Currency()
		}
	}
	return ""
}

// registeredTemplateRequired asks the DESTINATION's regime whether a send must
// carry a template the regulator registered.
//
// Resolved from the sender's country, in one function, so the three dispatch
// paths cannot disagree about it. An unknown country requires nothing: we do
// not operate there, and inventing a rule for it would refuse sends we have no
// basis to refuse.
func registeredTemplateRequired(country string) bool {
	regime, known := compliance.For(country)
	return known && regime.RequiresRegisteredTemplate()
}

// templateBody is the registered text a submitted body must instantiate.
//
// It is not simply Template.Body. An SMS template keeps its text there, but an
// RCS one keeps it inside rcs_content — the channel-specific union the contract
// defines — and reading only Body meant every RCS send compared its text
// against an empty string and was refused. Found by the RCS end-to-end tests
// the moment template binding was switched on, which is what they are for.
func templateBody(template store.Template) string {
	if template.Body != nil && *template.Body != "" {
		return *template.Body
	}
	if len(template.RCSContent) > 0 {
		var content struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(template.RCSContent, &content); err == nil {
			return content.Text
		}
	}
	return ""
}
