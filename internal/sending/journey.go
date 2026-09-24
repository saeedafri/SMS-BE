package sending

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// JourneyOutcome is what one journey send step did for one contact.
type JourneyOutcome string

const (
	// JourneySent: the message went through the gate and was recorded —
	// delivered, or refused by the gate with a reason, exactly as a campaign
	// recipient's would be.
	JourneySent JourneyOutcome = "sent"
	// JourneySuppressed: the contact is suppressed on this step's channel.
	// They leave the journey.
	JourneySuppressed JourneyOutcome = "suppressed"
	// JourneySkipped: not in this step's audience — no opt-in or no address on
	// its channel, or the template needs a value the contact has no column
	// for. Nothing is sent or charged, the same non-event a campaign makes of
	// them, and the contact moves on to the next step.
	JourneySkipped JourneyOutcome = "skipped"
	// JourneyHeld: the tenant's daily send ceiling is spent. Try again later.
	JourneyHeld JourneyOutcome = "held"
)

// SendJourneyStep sends one journey step to one contact through the campaign
// pipeline: the same leg resolution, audience rule, gate, pricing, wallet hold
// and daily ceiling as a one-recipient campaign page. A journey step is not a
// second way to send.
//
// ponytail: resolves the leg per contact, several queries each; cache per
// journey step per cycle if journeys ever carry campaign-sized volume.
func (s *Service) SendJourneyStep(ctx context.Context, identity store.Identity,
	journey store.Journey, senderID, templateID uuid.UUID, contact store.Contact) (JourneyOutcome, error) {

	listID := journey.TriggerListID

	tenantStatus, err := store.TenantStatus(ctx, s.DB, identity)
	if err != nil {
		return "", err
	}
	mapping := map[string]string{}
	if listID != nil {
		loaded, err := store.ListVariableMapping(ctx, s.DB, identity, *listID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
		if loaded != nil {
			mapping = loaded
		}
	}
	leg, err := s.resolveLeg(ctx, identity, nil, senderID, templateID, tenantStatus,
		mapping, &skipTally{})
	if err != nil {
		return "", err
	}
	leg.journeyID, leg.journeyName = &journey.ID, &journey.Name

	channel := leg.sender.Channel
	if (channel == "EMAIL" && contact.EmailSuppressed) ||
		(channel != "EMAIL" && contact.PhoneSuppressed) {
		return JourneySuppressed, nil
	}
	if !reachableOn(contact, channel) {
		return JourneySkipped, nil
	}

	allowance, err := store.ReadSendAllowance(ctx, s.DB, identity, s.now())
	if err != nil {
		return "", err
	}
	// A contact no cap may withhold is not held here either. The exemption is
	// about the contact, not about which path reached them — a customer's own
	// staff being skipped by a journey is the same failure as being skipped by
	// a campaign, and harder to notice because nobody is watching a journey's
	// recipient count.
	if !contact.AlwaysSend && allowance.Room(1) == 0 {
		return JourneyHeld, nil
	}
	balances, err := store.ListWalletBalances(ctx, s.DB, identity)
	if err != nil {
		return "", err
	}
	for _, balance := range balances {
		if balance.Currency == leg.rate.Currency {
			leg.balance = balance.BalanceMinor
		}
	}

	sent, failed, _, err := s.SendBatch(ctx, identity, leg, []store.Contact{contact})
	if err != nil {
		return "", err
	}
	if allowance.Capped() {
		if err := store.RecordSendUsage(ctx, s.DB, identity, allowance.Day, sent, 0); err != nil {
			return "", err
		}
	}
	if sent+failed == 0 {
		return JourneySkipped, nil
	}
	return JourneySent, nil
}
