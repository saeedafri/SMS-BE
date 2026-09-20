package sending

import (
	"context"
	"errors"
	"fmt"

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

// The template is passed whole rather than as its body, because an RCS
// template has no body: its words live in a card, and an estimate reading
// template.Body alone saw an empty string, found no slots in it and reported
// that nobody would be skipped — on the one channel where the carrier renders
// from the values we pass.
func (s *Service) EstimateCampaign(ctx context.Context, identity store.Identity,
	listID *uuid.UUID, country, channel string, template store.Template) (CampaignEstimate, error) {

	body := templateText(template)
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

	// Count the audience, not a page of it. A limit here would quietly
	// under-quote a large list, which is the one direction an estimate must
	// never be wrong in.
	// Only the contacts this channel can actually reach: an Email campaign
	// counts the ones with an email address, and every channel counts only the
	// ones who opted in on it. Counting the whole list quoted sends that could
	// never happen and charged the customer for agreeing to them.
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

	// The range, not a guess at one. This used to add 1 to the written count
	// when the body contained "{{" — right often enough to look correct, and
	// wrong twice over: the written count charges for braces no handset ever
	// sees, and a body forty septets under the boundary with three long
	// variables tips by two segments rather than one.
	minSegments, maxSegments := billing.SegmentBounds(body)

	// Who the template cannot be personalised for. Counted with the same rule
	// the fan-out skips by, walking the same list, so the number quoted before
	// the send is the number the send acts on. A count that disagreed with the
	// send would be worse than none, because it would be believed.
	skipped, byName, err := s.countUnpersonalisable(ctx, identity, listID, channel, template)
	if err != nil {
		return CampaignEstimate{}, err
	}
	if total -= skipped; total < 0 {
		total = 0
	}

	return CampaignEstimate{
		SuppressedExcluded:    suppressed,
		VariableSkipped:       skipped,
		VariableSkippedByName: byName,
		Recipients:            total,
		SegmentsPerMessageMin: minSegments,
		SegmentsPerMessageMax: maxSegments,
		CostMinorMin:          int64(total) * int64(minSegments) * rate.PerSegmentMinor,
		CostMinorMax:          int64(total) * int64(maxSegments) * rate.PerSegmentMinor,
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

	// Everything identical across recipients is resolved ONCE here. Doing it
	// per message cost eight Postgres round trips per recipient and capped
	// throughput at roughly 68 messages/second on this machine.
	sender, err := store.GetSenderID(ctx, s.DB, identity, campaign.SenderID)
	if err != nil {
		return 0, 0, err
	}
	template, err := store.GetTemplate(ctx, s.DB, identity, campaign.TemplateID)
	if err != nil {
		return 0, 0, err
	}
	body := templateText(template)
	rate, err := store.FindPricingRate(ctx, s.DB, identity.TenantID, sender.Country, sender.Channel, "")
	if err != nil {
		return 0, 0, fmt.Errorf("sending: no rate for %s/%s", sender.Country, sender.Channel)
	}
	tenantStatus, err := store.TenantStatus(ctx, s.DB, identity)
	if err != nil {
		return 0, 0, err
	}
	balance := int64(0)
	balances, err := store.ListWalletBalances(ctx, s.DB, identity)
	if err != nil {
		return 0, 0, err
	}
	for _, entry := range balances {
		if entry.Currency == rate.Currency {
			balance = entry.BalanceMinor
		}
	}

	// The path this campaign's traffic takes, resolved once with everything else
	// that is identical across recipients. See resolvePath in service.go.
	//
	// The brand every message in this campaign goes out under is resolved once
	// with it. A campaign has one sender and therefore one agent.
	rcsCarrier, agentID := s.rcsPath(ctx, identity, sender)
	carrier, routeID := s.resolvePath(ctx, sender.Country, sender.Channel, rcsCarrier)

	// The list's own columns, joined to the template's slots. Absent for a
	// campaign with no list, and an empty mapping is fine: a file whose headers
	// already match a template's slots resolves without one.
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

	campaignID := campaign.ID
	batch := batchContext{
		sender: sender, templateID: campaign.TemplateID,
		templateStatus: template.Status, templateSender: template.SenderID.String(),
		template: template,
		body:     body, rate: rate, tenantStatus: tenantStatus,
		balance: balance, campaignID: &campaignID,
		carrier: carrier, routeID: routeID,
		rcsCarrier: rcsCarrier, agentID: agentID,
		// Whatever the estimate said would be skipped must ACTUALLY be skipped
		// here, not merely counted there. The same mapping and the same rule,
		// so the two cannot drift.
		variableMapping: mapping,
		skipped:         &skipTally{},
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

		contacts, next, err := store.ListContactsAfter(ctx, s.DB, identity,
			campaign.ListID, sender.Channel, cursor, batchSize)
		if err != nil {
			return sent, failed, err
		}
		pageSent, pageFailed, err := s.SendBatch(ctx, identity, batch, contacts)
		sent += pageSent
		failed += pageFailed
		if err != nil {
			return sent, failed, err
		}
		// The running balance shrinks as the campaign spends, so a wallet that
		// runs dry stops the rest of the campaign instead of overdrawing.
		batch.balance -= int64(pageSent) * rate.PerSegmentMinor
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

// countUnpersonalisable walks a list and counts who the message cannot be
// filled for, by the same rule the fan-out skips by.
//
// A message with no slots anywhere returns zero without reading anything: that
// is the common case and it must not cost a walk of the audience. "Anywhere" is
// the fix — this used to ask only whether the BODY had slots, so an RCS
// template, whose body is null and whose {{first_name}} lives in a card,
// returned zero here while the send skipped nobody. Both numbers were wrong in
// the same direction, which is why they agreed.
func (s *Service) countUnpersonalisable(ctx context.Context, identity store.Identity,
	listID *uuid.UUID, channel string, template store.Template) (int, map[string]int, error) {

	body := templateText(template)
	if listID == nil || len(templateSlots(template, body)) == 0 {
		return 0, nil, nil
	}
	mapping, err := store.ListVariableMapping(ctx, s.DB, identity, *listID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return 0, nil, err
	}

	tally := &skipTally{}
	// Paged with the same cursor the fan-out uses, and narrowed to the contacts
	// this channel can actually reach — so the count explains the same audience
	// the estimate is quoting for, not a wider one.
	cursor := ""
	for {
		contacts, next, err := store.ListContactsAfter(
			ctx, s.DB, identity, listID, channel, cursor, estimateScanPage)
		if err != nil {
			return 0, nil, err
		}
		for _, contact := range contacts {
			rendered := renderMessage(template, body, contact.Fields, mapping)
			if len(rendered.missing) > 0 {
				tally.add(rendered.missing)
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return tally.Total, tally.ByName, nil
}

// estimateScanPage is how many contacts the estimate reads at a time. Larger
// than the console's page because this walk is ours and pays a round trip per
// page over a link where that is the dominant cost.
const estimateScanPage = 500
