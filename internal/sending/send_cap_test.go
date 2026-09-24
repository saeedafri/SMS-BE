package sending_test

import (
	"context"
	"strings"
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

// Ask 70, the other half. A campaign's wallet entry is untouched by the fix
// that made a journey's say "Journey": still a campaign id, no journey, and the
// wording it has always had.
//
// Here rather than beside the journey test because this is where a campaign
// actually launches and moves money — asserting the campaign side from the API
// would mean building a second way to launch one.
func TestACampaignsWalletEntryStillSaysCampaign(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	templateID := f.seedSMSTemplate(f.senderID, "Your order has shipped.")
	listID, _ := f.seedList("Wallet wording "+uuid.NewString()[:8], 3)
	campaign := f.seedCampaign(templateID, listID, "queued", 3)
	if _, _, err := f.service.LaunchCampaign(ctx, f.identity, campaign); err != nil {
		t.Fatalf("launch: %v", err)
	}

	entries, _, err := store.LedgerPage(ctx, f.service.DB, f.identity, "INR", 1, 50)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	for _, entry := range entries {
		if entry.Type != "charge" || entry.CampaignID == nil || *entry.CampaignID != campaign.ID {
			continue
		}
		if entry.JourneyID != nil {
			t.Errorf("journeyId = %v on a campaign's hold", *entry.JourneyID)
		}
		if !strings.Contains(entry.Description, "Campaign hold") {
			t.Errorf("description = %q, want the campaign wording left alone",
				entry.Description)
		}
		return
	}
	t.Fatalf("no charge found for campaign %s in %d ledger entries", campaign.ID, len(entries))
}

// setSharePercent caps the fixture's tenant to a share of each send.
func setSharePercent(t *testing.T, f *fixture, percent int) {
	t.Helper()
	if _, err := sendAdmin.Exec(context.Background(),
		`UPDATE tenants SET send_cap_percent = $2 WHERE id = $1`,
		f.identity.TenantID, percent); err != nil {
		t.Fatalf("set share cap: %v", err)
	}
}

// exemptContacts marks the first n contacts of a list as always_send.
func exemptContacts(t *testing.T, f *fixture, listID uuid.UUID, n int) []uuid.UUID {
	t.Helper()
	rows, err := sendAdmin.Query(context.Background(), `
		UPDATE contacts SET always_send = true
		WHERE id IN (SELECT c.id FROM contacts c
		             JOIN contact_list_members m ON m.contact_id = c.id
		             WHERE m.list_id = $1 ORDER BY c.created_at, c.id LIMIT $2)
		RETURNING id`, listID, n)
	if err != nil {
		t.Fatalf("exempt contacts: %v", err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	if len(ids) != n {
		t.Fatalf("exempted %d contacts, wanted %d", len(ids), n)
	}
	return ids
}

// The whole feature, end to end: a hundred contacts, a 70% cap, fifteen of them
// exempt. Seventy messages go out, all fifteen exempt among them, and the
// wallet moves by seventy — not a hundred.
func TestACappedSendCarriesItsShareAndEveryExemptContact(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rate := smsRate(t, f)

	templateID := f.seedSMSTemplate(f.senderID, "Your order has shipped.")
	listID, _ := f.seedList("Share "+uuid.NewString()[:8], 100)
	exempt := exemptContacts(t, f, listID, 15)
	setSharePercent(t, f, 70)

	before := f.balance()
	campaign := f.seedCampaign(templateID, listID, "queued", 100)
	sent, failed, err := f.service.LaunchCampaign(ctx, f.identity, campaign)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if sent != 70 || failed != 0 {
		t.Fatalf("sent=%d failed=%d, want 70 sent and 0 failed", sent, failed)
	}
	if spent := before - f.balance(); spent != 70*rate {
		t.Fatalf("wallet moved %d, want %d — a withheld contact is never charged for",
			spent, 70*rate)
	}

	// Every exempt contact was carried. This is the row the whole rebuild is
	// for: a cap that can skip the customer's own staff is not the feature
	// that was asked for.
	var carried int
	if err := sendAdmin.QueryRow(ctx, `
		SELECT count(*) FROM contacts
		WHERE id = ANY($1) AND last_capped_send_at IS NOT NULL`, exempt).Scan(&carried); err != nil {
		t.Fatalf("count carried: %v", err)
	}
	if carried != 15 {
		t.Fatalf("%d of the 15 exempt contacts were carried, want all 15", carried)
	}
}

// A second send to the same list reaches people the first one cut. Without
// rotation the same thirty are invisible forever, on every send, and nothing
// on any report says so.
func TestARepeatedCappedSendReachesDifferentPeople(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	templateID := f.seedSMSTemplate(f.senderID, "Your order has shipped.")
	listID, _ := f.seedList("Rotation "+uuid.NewString()[:8], 10)
	setSharePercent(t, f, 50)

	carriedNow := func() map[uuid.UUID]time.Time {
		t.Helper()
		rows, err := sendAdmin.Query(ctx, `
			SELECT c.id, c.last_capped_send_at FROM contacts c
			JOIN contact_list_members m ON m.contact_id = c.id
			WHERE m.list_id = $1 AND c.last_capped_send_at IS NOT NULL`, listID)
		if err != nil {
			t.Fatalf("read carried: %v", err)
		}
		defer rows.Close()
		out := map[uuid.UUID]time.Time{}
		for rows.Next() {
			var id uuid.UUID
			var at time.Time
			if err := rows.Scan(&id, &at); err != nil {
				t.Fatalf("scan: %v", err)
			}
			out[id] = at
		}
		return out
	}

	first := f.seedCampaign(templateID, listID, "queued", 10)
	if sent, _, err := f.service.LaunchCampaign(ctx, f.identity, first); err != nil || sent != 5 {
		t.Fatalf("first send: sent=%d err=%v, want 5", sent, err)
	}
	afterFirst := carriedNow()
	if len(afterFirst) != 5 {
		t.Fatalf("first send carried %d, want 5", len(afterFirst))
	}

	second := f.seedCampaign(templateID, listID, "queued", 10)
	if sent, _, err := f.service.LaunchCampaign(ctx, f.identity, second); err != nil || sent != 5 {
		t.Fatalf("second send: sent=%d err=%v, want 5", sent, err)
	}
	afterSecond := carriedNow()
	if len(afterSecond) != 10 {
		t.Fatalf("after two sends %d of 10 contacts have been carried, want all 10 — "+
			"the second send repeated the first one's choice", len(afterSecond))
	}
}
