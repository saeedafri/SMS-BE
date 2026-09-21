package sending_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// The running balance a campaign carries from page to page has to be the money
// that actually left the wallet. When it is too generous the ledger — which
// re-reads the real balance inside the transaction — refuses the hold, and that
// error ABORTS the fan-out. So the customer does not get the recipients they
// could not afford refused one by one with a reason they can read; they get a
// campaign that stopped somewhere in the middle.

// setWallet drains the fixture's funded wallet to exactly target minor units.
func setWallet(t *testing.T, f *fixture, target int64) {
	t.Helper()
	balances, err := store.ListWalletBalances(context.Background(), f.service.DB, f.identity)
	if err != nil {
		t.Fatalf("read wallet: %v", err)
	}
	var have int64
	for _, b := range balances {
		if b.Currency == "INR" {
			have = b.BalanceMinor
		}
	}
	if have < target {
		t.Fatalf("wallet holds %d, cannot drain to %d", have, target)
	}
	if _, err := store.AppendLedgerEntry(context.Background(), f.service.DB, f.identity,
		store.LedgerEntry{Currency: "INR", Type: "charge", AmountMinor: have - target,
			Description: "wallet fixture"}); err != nil {
		t.Fatalf("drain wallet: %v", err)
	}
}

func smsRate(t *testing.T, f *fixture) int64 {
	t.Helper()
	rate, err := store.FindPricingRate(context.Background(), f.service.DB,
		f.identity.TenantID, "IN", "SMS", "")
	if err != nil {
		t.Fatalf("sms rate: %v", err)
	}
	return rate.PerSegmentMinor
}

// W2. A single-leg SMS campaign of TWO-SEGMENT messages, across more than one
// page, with a wallet that covers the first page and not the second.
//
// This is the half that predates the fallback leg. The decrement multiplied a
// MESSAGE COUNT by the PER-SEGMENT rate, so every message over 160 characters
// was counted at half what it cost and the running balance drifted further
// from the ledger's with every page.
func TestAMultiSegmentCampaignRefusesWhatItCannotAffordInsteadOfStopping(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rate := smsRate(t, f)

	// 161+ GSM-7 characters is two segments; no slots, so every recipient's
	// message is the same length and the arithmetic is exact.
	body := strings.Repeat("Your order has shipped and is on its way to you. ", 4)
	if len(body) <= 160 {
		t.Fatalf("fixture body is %d chars, need more than 160 for two segments", len(body))
	}
	templateID := f.seedSMSTemplate(f.senderID, body)
	listID, _ := f.seedList("Wallet drift "+uuid.NewString()[:8], 505)

	// Page one is 500 messages at two segments each. The wallet holds that plus
	// five segments — enough for two more messages, not for the last three.
	setWallet(t, f, 1000*rate+5*rate)

	campaign := f.seedCampaign(templateID, listID, "queued", 505)
	sent, failed, err := f.service.LaunchCampaign(ctx, f.identity, campaign)
	if err != nil {
		t.Fatalf("launch returned an error instead of refusing what it could not afford: %v", err)
	}
	if sent != 502 || failed != 3 {
		t.Fatalf("sent=%d failed=%d, want 502 and 3: the wallet covers 502 two-segment messages",
			sent, failed)
	}
	if status := f.campaignStatus(campaign.ID); status != "sent" {
		t.Errorf("campaign status = %q, want sent", status)
	}
}

// W1. A two-leg campaign where EACH leg alone costs less than the wallet but
// together they cost more. Each leg used to be handed its own copy of the whole
// balance, so both believed they could afford their share and the campaign
// passed its own guard twice over.
func TestTwoLegsSpendOneWalletNotTwo(t *testing.T) {
	leg := newTwoLeg(t, nil)
	rate := smsRate(t, leg.f)

	// Six contacts: three the RCS card can be filled for, three it cannot,
	// which the SMS fallback carries. One segment each on both legs.
	seeds := map[string]contactSeed{}
	for i, msisdn := range []string{"919820000201", "919820000202", "919820000203"} {
		_ = i
		seeds[msisdn] = contactSeed{
			fields:  map[string]string{"first_name": "A", "offer": "20% off"},
			consent: both,
		}
	}
	for _, msisdn := range []string{"919820000204", "919820000205", "919820000206"} {
		seeds[msisdn] = contactSeed{fields: map[string]string{"first_name": "B"}, consent: both}
	}

	// Enough for four of the six, whichever legs carry them.
	setWallet(t, leg.f, 4*rate)

	campaignID, sent, failed := leg.launch(t, seeds)
	if sent+failed != 6 {
		t.Fatalf("sent=%d failed=%d, want six recipients accounted for", sent, failed)
	}
	if failed == 0 {
		t.Fatalf("every recipient was sent on a wallet that covers four of six; "+
			"each leg is still spending its own copy of the balance (sent=%d)", sent)
	}
	rows := leg.rows(t, campaignID)
	refused := 0
	for _, row := range rows {
		if row.ErrorCode != nil && *row.ErrorCode == "insufficient_balance" {
			refused++
			if row.CostMinor != 0 {
				t.Errorf("a refused recipient was charged %d", row.CostMinor)
			}
		}
	}
	if refused != failed {
		t.Errorf("%d refusals recorded as insufficient_balance, but %d failed", refused, failed)
	}
}
