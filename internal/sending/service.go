// Package sending is the data plane: it takes a send request through the gate,
// hands it to a connector, and applies the delivery report that comes back.
package sending

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/domain/audience"
	"github.com/saeedafri/sms-be/internal/domain/billing"
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
}

// SendRequest is one message to send.
type SendRequest struct {
	SenderID   uuid.UUID
	TemplateID *uuid.UUID
	Msisdn     string
	Body       string
	CampaignID *uuid.UUID
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
	// 1. Resolve the sender and confirm it is approved for this tenant.
	sender, err := store.GetSenderID(ctx, s.DB, identity, request.SenderID)
	if errors.Is(err, store.ErrNotFound) {
		return SendResult{Status: "rejected", FailureCode: "sender_not_found"},
			messaging.ErrSenderNotApproved
	}
	if err != nil {
		return SendResult{}, err
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
	rate, err := store.FindPricingRate(ctx, s.DB, sender.Country, sender.Channel, "")
	if err != nil {
		return SendResult{Status: "rejected", FailureCode: "no_rate"},
			fmt.Errorf("sending: no rate for %s/%s", sender.Country, sender.Channel)
	}
	cost := int64(segments) * rate.PerSegmentMinor

	// 4. Gather the rest of the gate's inputs.
	templateStatus, templateSender := "", ""
	if request.TemplateID != nil {
		template, err := store.GetTemplate(ctx, s.DB, identity, *request.TemplateID)
		if errors.Is(err, store.ErrNotFound) {
			return SendResult{Status: "rejected", FailureCode: "template_not_found"},
				messaging.ErrTemplateNotApproved
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

	tenantStatus, err := store.TenantStatus(ctx, s.DB, identity)
	if err != nil {
		return SendResult{}, err
	}

	// 5. The gate. Nothing has been charged and nothing has been sent yet, so
	// a refusal here costs the tenant nothing at all.
	gateErr := messaging.Check(messaging.GateInput{
		TenantStatus: tenantStatus, SenderStatus: sender.Status,
		SenderID: sender.ID.String(), TemplateStatus: templateStatus,
		TemplateSender: templateSender, Suppressed: suppressed,
		BalanceMinor: balance, CostMinor: cost, RecipientValid: recipientValid,
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
		return SendResult{MessageID: messageID, Status: "failed", FailureCode: code,
			Segments: segments, Currency: rate.Currency}, gateErr
	}

	// 6. Hold the money. The hold is taken before submission so a carrier can
	// never receive a message we have not reserved payment for.
	if _, err := store.AppendLedgerEntry(ctx, s.DB, identity, store.LedgerEntry{
		Currency: rate.Currency, Type: "charge", AmountMinor: cost,
		Description: "Message hold " + messageID.String(),
	}); err != nil {
		if errors.Is(err, store.ErrInsufficientFunds) {
			return SendResult{Status: "failed", FailureCode: "insufficient_balance"},
				messaging.ErrInsufficientFunds
		}
		return SendResult{}, err
	}

	// 7. Record as queued, then submit.
	if err := s.record(ctx, identity, store.MessageRecord{
		ID: messageID, Channel: sender.Channel, Country: sender.Country,
		SenderHeader: sender.Header, TemplateID: request.TemplateID,
		Msisdn: msisdn, Status: string(messaging.StateQueued),
		Segments: uint8(segments), CostMinor: cost, Currency: rate.Currency,
		CampaignID: request.CampaignID, CreatedAt: now, UpdatedAt: now, Version: 1,
	}, "", string(messaging.StateQueued), ""); err != nil {
		return SendResult{}, err
	}

	receipts, err := s.Connector.Submit(ctx, []connector.Submission{{
		MessageID: messageID.String(), Msisdn: msisdn, Sender: sender.Header,
		Body: request.Body, Channel: sender.Channel, Country: sender.Country,
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
		MessageID: messageID, Status: messaging.ContractStatus(state),
		CostMinor: update.CostMinor, Currency: rate.Currency, Segments: segments,
	}, nil
}

// ApplyDeliveryReport settles a message against what the carrier eventually
// said. This is where "no charge for undelivered" is actually enforced.
func (s *Service) ApplyDeliveryReport(ctx context.Context, identity store.Identity,
	report connector.DeliveryReport) error {

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
	to := messaging.StateUndelivered
	if report.Delivered {
		to = messaging.StateDelivered
	}

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
	record := store.MessageRecord{
		ID: messageID, Channel: current.Channel, Country: current.Country,
		SenderHeader: current.SenderHeader, Msisdn: current.Msisdn,
		Status: string(to), Segments: current.Segments, Currency: current.Currency,
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
