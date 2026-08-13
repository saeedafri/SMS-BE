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

// LaunchCampaign fans a campaign out to its list.
//
// Every recipient goes through Service.Send — the same gate, the same hold, the
// same settlement as a single API send. Nothing here re-implements those rules,
// because a campaign path that drifts from the single-send path is how a
// suppressed contact eventually gets messaged.
func (s *Service) LaunchCampaign(ctx context.Context, identity store.Identity,
	campaign store.Campaign) (sent int, failed int, err error) {

	template, err := store.GetTemplate(ctx, s.DB, identity, campaign.TemplateID)
	if err != nil {
		return 0, 0, err
	}
	body := ""
	if template.Body != nil {
		body = *template.Body
	}

	if err := store.MarkCampaignSending(ctx, s.DB, identity, campaign.ID); err != nil {
		return 0, 0, err
	}

	// Paged so a million-contact list does not have to fit in memory at once.
	cursor := ""
	for {
		contacts, _, next, err := store.ListContacts(ctx, s.DB, identity, campaign.ListID, cursor, 200)
		if err != nil {
			return sent, failed, err
		}
		for _, contact := range contacts {
			campaignID := campaign.ID
			_, sendErr := s.Send(ctx, identity, SendRequest{
				SenderID:   campaign.SenderID,
				TemplateID: &campaign.TemplateID,
				Msisdn:     contact.Msisdn,
				Body:       personalise(body, contact),
				CampaignID: &campaignID,
			})
			// A refused recipient is a failed recipient, not a failed campaign.
			// One suppressed contact must not stop the other 999,999 — and the
			// refusal is already recorded against the message, so the user can
			// still see exactly why it did not go.
			if sendErr != nil {
				failed++
				continue
			}
			sent++
		}
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
