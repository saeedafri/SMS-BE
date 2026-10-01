package sending

import (
	"context"
	"errors"
	"time"

	"github.com/saeedafri/sms-be/internal/store"
)

// Fallback after a definite failure.
//
// legs.go chooses a leg before anything is sent. That cannot see a failure that
// only the carrier knows about: a handset that looked RCS-capable and bounced,
// a number the carrier then calls undeliverable. This is the other half. When
// the carrier reports a final failure for a message on a campaign's PRIMARY leg,
// the campaign's fallback leg sends that same recipient.
//
// Deliberately only on `undelivered`, a definite report. `expired` is the
// reconciler giving up on a message the carrier never answered, and acting on
// silence is how a platform sends a delivered message a second time, which is
// the risk legs.go names for a timeout fallback.
//
// It goes through SendBatch with the fallback leg, so it is the same gate, the
// same consent check, the same wallet hold and the same recording as a
// fallback chosen up front. The one thing it adds is the claim in
// fallback_sends, so a repeated report sends once.

func (s *Service) fallbackAfterFailure(ctx context.Context, identity store.Identity,
	failed store.MessageRecord) {

	if failed.CampaignID == nil || failed.DeliveredChannel == nil {
		return
	}
	run := func(ctx context.Context) {
		if err := s.sendFallback(ctx, identity, failed); err != nil && s.Logger != nil {
			s.Logger.Warn("fallback after failure did not send",
				"message", failed.ID, "campaign", *failed.CampaignID, "error", err.Error())
		}
	}
	if s.Async != nil {
		s.Async(run)
		return
	}
	run(ctx)
}

func (s *Service) sendFallback(ctx context.Context, identity store.Identity,
	failed store.MessageRecord) error {

	campaign, err := store.GetCampaign(ctx, s.DB, identity, *failed.CampaignID)
	if err != nil {
		return err
	}
	fallbackChannel, fallbackSender, fallbackTemplate, ok := fallbackLeg(campaign)
	if !ok {
		return nil
	}
	// Only the primary leg's failures. A failure on the fallback itself ends
	// here; there is no third leg to fall to.
	if *failed.DeliveredChannel != campaign.Channel || campaign.Channel == fallbackChannel {
		return nil
	}
	// A campaign someone stopped stays stopped.
	if campaign.Status == "cancelled" || campaign.Status == "paused" {
		return nil
	}

	claimed, err := store.ClaimFallback(ctx, s.DB, identity, failed.ID, campaign.ID)
	if err != nil || !claimed {
		return err
	}
	contact, err := store.ContactForLeg(ctx, s.DB, identity, campaign.ListID,
		failed.Msisdn, fallbackChannel)
	if errors.Is(err, store.ErrNotFound) {
		// No consent on the fallback channel, suppressed, or no longer in the
		// list. Not an error: the rules held. The claim stays so it is not retried.
		return nil
	}
	if err != nil {
		_ = store.ReleaseFallback(context.WithoutCancel(ctx), s.DB, identity, failed.ID)
		return err
	}

	release := func(cause error) error {
		_ = store.ReleaseFallback(context.WithoutCancel(ctx), s.DB, identity, failed.ID)
		return cause
	}
	tenantStatus, err := store.TenantStatus(ctx, s.DB, identity)
	if err != nil {
		return release(err)
	}
	mapping := map[string]string{}
	if campaign.ListID != nil {
		loaded, err := store.ListVariableMapping(ctx, s.DB, identity, *campaign.ListID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return release(err)
		}
		if loaded != nil {
			mapping = loaded
		}
	}
	campaignID := campaign.ID
	leg, err := s.resolveLeg(ctx, identity, &campaignID, fallbackSender, fallbackTemplate,
		tenantStatus, mapping, &skipTally{})
	if err != nil {
		return release(err)
	}
	leg.campaignChannel = campaign.Channel
	balances, err := store.ListWalletBalances(ctx, s.DB, identity)
	if err != nil {
		return release(err)
	}
	for _, entry := range balances {
		if entry.Currency == leg.rate.Currency {
			leg.balance = entry.BalanceMinor
		}
	}
	_, _, _, err = s.SendBatch(ctx, identity, leg, []store.Contact{contact})
	return err
}

// fallbackTimeout bounds one fallback send when it runs off the report path.
const fallbackTimeout = 60 * time.Second
