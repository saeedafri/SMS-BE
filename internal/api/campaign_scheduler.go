package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/saeedafri/sms-be/internal/domain/compliance"
	"github.com/saeedafri/sms-be/internal/store"
)

// LaunchDueCampaigns sends every scheduled campaign whose time has come.
//
// A promotional campaign scheduled outside its country's promotional hours is
// held: it is due at held_until, the next opening, and is not selected before.
//
// ponytail: a claimed campaign whose process dies before fan-out marks it
// sending stays queued; add a queued-too-long sweep if that is ever seen.
func (s *Server) LaunchDueCampaigns(ctx context.Context) error {
	service := s.sendingService(ctx)
	if service == nil || s.OperatorDB == nil {
		return nil
	}
	now := s.now().UTC()
	due, err := store.DueScheduledCampaigns(ctx, s.OperatorDB, now, 100)
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range due {
		identity := store.Identity{TenantID: entry.TenantID}
		campaign, err := store.GetCampaign(ctx, s.DB, identity, entry.ID)
		if err != nil {
			failures = append(failures, fmt.Errorf("campaign %s: %w", entry.ID, err))
			continue
		}
		template, err := store.GetTemplate(ctx, s.DB, identity, campaign.TemplateID)
		if err != nil {
			failures = append(failures, fmt.Errorf("campaign %s: %w", entry.ID, err))
			continue
		}
		if isPromotional(template) && !compliance.PromotionalAllowedAt(campaign.Country, now) {
			// held_until was computed wrong: this should not be selectable. Said
			// loudly, and launched anyway — the gate still refuses each message.
			s.Logger.Error("promotional campaign selected outside its window",
				"campaign", entry.ID, "held_until", campaign.HeldUntil)
			if s.Metrics != nil {
				s.Metrics.RecordIncident("held_until_wrong", entry.ID.String())
			}
		}
		claimed, err := store.ClaimScheduledCampaign(ctx, s.DB, identity, campaign.ID)
		if err != nil {
			failures = append(failures, fmt.Errorf("campaign %s: %w", entry.ID, err))
			continue
		}
		if !claimed {
			continue
		}
		sent, failed, err := service.LaunchCampaign(ctx, identity, campaign)
		if err != nil {
			failures = append(failures, fmt.Errorf("campaign %s: %w", entry.ID, err))
			continue
		}
		s.Logger.Info("launched scheduled campaign", "campaign", entry.ID,
			"sent", sent, "failed", failed)
	}
	return errors.Join(failures...)
}
