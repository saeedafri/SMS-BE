package sending

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// A campaign can have two legs, and which one carries a recipient is decided
// per recipient.
//
// The rule is: skipped only when NO leg can carry them. Extending the flat
// per-campaign skip to a campaign with a fallback would be a regression, and a
// large one — an RCS card needing first_name, on 2,000 contacts of whom 200
// have RCS and none have that column, would remove all 2,000 rather than the
// 200, including the 1,800 who were only ever going to get the SMS fallback and
// for whom that fallback fills perfectly.
//
// Before this, the fallback columns on a campaign were decorative. They were
// stored, echoed back by GET /v1/campaigns, and read by nothing: a campaign
// with a fallback configured sent the primary leg to the primary channel's
// audience and stopped, so a contact the primary could not reach was not
// carried by the fallback — they were not in the audience at all.

// resolveLeg builds everything identical across one leg's recipients: its
// sender, template, price, route and brand, resolved once rather than per
// message.
func (s *Service) resolveLeg(ctx context.Context, identity store.Identity,
	campaignID *uuid.UUID, senderID, templateID uuid.UUID, tenantStatus string,
	balances []store.WalletBalance, mapping map[string]string,
	skipped *skipTally) (batchContext, error) {

	sender, err := store.GetSenderID(ctx, s.DB, identity, senderID)
	if err != nil {
		return batchContext{}, err
	}
	template, err := store.GetTemplate(ctx, s.DB, identity, templateID)
	if err != nil {
		return batchContext{}, err
	}
	rate, err := store.FindPricingRate(ctx, s.DB, identity.TenantID,
		sender.Country, sender.Channel, "")
	if err != nil {
		return batchContext{}, fmt.Errorf("sending: no rate for %s/%s",
			sender.Country, sender.Channel)
	}

	balance := int64(0)
	for _, entry := range balances {
		if entry.Currency == rate.Currency {
			balance = entry.BalanceMinor
		}
	}

	// The brand every message on this leg goes out under, and the path its
	// traffic takes. One sender per leg, so one of each.
	rcsCarrier, agentID := s.rcsPath(ctx, identity, sender)
	carrier, routeID := s.resolvePath(ctx, sender.Country, sender.Channel, rcsCarrier)

	return batchContext{
		sender: sender, templateID: templateID,
		templateStatus: template.Status, templateSender: template.SenderID.String(),
		template: template, body: templateText(template),
		rate: rate, tenantStatus: tenantStatus,
		balance: balance, campaignID: campaignID,
		carrier: carrier, routeID: routeID,
		rcsCarrier: rcsCarrier, agentID: agentID,
		variableMapping: mapping,
		skipped:         skipped,
	}, nil
}

// assignLegs decides which leg carries each contact on a page, or that nobody
// can.
//
// Order matters: the primary is tried first and the fallback only for a
// recipient the primary cannot serve, so a campaign never pays the fallback's
// price for somebody the primary would have reached.
func assignLegs(primary batchContext, fallback *batchContext,
	contacts []store.Contact) (forPrimary, forFallback []store.Contact) {

	for _, contact := range contacts {
		primaryMissing, primaryReaches := legVerdict(primary, contact)
		if primaryReaches && len(primaryMissing) == 0 {
			forPrimary = append(forPrimary, contact)
			continue
		}
		if fallback != nil {
			fallbackMissing, fallbackReaches := legVerdict(*fallback, contact)
			if fallbackReaches && len(fallbackMissing) == 0 {
				forFallback = append(forFallback, contact)
				continue
			}
			// Reachable somewhere, fillable nowhere. Report the primary's
			// missing slots when the primary could have reached them, because
			// that is the column the customer would fix; otherwise the
			// fallback's, which is the leg that was actually going to carry
			// them.
			if primaryReaches {
				primary.skipped.add(primaryMissing)
				continue
			}
			if fallbackReaches {
				primary.skipped.add(fallbackMissing)
				continue
			}
		} else if primaryReaches {
			primary.skipped.add(primaryMissing)
			continue
		}

		// No leg can reach them at all — not addressable on this channel, not
		// opted in, or suppressed since the page was read. Handed to the
		// primary leg rather than dropped here, so the gate records the refusal
		// with the reason it actually has. Dropping them would lose a row a
		// tenant asking "why didn't this arrive" is entitled to.
		forPrimary = append(forPrimary, contact)
	}
	return forPrimary, forFallback
}

// legVerdict is what stands between one contact and one leg: whether the leg
// can reach them at all, and which of its slots they have no value for.
func legVerdict(leg batchContext, contact store.Contact) ([]string, bool) {
	return legCarries(leg.sender.Channel, leg.template, leg.variableMapping, contact)
}

// legCarries is the same verdict without a resolved sender or route, so the
// estimate can ask it before a campaign exists. One rule for both: a count
// that disagreed with the send would be worse than no count, because it would
// be believed.
func legCarries(channel string, template store.Template, mapping map[string]string,
	contact store.Contact) ([]string, bool) {

	if !reachableOn(contact, channel) {
		return nil, false
	}
	return renderMessage(template, templateText(template), contact.Fields, mapping).missing, true
}

// reachableOn is the audience rule, in Go.
//
// It is the mirror of the reachableOnChannel SQL fragment, and has to stay one:
// the query decides who is in a campaign's audience and this decides which leg
// carries them, so a disagreement would page a contact in and then serve them
// with neither leg. Addressable on the channel, explicitly opted in on it, and
// not suppressed — an opt-in is usually older than the STOP that followed it,
// so suppression wins.
func reachableOn(contact store.Contact, channel string) bool {
	if channel == "EMAIL" {
		return contact.Email != nil && *contact.Email != "" &&
			contact.Consent[channel] == "opted_in" && !contact.EmailSuppressed
	}
	return contact.Msisdn != "" &&
		contact.Consent[channel] == "opted_in" && !contact.PhoneSuppressed
}

// fallbackLeg is a campaign's second leg, or nil when it has none. All three
// columns have to be set: a channel with no sender or no template is not a leg
// anything can be sent over.
func fallbackLeg(campaign store.Campaign) (channel string, senderID, templateID uuid.UUID, ok bool) {
	if campaign.FallbackChannel == nil || campaign.FallbackSenderID == nil ||
		campaign.FallbackTemplateID == nil {
		return "", uuid.Nil, uuid.Nil, false
	}
	return *campaign.FallbackChannel, *campaign.FallbackSenderID, *campaign.FallbackTemplateID, true
}

// legChannels is every channel this campaign's audience has to be paged on.
func legChannels(primary batchContext, fallback *batchContext) []string {
	channels := []string{primary.sender.Channel}
	if fallback != nil && fallback.sender.Channel != primary.sender.Channel {
		channels = append(channels, fallback.sender.Channel)
	}
	return channels
}
