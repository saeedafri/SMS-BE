package sending

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
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
// stored, echoed back by GET /v1/campaigns, and read by nothing.
//
// THE AUDIENCE IS THE PRIMARY CHANNEL'S. The fallback is a rescue for people
// already in it, never a way to add people to it: somebody opted in to SMS
// and not to RCS is not in an RCS campaign's audience, and a fallback does not
// bring them in. And the fallback needs its OWN consent — someone opted in to
// RCS and not SMS must not receive the SMS fallback. Consent is per channel,
// and a second leg is a second channel.

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

// legReason is why a recipient is not carried by the primary leg.
//
// It is the single seam a later reason plugs into. A delivery-timeout fallback
// ("sent, never delivered, try SMS") would be a third value here and a third
// arm in chooseLeg; nothing that dispatches changes. It is deliberately NOT
// built: with no RCS traffic on this deployment there is no delivery-time data
// to choose a timeout from, and a guessed timeout is how a platform
// double-sends to a whole list.
type legReason string

const (
	// reasonNone: the primary carries them.
	reasonNone legReason = ""
	// reasonNotReachable: the handset cannot receive the primary channel —
	// an RCS campaign to a phone the carrier says has no RCS.
	reasonNotReachable legReason = "not_reachable"
	// reasonUnfillable: the primary's content needs a value this contact has
	// no column for.
	reasonUnfillable legReason = "unfillable"
)

// legChoice is where one recipient goes, and why.
type legChoice struct {
	onFallback bool
	skipped    bool
	// excluded: not in this campaign's audience at all — not consented on the
	// primary channel, or suppressed. Not sent, not counted, no row: they were
	// never a recipient, so there is nothing to explain.
	excluded bool
	reason   legReason
	// missing is what the skip is counted under — the primary's slots, since
	// that is the column the customer can go and fill.
	missing []string
}

// chooseLeg is THE decision, for one recipient already in the audience. Every
// caller — the fan-out, the estimate — asks here, so the number quoted before
// a send is the number the send acts on.
//
// handsetReachable is the carrier's answer for the primary channel, or true
// when nobody could say: an unknown is not an unreachable, and treating it as
// one would reroute a whole campaign the first time a capability lookup times
// out.
func chooseLeg(primary legSpec, fallback *legSpec, contact store.Contact,
	handsetReachable bool) legChoice {

	primaryMissing, primaryConsented := legCarries(primary.channel, primary.template,
		primary.mapping, contact)

	// Not in the primary's audience. The page query already excludes them, so
	// this only fires when consent or a suppression changed after the page was
	// read — and it must EXCLUDE, not hand them to the primary: the send gate
	// checks suppression but not consent, so handing over somebody whose
	// consent was just withdrawn would send to them.
	if !primaryConsented {
		return legChoice{excluded: true}
	}

	reason := reasonNone
	switch {
	case len(primaryMissing) > 0:
		reason = reasonUnfillable
	case !handsetReachable:
		reason = reasonNotReachable
	}
	if reason == reasonNone {
		return legChoice{}
	}

	if fallback != nil {
		fallbackMissing, fallbackConsented := legCarries(fallback.channel,
			fallback.template, fallback.mapping, contact)
		if fallbackConsented && len(fallbackMissing) == 0 {
			return legChoice{onFallback: true, reason: reason}
		}
	}

	// No leg can carry them. For a handset the carrier says cannot take RCS
	// but whose message is complete, the primary is sent anyway: that is what
	// a campaign with no usable fallback has always done, a capability answer
	// can be stale, and the carrier's own verdict — recorded against the
	// message — is the truth. Only a message that cannot be FILLED is dropped,
	// because sending that one would put a hole in a sentence.
	if reason == reasonNotReachable {
		return legChoice{}
	}
	return legChoice{skipped: true, reason: reason, missing: primaryMissing}
}

// legSpec is one leg as the leg decision needs it: which channel, which
// content, and how a spreadsheet's columns map onto its slots. Enough to decide
// before any sender, route or price is resolved, which is what lets the
// estimate ask the same question as the fan-out.
type legSpec struct {
	channel  string
	template store.Template
	mapping  map[string]string
}

func (c batchContext) spec() legSpec {
	return legSpec{channel: c.sender.Channel, template: c.template, mapping: c.variableMapping}
}

// assignLegs splits one page of the audience between the legs.
func (s *Service) assignLegs(ctx context.Context, primary batchContext,
	fallback *batchContext, contacts []store.Contact) (forPrimary, forFallback []store.Contact) {

	var fallbackSpec *legSpec
	if fallback != nil {
		spec := fallback.spec()
		fallbackSpec = &spec
	}
	reachable := s.handsetsReachable(ctx, primary, fallback != nil, contacts)

	for _, contact := range contacts {
		choice := chooseLeg(primary.spec(), fallbackSpec, contact, reachable(contact))
		switch {
		case choice.excluded:
		case choice.skipped:
			primary.skipped.add(choice.missing)
		case choice.onFallback:
			forFallback = append(forFallback, contact)
		default:
			forPrimary = append(forPrimary, contact)
		}
	}
	return forPrimary, forFallback
}

// handsetsReachable asks the RCS carrier, once for the whole page, which of
// these handsets can receive RCS.
//
// Asked only when the answer could change anything: an RCS primary with a
// fallback to go to. Without a fallback an unreachable handset is sent the RCS
// exactly as before, so a lookup would be spend with no decision behind it.
//
// Every failure to answer means "reachable". No dedicated RCS gateway — the
// sandbox, which is this deployment today — no agent, a vendor error: in each
// case nobody knows, and unknown keeps the campaign on its own channel. The
// alternative moves a whole list onto the fallback the first time a vendor
// endpoint blinks.
func (s *Service) handsetsReachable(ctx context.Context, primary batchContext,
	hasFallback bool, contacts []store.Contact) func(store.Contact) bool {

	everyone := func(store.Contact) bool { return true }
	if !hasFallback || primary.sender.Channel != "RCS" || primary.agentID == "" {
		return everyone
	}
	gateway, dedicated := s.gatewayFor("RCS", primary.rcsCarrier)
	if !dedicated {
		return everyone
	}
	checker, ok := gateway.(connector.RCSCapabilityChecker)
	if !ok {
		return everyone
	}

	msisdns := make([]string, 0, len(contacts))
	for _, contact := range contacts {
		if contact.Msisdn != "" {
			msisdns = append(msisdns, contact.Msisdn)
		}
	}
	// One call per page, never one per contact. Airtel's bulk endpoint has a
	// floor of 500 and its adapter checks one at a time below it, so a
	// campaign's final short page costs up to that many single lookups —
	// bounded, and only on the last page.
	capable, err := checker.Reachable(ctx, primary.agentID, msisdns)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("rcs reachability lookup failed; keeping the page on rcs",
				"carrier", primary.rcsCarrier, "error", err)
		}
		return everyone
	}
	yes := make(map[string]bool, len(capable))
	for _, msisdn := range capable {
		yes[msisdn] = true
	}
	return func(contact store.Contact) bool { return yes[contact.Msisdn] }
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
