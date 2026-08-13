package sending

import (
	"context"
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
func (s *Service) EstimateCampaign(ctx context.Context, identity store.Identity,
	listID *uuid.UUID, country, channel, body string) (CampaignEstimate, error) {

	rate, err := store.FindPricingRate(ctx, s.DB, country, channel, "")
	if err != nil {
		return CampaignEstimate{}, fmt.Errorf("sending: no rate for %s/%s", country, channel)
	}

	// Count the audience, not a page of it. A limit here would quietly
	// under-quote a large list, which is the one direction an estimate must
	// never be wrong in.
	_, total, _, err := store.ListContacts(ctx, s.DB, identity, listID, "", 1)
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
	rate, err := store.FindPricingRate(ctx, s.DB, sender.Country, sender.Channel, "")
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

	campaignID := campaign.ID
	context := batchContext{
		sender: sender, templateID: campaign.TemplateID,
		templateStatus: template.Status, templateSender: template.SenderID.String(),
		body: body, rate: rate, tenantStatus: tenantStatus,
		balance: balance, campaignID: &campaignID,
	}

	if err := store.MarkCampaignSending(ctx, s.DB, identity, campaign.ID); err != nil {
		return 0, 0, err
	}

	// Paged so a million-contact list never has to fit in memory at once.
	cursor := ""
	for {
		contacts, _, next, err := store.ListContacts(ctx, s.DB, identity,
			campaign.ListID, cursor, batchSize)
		if err != nil {
			return sent, failed, err
		}
		pageSent, pageFailed, err := s.SendBatch(ctx, identity, context, contacts)
		sent += pageSent
		failed += pageFailed
		if err != nil {
			return sent, failed, err
		}
		// The running balance shrinks as the campaign spends, so a wallet that
		// runs dry stops the rest of the campaign instead of overdrawing.
		context.balance -= int64(pageSent) * rate.PerSegmentMinor
		if next == "" {
			break
		}
		cursor = next
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
