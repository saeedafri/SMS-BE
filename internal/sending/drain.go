package sending

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/domain/messaging"
	"github.com/saeedafri/sms-be/internal/store"
)

// ReportSource is a connector that queues its own delivery reports rather than
// posting them to us. Only the sandbox does this: a real carrier calls our
// ingest endpoint, so there is nothing to drain.
type ReportSource interface {
	DrainReports() []connector.DeliveryReport
}

// DrainSandboxReports applies whatever the sandbox has queued.
//
// This exists so the whole lifecycle is observable locally: without it a
// message stops at "sent" forever, no wallet ever settles, and the delivered /
// undelivered distinction the product is built on cannot be seen in the UI at
// all. In production the same settlement runs from the carrier's DLR webhook
// instead — this is the identical code path, driven by a different trigger.
func (s *Service) DrainSandboxReports(ctx context.Context) (int, error) {
	source, ok := s.Connector.(ReportSource)
	if !ok {
		return 0, nil
	}

	applied := 0
	for _, report := range source.DrainReports() {
		messageID, err := uuid.Parse(report.MessageID)
		if err != nil {
			continue
		}
		// The report carries no tenant — carriers have no concept of one — so
		// it is resolved from the message before anything is scoped.
		tenantID, err := store.FindMessageTenant(ctx, s.ClickHouse, messageID)
		if err != nil {
			continue
		}
		if err := s.ApplyDeliveryReport(ctx, store.Identity{TenantID: tenantID}, report); err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}

// SettleCarrierReport applies a delivery receipt a real operator sent over an
// SMPP bind. The receipt names only the operator's own message id, so the
// message — and the tenant it belongs to — is found from the reference stored
// when the operator accepted the submit.
//
// The same id arrives in two spellings from some operators: hexadecimal in the
// submit_sm_resp and decimal in the receipt, or the reverse. A receipt that
// matches nothing is tried in the other base before it is given up on, because
// an unmatched receipt leaves a delivered message looking unsent and its hold
// never settles.
func (s *Service) SettleCarrierReport(ctx context.Context, report connector.DeliveryReport) error {
	for _, ref := range carrierRefSpellings(report.CarrierRef) {
		// Scoped to the bind's operator, so the other-base spelling cannot
		// collide with a different operator's id either.
		tenantID, messageID, err := store.FindMessageByCarrierRef(ctx, s.ClickHouse, report.Carrier, ref)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		report.CarrierRef, report.MessageID = ref, messageID.String()
		return s.ApplyDeliveryReport(ctx, store.Identity{TenantID: tenantID}, report)
	}
	return nil // a receipt for a message this deployment never sent
}

// ApplyLateSubmit settles a message the send path left pending: a submit that
// was written and never answered in time, or a message that waited for the bind
// and was sent after its caller had moved on.
//
// Only a message still queued or submitted moves. One the reconciler has
// already expired stays expired — its hold is back — and anything further on
// has been settled by a receipt already.
func (s *Service) ApplyLateSubmit(ctx context.Context, late connector.LateSubmit) error {
	messageID, err := uuid.Parse(late.MessageID)
	if err != nil {
		return nil
	}
	tenantID, err := store.FindMessageTenant(ctx, s.ClickHouse, messageID)
	if err != nil {
		return nil
	}
	identity := store.Identity{TenantID: tenantID}
	current, err := store.LoadMessageState(ctx, s.ClickHouse, tenantID, messageID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	from := messaging.State(current.Status)
	if from != messaging.StateQueued && from != messaging.StateSubmitted {
		return nil
	}

	outcome := outcomeOf(connector.Receipt{MessageID: late.MessageID, Accepted: late.Accepted,
		CarrierRef: late.CarrierRef, ErrorCode: late.ErrorCode}, true)
	if outcome.release {
		if err := s.release(ctx, identity, current.Currency, current.CostMinor, messageID); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	record := current
	record.Status, record.CarrierRef, record.UpdatedAt = string(outcome.state), outcome.ref, now
	record.ErrorCode, record.ErrorClass = outcome.codes()
	if record.SentAt == nil {
		record.SentAt = &now
	}
	if outcome.release {
		record.CostMinor = 0
	}
	// Two versions ahead of a queued row, not one. The send path writes its own
	// version 2 for a message it is still answering, and a late outcome that
	// races it must not be replaced by that write.
	record.Version = current.Version + 1
	if from == messaging.StateQueued {
		record.Version = current.Version + 2
	}
	return s.record(ctx, identity, record, string(from), string(outcome.state), outcome.code)
}

func carrierRefSpellings(ref string) []string {
	spellings := []string{ref}
	if n, err := strconv.ParseUint(ref, 10, 64); err == nil {
		spellings = append(spellings, strconv.FormatUint(n, 16), strings.ToUpper(strconv.FormatUint(n, 16)))
	} else if n, err := strconv.ParseUint(ref, 16, 64); err == nil {
		spellings = append(spellings, strconv.FormatUint(n, 10))
	}
	return spellings
}
