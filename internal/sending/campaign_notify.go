package sending

import (
	"context"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// CampaignNotifier is how the send path tells a live screen that a campaign
// moved. Both calls are nudges, never data: the screen re-fetches the campaign
// itself. Implementations must be fire-and-forget — a campaign that cannot
// notify is still a campaign, and must never fail because of it.
type CampaignNotifier interface {
	// CampaignStatusChanged: the campaign's status moved. Delivered at once.
	CampaignStatusChanged(ctx context.Context, tenantID, campaignID uuid.UUID)
	// CampaignProgressed: the campaign's message counts moved. May be called once
	// per message, so the implementation is responsible for coalescing.
	CampaignProgressed(ctx context.Context, tenantID, campaignID uuid.UUID)
}

func (s *Service) campaignStatusChanged(ctx context.Context, identity store.Identity,
	campaignID uuid.UUID) {

	if s.Notifier != nil {
		s.Notifier.CampaignStatusChanged(ctx, identity.TenantID, campaignID)
	}
}

// campaignProgressed is a no-op for a message that belongs to no campaign.
func (s *Service) campaignProgressed(ctx context.Context, identity store.Identity,
	campaignID *uuid.UUID) {

	if s.Notifier != nil && campaignID != nil {
		s.Notifier.CampaignProgressed(ctx, identity.TenantID, *campaignID)
	}
}
