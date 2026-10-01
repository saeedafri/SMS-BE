package sending_test

import (
	"context"
	"testing"
	"time"

	"github.com/saeedafri/sms-be/internal/store"
)

func (f *fixture) dripCampaign(batch, interval, audience int) store.Campaign {
	f.t.Helper()
	templateID := f.seedSMSTemplate(f.senderID, "Your order has shipped.")
	listID, _ := f.seedList("Drip "+time.Now().Format("150405.000000"), audience)
	campaign := f.seedCampaign(templateID, listID, "queued", audience)
	f.exec(`UPDATE campaigns SET drip_batch_size = $2, drip_interval_minutes = $3 WHERE id = $1`,
		campaign.ID, batch, interval)
	loaded, err := store.GetCampaign(context.Background(), f.service.DB, f.identity, campaign.ID)
	if err != nil {
		f.t.Fatalf("reload: %v", err)
	}
	return loaded
}

// A drip campaign sends one instalment, parks itself as scheduled with its
// cursor kept, and each time the scheduler comes back it sends the next, until
// the list is done. Nobody is sent twice and nobody is missed.
func TestADripCampaignSendsInstalmentsUntilTheListIsDone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	campaign := f.dripCampaign(5, 10, 12)

	var totals []int
	for round := 1; round <= 4; round++ {
		sent, _, err := f.service.LaunchCampaign(ctx, f.identity, campaign)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		totals = append(totals, sent)
		status := f.campaignStatus(campaign.ID)
		if status == "sent" {
			break
		}
		if status != "scheduled" {
			t.Fatalf("round %d left the campaign %q, want scheduled between instalments", round, status)
		}
		parked, err := store.GetCampaign(ctx, f.service.DB, f.identity, campaign.ID)
		if err != nil {
			t.Fatal(err)
		}
		if wait := time.Until(*parked.ScheduledAt); wait < 9*time.Minute || wait > 11*time.Minute {
			t.Errorf("round %d next instalment in %s, want about 10 minutes", round, wait)
		}
		if parked.DispatchCursor == "" || parked.HeldUntil != nil {
			t.Errorf("round %d parked with cursor %q held %v", round, parked.DispatchCursor, parked.HeldUntil)
		}
		// The scheduler claims it (scheduled -> queued) and relaunches.
		if claimed, err := store.ClaimScheduledCampaign(ctx, f.service.DB, f.identity, campaign.ID); err != nil || !claimed {
			t.Fatalf("round %d claim = %v %v", round, claimed, err)
		}
		campaign = parked
	}
	if len(totals) != 3 || totals[0] != 5 || totals[1] != 5 || totals[2] != 2 {
		t.Errorf("instalments = %v, want [5 5 2]", totals)
	}
	if got := f.campaignMessageCount(campaign.ID); got != 12 {
		t.Errorf("12 contacts produced %d messages: someone was missed or sent twice", got)
	}
	if got := f.campaignStatus(campaign.ID); got != "sent" {
		t.Errorf("finished as %q, want sent", got)
	}
}

// A list that fits in one instalment is an ordinary send.
func TestADripCampaignThatFitsInOneInstalmentJustSends(t *testing.T) {
	f := newFixture(t)
	campaign := f.dripCampaign(50, 10, 7)
	sent, _, err := f.service.LaunchCampaign(context.Background(), f.identity, campaign)
	if err != nil || sent != 7 {
		t.Fatalf("sent=%d err=%v, want 7", sent, err)
	}
	if got := f.campaignStatus(campaign.ID); got != "sent" {
		t.Errorf("status %q, want sent", got)
	}
}

// A list that is an exact multiple of the instalment finishes on the last one
// rather than parking for a batch of nobody.
func TestADripCampaignThatEndsOnAnInstalmentBoundaryFinishes(t *testing.T) {
	f := newFixture(t)
	campaign := f.dripCampaign(5, 10, 10)
	ctx := context.Background()
	if sent, _, err := f.service.LaunchCampaign(ctx, f.identity, campaign); err != nil || sent != 5 {
		t.Fatalf("first: sent=%d err=%v", sent, err)
	}
	parked, _ := store.GetCampaign(ctx, f.service.DB, f.identity, campaign.ID)
	_, _ = store.ClaimScheduledCampaign(ctx, f.service.DB, f.identity, campaign.ID)
	if sent, _, err := f.service.LaunchCampaign(ctx, f.identity, parked); err != nil || sent != 5 {
		t.Fatalf("second: sent=%d err=%v", sent, err)
	}
	if got := f.campaignStatus(campaign.ID); got != "sent" {
		t.Errorf("status %q, want sent", got)
	}
}

// A pause that lands between instalments is honoured: nothing more goes out
// until it is resumed, and the resume carries on rather than starting over.
func TestAPausedDripCampaignDoesNotSendTheNextInstalment(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	campaign := f.dripCampaign(4, 10, 10)
	if sent, _, err := f.service.LaunchCampaign(ctx, f.identity, campaign); err != nil || sent != 4 {
		t.Fatalf("first: sent=%d err=%v", sent, err)
	}
	f.exec(`UPDATE campaigns SET status = 'paused', paused_at = now() WHERE id = $1`, campaign.ID)
	if claimed, _ := store.ClaimScheduledCampaign(ctx, f.service.DB, f.identity, campaign.ID); claimed {
		t.Fatal("the scheduler could claim a paused campaign")
	}
	if got := f.campaignMessageCount(campaign.ID); got != 4 {
		t.Errorf("%d messages after a pause, want the 4 already sent", got)
	}
}
