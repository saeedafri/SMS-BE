package sending_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// setSendCap gives the fixture's tenant a daily ceiling.
func setSendCap(t *testing.T, f *fixture, perDay int) {
	t.Helper()
	if _, err := sendAdmin.Exec(context.Background(),
		`UPDATE tenants SET send_cap_per_day = $2 WHERE id = $1`,
		f.identity.TenantID, perDay); err != nil {
		t.Fatalf("set send cap: %v", err)
	}
}

// A capped tenant is sent what the ceiling admits and no more — and, the part
// that matters far more than the count, NOTHING is held for the rest.
//
// A withheld recipient has no message row, so there is nothing to charge for
// and nothing to release. The wallet movement for a clipped campaign has to be
// exactly the movement for a campaign of the clipped size, or we are billing a
// customer for messages we decided not to send them.
func TestACeilingSendsWhatItAdmitsAndChargesForNothingElse(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rate := smsRate(t, f)

	const (
		audience = 505
		ceiling  = 300
	)
	templateID := f.seedSMSTemplate(f.senderID, "Your order has shipped.")
	listID, _ := f.seedList("Ceiling "+uuid.NewString()[:8], audience)
	setSendCap(t, f, ceiling)

	before := f.balance()
	campaign := f.seedCampaign(templateID, listID, "queued", audience)
	sent, failed, err := f.service.LaunchCampaign(ctx, f.identity, campaign)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if sent != ceiling || failed != 0 {
		t.Fatalf("sent=%d failed=%d, want %d sent and 0 failed: the ceiling withholds, "+
			"it does not reject", sent, failed, ceiling)
	}
	// The ceiling must not manufacture failures. A withheld recipient recorded
	// as failed would show the customer a delivery rate wrecked by a decision
	// of ours, on messages that were never attempted.
	if spent := before - f.balance(); spent != int64(ceiling)*rate {
		t.Fatalf("wallet moved %d, want %d: a withheld recipient is never held for",
			spent, int64(ceiling)*rate)
	}
	if status := f.campaignStatus(campaign.ID); status != "sent" {
		t.Errorf("campaign status = %q, want sent", status)
	}

	// And the day's usage says what happened, because it is the only place it
	// is written down.
	day := store.SendDay(f.identity.Country, time.Now())
	accepted, withheld, err := store.ReadSendUsage(ctx, sendAdmin, f.identity.TenantID, day)
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if accepted != ceiling {
		t.Errorf("usage says %d accepted, want %d", accepted, ceiling)
	}
	if withheld == 0 {
		t.Errorf("usage recorded nothing withheld on a campaign the ceiling cut by %d",
			audience-ceiling)
	}
}

// An uncapped tenant is the default and must be untouched by any of this —
// including paying for a usage row they will never be measured against.
func TestAnUncappedCampaignSendsItsWholeAudience(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const audience = 40
	templateID := f.seedSMSTemplate(f.senderID, "Your order has shipped.")
	listID, _ := f.seedList("Uncapped "+uuid.NewString()[:8], audience)

	campaign := f.seedCampaign(templateID, listID, "queued", audience)
	sent, failed, err := f.service.LaunchCampaign(ctx, f.identity, campaign)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if sent != audience || failed != 0 {
		t.Fatalf("sent=%d failed=%d, want the whole audience of %d", sent, failed, audience)
	}
	day := store.SendDay(f.identity.Country, time.Now())
	accepted, withheld, err := store.ReadSendUsage(ctx, sendAdmin, f.identity.TenantID, day)
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if accepted != 0 || withheld != 0 {
		t.Errorf("an uncapped tenant was metered: %d accepted, %d withheld", accepted, withheld)
	}
}

// The ceiling is spent down ACROSS campaigns, not per campaign. A per-campaign
// cut would be lifted by splitting one send into two, which is the first thing
// anybody would try.
func TestASecondCampaignSeesWhatTheFirstOneSpent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const ceiling = 30
	templateID := f.seedSMSTemplate(f.senderID, "Your order has shipped.")
	setSendCap(t, f, ceiling)

	// One list, two campaigns over it. The ceiling is a property of the tenant
	// and the day, not of an audience, so the same twenty people twice is the
	// cleanest way to ask whether the second send sees the first one's spend.
	list, _ := f.seedList("Across campaigns "+uuid.NewString()[:8], 20)
	first := f.seedCampaign(templateID, list, "queued", 20)
	sent, _, err := f.service.LaunchCampaign(ctx, f.identity, first)
	if err != nil {
		t.Fatalf("launch first: %v", err)
	}
	if sent != 20 {
		t.Fatalf("first campaign sent %d of 20, and it fits inside the ceiling", sent)
	}

	second := f.seedCampaign(templateID, list, "queued", 20)
	sent, _, err = f.service.LaunchCampaign(ctx, f.identity, second)
	if err != nil {
		t.Fatalf("launch second: %v", err)
	}
	if sent != 10 {
		t.Fatalf("second campaign sent %d, want 10: the first one already spent 20 of %d",
			sent, ceiling)
	}
}

// The quote a customer approves is ALREADY clipped.
//
// Without this the wizard promises a hundred thousand, the campaign row records
// a hundred thousand, and seventy thousand go out — so the campaign detail, the
// delivery rate and the invoice would each report a different truth about the
// same send. Clipping the estimate is what keeps them agreeing, and it is the
// difference between a ceiling and a lie.
func TestTheQuoteIsAlreadyClippedToTheCeiling(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const (
		audience = 40
		ceiling  = 12
	)
	templateID := f.seedSMSTemplate(f.senderID, "Your order has shipped.")
	listID, _ := f.seedList("Quote "+uuid.NewString()[:8], audience)
	template, err := store.GetTemplate(ctx, f.service.DB, f.identity, templateID)
	if err != nil {
		t.Fatalf("load template: %v", err)
	}

	uncapped, err := f.service.EstimateCampaign(ctx, f.identity, &listID, "IN", "SMS", template, nil, time.Now())
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if uncapped.Recipients != audience {
		t.Fatalf("an uncapped quote is %d, want the whole audience of %d",
			uncapped.Recipients, audience)
	}

	setSendCap(t, f, ceiling)
	capped, err := f.service.EstimateCampaign(ctx, f.identity, &listID, "IN", "SMS", template, nil, time.Now())
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if capped.Recipients != ceiling {
		t.Fatalf("the quote is %d, want %d: the number approved must be the number sent",
			capped.Recipients, ceiling)
	}
	// And the money quoted follows the recipients, or the wizard shows a price
	// for messages the ceiling was always going to keep.
	if capped.CostMinorMax*int64(audience) != uncapped.CostMinorMax*int64(ceiling) {
		t.Errorf("quoted cost %d for %d recipients does not scale from %d for %d",
			capped.CostMinorMax, ceiling, uncapped.CostMinorMax, audience)
	}
}

// A campaign scheduled for a later day is quoted against THAT day's allowance.
//
// Both clips read the ceiling for the day the campaign will actually go out. A
// send scheduled for next week, created by a tenant who has already spent
// today's allowance, would otherwise be quoted at zero recipients for a day it
// is not going to run on — and the customer would be told their campaign
// reaches nobody.
func TestAScheduledCampaignIsQuotedAgainstTheDayItWillSend(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const (
		audience = 20
		ceiling  = 5
	)
	templateID := f.seedSMSTemplate(f.senderID, "Your order has shipped.")
	listID, _ := f.seedList("Scheduled "+uuid.NewString()[:8], audience)
	template, err := store.GetTemplate(ctx, f.service.DB, f.identity, templateID)
	if err != nil {
		t.Fatalf("load template: %v", err)
	}
	setSendCap(t, f, ceiling)

	// Today is spent.
	day := store.SendDay(f.identity.Country, time.Now())
	if err := store.RecordSendUsage(ctx, f.service.DB, f.identity, day, ceiling, 0); err != nil {
		t.Fatalf("spend today: %v", err)
	}

	today, err := f.service.EstimateCampaign(ctx, f.identity, &listID, "IN", "SMS",
		template, nil, time.Now())
	if err != nil {
		t.Fatalf("estimate today: %v", err)
	}
	if today.Recipients != 0 {
		t.Fatalf("quoted %d for today, want 0: the ceiling is spent", today.Recipients)
	}

	nextWeek, err := f.service.EstimateCampaign(ctx, f.identity, &listID, "IN", "SMS",
		template, nil, time.Now().AddDate(0, 0, 7))
	if err != nil {
		t.Fatalf("estimate next week: %v", err)
	}
	if nextWeek.Recipients != ceiling {
		t.Fatalf("quoted %d for next week, want %d: that day's allowance is untouched",
			nextWeek.Recipients, ceiling)
	}
}
