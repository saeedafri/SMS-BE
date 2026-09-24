package sending

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/billing"
	"github.com/saeedafri/sms-be/internal/store"
)

// CampaignEstimate is what the wizard shows before a user commits. It is a
// range rather than a single number because per-contact personalisation can
// change the segment count: a template with a {{name}} in it costs one segment
// for "Sam" and two for a longer name that tips it past the boundary.
type CampaignEstimate struct {
	Recipients            int
	SegmentsPerMessageMin int
	SegmentsPerMessageMax int
	CostMinorMin          int64
	CostMinorMax          int64
	Currency              string
	// VariableSkipped counts the reachable contacts this template cannot be
	// personalised for, and VariableSkippedByName says which slot each was
	// missing. Recipients and both costs are already REDUCED by it: quoting for
	// somebody who is never sent to is the same defect as sending them a hole
	// in a sentence, seen from the billing side.
	//
	// Counted only when there is a list to walk. A body with no slots skips
	// nobody, which is the common case and costs nothing to answer.
	VariableSkipped       int
	VariableSkippedByName map[string]int

	// FallbackEligible is how many of the audience COULD be carried by the
	// fallback: one is configured, and they are addressable and consented on
	// its channel. A capacity, not a prediction — contrast FallbackForced.
	FallbackEligible int

	// FallbackForced is how many of Recipients will CERTAINLY be carried by the
	// fallback leg: the primary could not reach them or could not be filled for
	// them, and the fallback can. Distinct from how many COULD land there,
	// which is a capacity rather than a prediction.
	//
	// They are priced at the FALLBACK rate at both ends of the range. Once a
	// recipient is known to route through the second leg, the cheap end of the
	// primary's price is no longer reachable for them, and averaging the two
	// would quote a number no recipient will actually cost.
	FallbackForced int

	// SuppressedExcluded is how many of the list's otherwise-reachable contacts
	// have opted out of being contacted. Recipients is already reduced by it,
	// the same way it is reduced by VariableSkipped — this is the sentence that
	// explains the drop, without which a smaller number than the file the
	// customer just uploaded reads as a fault in the estimate.
	SuppressedExcluded int
}

// EstimateCampaign prices a campaign against its real audience.
//
// The estimate is stored on the campaign at creation and never recomputed,
// because the user approved a specific number: a rate change afterwards must
// not silently rewrite what they agreed to.
// ErrNoRate means this corridor has no price yet. It is a configuration gap,
// not a fault, and the API answers it with a sentence the customer can act on.
var ErrNoRate = errors.New("sending: no rate for corridor")

// ErrNoFallbackRate is ErrNoRate for the fallback leg, told apart so the
// sentence can name the leg to go and price. A fallback with no rate is caught
// here, at estimate time, rather than discovered when the campaign launches
// and the second leg cannot be resolved.
var ErrNoFallbackRate = fmt.Errorf("%w (fallback leg)", ErrNoRate)

// FallbackEstimate is a campaign's second leg as the wizard describes it,
// before any campaign row exists to read it from.
type FallbackEstimate struct {
	Channel  string
	Template store.Template
}

// The template is passed whole rather than as its body, because an RCS
// template has no body: its words live in a card, and an estimate reading
// template.Body alone saw an empty string, found no slots in it and reported
// that nobody would be skipped — on the one channel where the carrier renders
// from the values we pass.
//
// sendingAt is when the campaign will actually go out: its scheduled time, or
// now for one being sent immediately. It exists for the daily ceiling below —
// quoting a campaign scheduled for next week against THIS afternoon's spent
// allowance would clip it to nothing for a day it is not going to run on.
func (s *Service) EstimateCampaign(ctx context.Context, identity store.Identity,
	listID *uuid.UUID, country, channel string, template store.Template,
	fallback *FallbackEstimate, sendingAt time.Time) (CampaignEstimate, error) {

	category := ""
	if template.Category != nil {
		category = *template.Category
	}
	rate, err := store.FindPricingRate(ctx, s.DB, identity.TenantID, country, channel, category)
	if err != nil {
		// Wrapped so the caller can tell "we have no price for this corridor"
		// — a real, explainable answer — from a database failure. It used to
		// collapse both into one opaque error that the API turned into a 500,
		// so an unpriced corridor read to the customer as "something broke".
		return CampaignEstimate{}, fmt.Errorf("%w: %s/%s", ErrNoRate, country, channel)
	}

	fallbackRate := rate
	if fallback != nil {
		fallbackCategory := ""
		if fallback.Template.Category != nil {
			fallbackCategory = *fallback.Template.Category
		}
		fallbackRate, err = store.FindPricingRate(ctx, s.DB, identity.TenantID,
			country, fallback.Channel, fallbackCategory)
		if err != nil {
			return CampaignEstimate{}, fmt.Errorf("%w: %s/%s", ErrNoFallbackRate, country, fallback.Channel)
		}
	}

	// The audience is the PRIMARY channel's. A fallback rescues people already
	// in it; it never adds anyone, so it never widens this count.
	total, err := store.ReachableOnChannel(ctx, s.DB, identity, listID, channel)
	if err != nil {
		return CampaignEstimate{}, err
	}
	// Already excluded from total above — counted here only so the screen can
	// say why. Suppression overrides consent wherever the two disagree: an
	// opt-in is usually older than the STOP that followed it.
	suppressed, err := store.SuppressedOnChannel(ctx, s.DB, identity, listID, channel)
	if err != nil {
		return CampaignEstimate{}, err
	}

	// Who each leg actually carries, by the same rule the fan-out assigns by,
	// walking the same list. A count that disagreed with the send would be
	// worse than none, because it would be believed.
	split, err := s.splitAudience(ctx, identity, listID, channel, template, fallback, total)
	if err != nil {
		return CampaignEstimate{}, err
	}

	// The daily ceiling, applied to the QUOTE and not only to the send.
	//
	// A customer who approves a hundred thousand and is handed seventy has been
	// told one number and given another. Clipping here means the number they
	// approve IS the number that goes out, so the campaign's recipient count,
	// its delivery rate and its invoice all agree about it afterwards — and no
	// money is ever held for a recipient the ceiling was always going to keep.
	allowance, err := store.ReadSendAllowance(ctx, s.DB, identity, sendingAt)
	if err != nil {
		return CampaignEstimate{}, err
	}
	// The share of this send the tenant may have, applied before the day's
	// ceiling: a send is first cut to its percentage, and whatever survives
	// still has to fit inside what is left of the day.
	if allowance.ShareCapped() {
		total := split.primary + split.fallbackForced
		exempt, err := store.CountAlwaysSendOnChannel(ctx, s.DB, identity, listID, channel)
		if err != nil {
			return CampaignEstimate{}, err
		}
		// Rounded up, and never below the contacts no cap may withhold — a send
		// whose exempt outnumber its share is quoted at the exempt count,
		// because that is what will actually go out.
		allowed := max((total*allowance.Share()+99)/100, exempt)
		split.primary, split.fallbackForced = clipToRoom(split.primary, split.fallbackForced,
			min(allowed, total))
	}
	if allowance.Capped() {
		split.primary, split.fallbackForced = clipToRoom(split.primary, split.fallbackForced,
			allowance.Room(split.primary+split.fallbackForced))
	}

	// The range, not a guess at one. This used to add 1 to the written count
	// when the body contained "{{" — right often enough to look correct, and
	// wrong twice over: the written count charges for braces no handset ever
	// sees, and a body forty septets under the boundary with three long
	// variables tips by two segments rather than one.
	minSegments, maxSegments := billing.SegmentBounds(templateText(template))
	costMin := int64(split.primary) * int64(minSegments) * rate.PerSegmentMinor
	costMax := int64(split.primary) * int64(maxSegments) * rate.PerSegmentMinor

	if split.fallbackForced > 0 && fallback != nil {
		// Priced at the fallback's rate at BOTH ends. These recipients are not
		// "maybe cheaper on the primary" — the primary cannot carry them, so
		// the primary's price is not reachable for them at either end of the
		// range.
		fallbackMin, fallbackMax := billing.SegmentBounds(templateText(fallback.Template))
		costMin += int64(split.fallbackForced) * int64(fallbackMin) * fallbackRate.PerSegmentMinor
		costMax += int64(split.fallbackForced) * int64(fallbackMax) * fallbackRate.PerSegmentMinor
		// The quoted per-message range has to span both legs, or the screen
		// shows a segment range no fallback recipient falls inside.
		minSegments = min(minSegments, fallbackMin)
		maxSegments = max(maxSegments, fallbackMax)
	}

	return CampaignEstimate{
		SuppressedExcluded:    suppressed,
		VariableSkipped:       split.skipped,
		VariableSkippedByName: split.byName,
		FallbackForced:        split.fallbackForced,
		FallbackEligible:      split.fallbackEligible,
		Recipients:            split.primary + split.fallbackForced,
		SegmentsPerMessageMin: minSegments,
		SegmentsPerMessageMax: maxSegments,
		CostMinorMin:          costMin,
		CostMinorMax:          costMax,
		Currency:              rate.Currency,
	}, nil
}

// LaunchCampaign fans a campaign out to its list, one page at a time.
//
// Every recipient still passes the SAME gate with the same rules as a single
// API send — the batching changes how many round trips that costs, never what
// is checked. A campaign path that relaxed the rules is how a suppressed
// contact eventually gets messaged.
func (s *Service) LaunchCampaign(ctx context.Context, identity store.Identity,
	campaign store.Campaign) (sent int, failed int, err error) {

	// Everything identical across recipients is resolved ONCE per leg. Doing it
	// per message cost eight Postgres round trips per recipient and capped
	// throughput at roughly 68 messages/second on this machine.
	tenantStatus, err := store.TenantStatus(ctx, s.DB, identity)
	if err != nil {
		return 0, 0, err
	}
	balances, err := store.ListWalletBalances(ctx, s.DB, identity)
	if err != nil {
		return 0, 0, err
	}
	// ONE running balance per currency, shared by both legs. Handing each leg
	// its own copy of the snapshot let a two-leg campaign pass its own guard
	// twice over — each leg believing it had the whole wallet — and the ledger
	// then refused a hold part-way, which aborted the campaign instead of
	// refusing the recipients it could not afford, one by one, at cost 0.
	wallet := map[string]int64{}
	for _, entry := range balances {
		wallet[entry.Currency] = entry.BalanceMinor
	}

	// The daily ceiling, read once and spent down page by page like the wallet
	// above it. The estimate this campaign was approved at was already clipped
	// to it, so in the ordinary case the list runs out before the ceiling does
	// and this changes nothing. It bites when the allowance moved in between —
	// a second campaign ran, or this one was scheduled days ago.
	allowance, err := store.ReadSendAllowance(ctx, s.DB, identity, s.now())
	if err != nil {
		return 0, 0, err
	}

	// The list's own columns, joined to the template's slots. Absent for a
	// campaign with no list, and an empty mapping is fine: a file whose headers
	// already match a template's slots resolves without one. Shared by both
	// legs — it is a fact about the spreadsheet, not about a channel.
	mapping := map[string]string{}
	if campaign.ListID != nil {
		loaded, err := store.ListVariableMapping(ctx, s.DB, identity, *campaign.ListID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return 0, 0, err
		}
		if loaded != nil {
			mapping = loaded
		}
	}

	// Whatever the estimate said would be skipped must ACTUALLY be skipped
	// here, not merely counted there. One tally across both legs and every
	// page: a contact is skipped once, for the whole campaign, not once per
	// leg that could not carry them.
	skipped := &skipTally{}
	campaignID := campaign.ID

	batch, err := s.resolveLeg(ctx, identity, &campaignID, campaign.SenderID,
		campaign.TemplateID, tenantStatus, mapping, skipped)
	if err != nil {
		return 0, 0, err
	}

	// The second leg, for the recipients the primary cannot serve. Nil for a
	// campaign that has none, which is the common case and costs nothing.
	var fallback *batchContext
	if _, fallbackSender, fallbackTemplate, ok := fallbackLeg(campaign); ok {
		leg, err := s.resolveLeg(ctx, identity, &campaignID, fallbackSender,
			fallbackTemplate, tenantStatus, mapping, skipped)
		if err != nil {
			return 0, 0, err
		}
		// Filed under the campaign's own channel; what carried it is recorded
		// separately as delivered_channel.
		leg.campaignChannel = batch.sender.Channel
		fallback = &leg
	}

	if err := store.MarkCampaignSending(ctx, s.DB, identity, campaign.ID); err != nil {
		return 0, 0, err
	}

	// From here on the campaign is 'sending', and every early return below has
	// to move it off that status. Without this, one failed page — a contact
	// query that errors, a ClickHouse blip — left the campaign sending forever
	// with nothing running to finish it, and the customer saw a send that never
	// ended. The status matches what actually left: some messages out is a send
	// that happened, none out is one that did not.
	//
	// WithoutCancel because the common cause of the early return is the request
	// context going away, and the landing write must still happen.
	// halted is set when the loop stopped because someone hit the brake. The
	// landing writes below must then leave the status alone: a campaign that
	// was paused and is overwritten with 'sent' has had its brake silently
	// undone, and one overwritten with 'failed' is reported as broken when it
	// was stopped on purpose.
	halted := false

	// What the daily ceiling took off the page in hand, carried to the usage
	// write at the end of the page and reset there.
	withheld := 0

	defer func() {
		if err == nil || halted {
			return
		}
		status := "sent"
		if sent == 0 {
			status = "failed"
		}
		if landErr := store.SetCampaignStatus(context.WithoutCancel(ctx), s.DB,
			identity, campaign.ID, status); landErr != nil && s.Logger != nil {
			s.Logger.Warn("campaign left sending", "campaign", campaign.ID, "error", landErr)
		}
	}()

	// Paged so a million-contact list never has to fit in memory at once, and
	// resumed from where a pause stopped rather than from the top.
	cursor := campaign.DispatchCursor
	for {
		// The brake, checked between pages.
		//
		// Fan-out is a loop inside a request, so the only thing that can stop
		// it is another request changing the row underneath it. One indexed
		// read per five hundred recipients is what "no further recipient is
		// dispatched" costs. A page already handed to a carrier cannot be
		// recalled and is not meant to be — the invariant is that nothing NEW
		// leaves after the halt commits.
		status, statusErr := store.CampaignStatus(ctx, s.DB, identity, campaign.ID)
		if statusErr != nil {
			return sent, failed, statusErr
		}
		if status == "paused" || status == "cancelled" {
			halted = true
			// Where to resume from. Written even on cancel: it costs nothing
			// and it is the record of how far the campaign actually got.
			if saveErr := store.SaveDispatchCursor(context.WithoutCancel(ctx), s.DB,
				identity, campaign.ID, cursor); saveErr != nil && s.Logger != nil {
				s.Logger.Warn("campaign halted without recording its cursor",
					"campaign", campaign.ID, "error", saveErr)
			}
			return sent, failed, nil
		}

		// The audience spans BOTH legs' channels. Walking only the primary's is
		// what made a fallback decorative: the people it exists to reach were
		// never paged in.
		// The audience is the PRIMARY channel's. The fallback rescues people
		// already in it and never adds anyone: paging the union of both
		// channels sent the SMS fallback to people who had consented to SMS
		// and never to this campaign's RCS.
		contacts, next, err := store.ListContactsAfter(ctx, s.DB, identity,
			campaign.ListID, cursor, batchSize, batch.sender.Channel)
		if err != nil {
			return sent, failed, err
		}

		// The ceiling, applied before anyone is priced or held for. A withheld
		// recipient gets no message row, no wallet hold and no error code —
		// they are simply not dispatched, the same non-event as a contact this
		// channel cannot address.
		//
		// withheld counts only what was clipped off a page we had already read.
		// When the ceiling stops the loop outright the untouched tail is not
		// counted, because counting it would mean walking the rest of the list
		// to learn a number nothing depends on.
		// The share, applied per page. Exempt contacts are carried whatever the
		// cap says; the rest fill the remaining room least-recently-carried
		// first, so a repeated send reaches different people rather than
		// cutting the same tail of the list every time.
		var carried []store.Contact
		if allowance.ShareCapped() {
			var cut []store.Contact
			contacts, cut = chooseUnderCap(contacts, allowance.Share())
			withheld += len(cut)
			carried = contacts
		}

		if allowance.Capped() {
			room := allowance.Room(len(contacts))
			if room <= 0 {
				break
			}
			if room < len(contacts) {
				withheld += len(contacts) - room
				contacts = contacts[:room]
				carried = contacts
			}
		}

		forPrimary, forFallback := s.assignLegs(ctx, batch, fallback, contacts)
		// Each leg reads the SHARED balance and spends it down by what it
		// actually took, so a wallet that runs dry refuses the rest of the
		// campaign recipient by recipient instead of overdrawing.
		batch.balance = wallet[batch.rate.Currency]
		fallbackSent := 0
		pageSent, pageFailed, pageSpent, err := s.SendBatch(ctx, identity, batch, forPrimary)
		sent += pageSent
		failed += pageFailed
		wallet[batch.rate.Currency] -= pageSpent
		if err != nil {
			return sent, failed, err
		}
		if fallback != nil && len(forFallback) > 0 {
			fallback.balance = wallet[fallback.rate.Currency]
			legSent, legFailed, legSpent, legErr := s.SendBatch(ctx, identity, *fallback, forFallback)
			fallbackSent = legSent
			sent += legSent
			failed += legFailed
			wallet[fallback.rate.Currency] -= legSpent
			if legErr != nil {
				return sent, failed, legErr
			}
		}
		// Who this capped send SELECTED, so the next one starts with them at the
		// back of the queue. Selected rather than delivered on purpose: a contact
		// the gate then refuses still had their turn, and counting it otherwise
		// would hand them the front of the queue forever.
		if allowance.ShareCapped() && len(carried) > 0 {
			ids := make([]uuid.UUID, 0, len(carried))
			for _, contact := range carried {
				ids = append(ids, contact.ID)
			}
			if err := store.MarkCarriedUnderCap(ctx, s.DB, identity, ids, s.now()); err != nil {
				return sent, failed, err
			}
		}
		if allowance.ShareCapped() && !allowance.Capped() && withheld > 0 {
			if err := store.RecordSendUsage(ctx, s.DB, identity, allowance.Day,
				pageSent+fallbackSent, withheld); err != nil {
				return sent, failed, err
			}
			withheld = 0
		}
		if allowance.Capped() {
			allowance.Spend(pageSent + fallbackSent)
			if err := store.RecordSendUsage(ctx, s.DB, identity, allowance.Day,
				pageSent+fallbackSent, withheld); err != nil {
				return sent, failed, err
			}
			withheld = 0
		}
		if next == "" {
			break
		}
		cursor = next
		// Persisted per page rather than only at a halt, so a campaign that
		// dies mid-fan-out — a crash, a ClickHouse blip — resumes from the last
		// page it finished instead of re-sending everyone before it.
		if saveErr := store.SaveDispatchCursor(ctx, s.DB, identity,
			campaign.ID, cursor); saveErr != nil {
			return sent, failed, saveErr
		}
	}

	status := "sent"
	if sent == 0 && failed > 0 {
		status = "failed"
	}
	if err := store.SetCampaignStatus(ctx, s.DB, identity, campaign.ID, status); err != nil {
		return sent, failed, err
	}
	return sent, failed, nil
}

// clipToRoom reduces both legs of a campaign to what the daily ceiling admits.
//
// In proportion, because the fan-out stops at a page boundary and a page
// carries both legs — they shrink together. Emptying one leg first would quote
// a campaign whose shape nobody is ever going to send.
func clipToRoom(primary, fallbackForced, room int) (int, int) {
	total := primary + fallbackForced
	if total == 0 || room >= total {
		return primary, fallbackForced
	}
	if room <= 0 {
		return 0, 0
	}
	clipped := fallbackForced * room / total
	return room - clipped, clipped
}

// audienceSplit is who each leg carries, and who neither can.
type audienceSplit struct {
	primary          int
	fallbackForced   int
	fallbackEligible int
	skipped          int
	byName           map[string]int
}

// splitAudience walks a list and puts every contact through chooseLeg — the
// function the fan-out uses — so the quote and the send cannot disagree.
//
// Handset reachability is NOT asked here: the estimate would otherwise query a
// carrier for every number on a list before anyone has decided to send. It
// reports what is CERTAIN (fallbackForced: the primary cannot be filled for
// them) and what is POSSIBLE (fallbackEligible), and leaves the reachability
// share to the send, where the lookup is one call per page.
//
// The fast path matters: a single-leg campaign whose message has no slots
// anywhere carries everyone, and answering that must not cost a walk.
func (s *Service) splitAudience(ctx context.Context, identity store.Identity,
	listID *uuid.UUID, channel string, template store.Template,
	fallback *FallbackEstimate, total int) (audienceSplit, error) {

	if listID == nil ||
		(fallback == nil && len(templateSlots(template, templateText(template))) == 0) {
		return audienceSplit{primary: total}, nil
	}
	mapping, err := store.ListVariableMapping(ctx, s.DB, identity, *listID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return audienceSplit{}, err
	}

	primary := legSpec{channel: channel, template: template, mapping: mapping}
	var fallbackSpec *legSpec
	if fallback != nil {
		fallbackSpec = &legSpec{channel: fallback.Channel, template: fallback.Template,
			mapping: mapping}
	}

	tally := &skipTally{}
	split := audienceSplit{}
	cursor := ""
	for {
		contacts, next, err := store.ListContactsAfter(ctx, s.DB, identity, listID,
			cursor, estimateScanPage, channel)
		if err != nil {
			return audienceSplit{}, err
		}
		for _, contact := range contacts {
			if fallback != nil && reachableOn(contact, fallback.Channel) {
				split.fallbackEligible++
			}
			choice := chooseLeg(primary, fallbackSpec, contact, true)
			switch {
			case choice.excluded:
			case choice.skipped:
				tally.add(choice.missing)
			case choice.onFallback:
				split.fallbackForced++
			default:
				split.primary++
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	split.skipped, split.byName = tally.Total, tally.ByName
	return split, nil
}

// estimateScanPage is how many contacts the estimate reads at a time. Larger
// than the console's page because this walk is ours and pays a round trip per
// page over a link where that is the dominant cost.
const estimateScanPage = 500
