package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/saeedafri/sms-be/internal/domain/compliance"
	"github.com/saeedafri/sms-be/internal/store"
)

// LaunchDueCampaigns sends every scheduled campaign whose time has come.
//
// A promotional campaign due outside its country's promotional hours stays
// scheduled and goes at the next opening: launching it would have every message
// refused at the gate, which honours the rule and wastes the campaign.
//
// ponytail: a claimed campaign whose process dies before fan-out marks it
// sending stays queued; add a queued-too-long sweep if that is ever seen.
func (s *Server) LaunchDueCampaigns(ctx context.Context) error {
	service := s.sendingService(ctx)
	if service == nil || s.OperatorDB == nil {
		return nil
	}
	now := time.Now().UTC()
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
		if template.DltCategory != nil && *template.DltCategory == "PROMOTIONAL" &&
			!compliance.PromotionalAllowedAt(campaign.Country, now) {
			continue
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
