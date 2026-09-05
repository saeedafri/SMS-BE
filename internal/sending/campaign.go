package sending

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/billing"
	"github.com/saeedafri/sms-be/internal/store"
)

// CampaignEstimate is what the wizard shows before a user commits. It is a
// range rather than a single number because per-contact personalisation can
// change the segment count: a template with a {{name}} in it costs one segment
// for "Sam" and two for a longer name that tips it past the boundary.
type CampaignEstimate struct {
	Recipients         int
	SegmentsPerMessage int
	CostMinorMin       int64
	CostMinorMax       int64
	Currency           string
}

// EstimateCampaign prices a campaign against its real audience.
//
// The estimate is stored on the campaign at creation and never recomputed,
// because the user approved a specific number: a rate change afterwards must
// not silently rewrite what they agreed to.
// ErrNoRate means this corridor has no price yet. It is a configuration gap,
// not a fault, and the API answers it with a sentence the customer can act on.
var ErrNoRate = errors.New("sending: no rate for corridor")

func (s *Service) EstimateCampaign(ctx context.Context, identity store.Identity,
	listID *uuid.UUID, country, channel, body, category string) (CampaignEstimate, error) {

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

	segments := billing.SegmentCount(body)
	if segments < 1 {
		segments = 1
	}
	// The upper bound assumes every personalised message tips into one more
	// segment. Quoting the optimistic number and then charging more is how
	// billing surprises happen.
	maxSegments := segments
	if strings.Contains(body, "{{") {
		maxSegments = segments + 1
	}

	return CampaignEstimate{
		Recipients:         total,
		SegmentsPerMessage: segments,
		CostMinorMin:       int64(total) * int64(segments) * rate.PerSegmentMinor,
		CostMinorMax:       int64(total) * int64(maxSegments) * rate.PerSegmentMinor,
		Currency:           rate.Currency,
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
	body := ""
	if template.Body != nil {
		body = *template.Body
	}
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
	carrier, routeID := s.resolvePath(ctx, sender.Country, sender.Channel)

	campaignID := campaign.ID
	batch := batchContext{
		sender: sender, templateID: campaign.TemplateID,
		templateStatus: template.Status, templateSender: template.SenderID.String(),
		template: template,
		body:     body, rate: rate, tenantStatus: tenantStatus,
		balance: balance, campaignID: &campaignID,
		carrier: carrier, routeID: routeID,
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
			campaign.ListID, cursor, batchSize)
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

// personalise substitutes {{field}} placeholders from the contact's own fields.
// An unknown placeholder is left as-is rather than blanked: sending "Hi {{name}}"
// is an obvious bug a user will report, while sending "Hi " looks deliberate and
// ships to the whole list unnoticed.
func personalise(body string, contact store.Contact) string {
	if !strings.Contains(body, "{{") {
		return body
	}
	for key, value := range contact.Fields {
		body = strings.ReplaceAll(body, "{{"+key+"}}", value)
	}
	return body
}
