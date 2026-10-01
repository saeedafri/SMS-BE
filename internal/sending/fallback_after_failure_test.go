package sending_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/store"
)

// twoLegCampaign launches an RCS campaign with an SMS fallback to the given
// contacts and returns the carriers' stubs and the campaign.
func (f *fixture) twoLegCampaign(t *testing.T, contacts map[string]contactSeed) (rcs, sms *recordingCarrier, campaign store.Campaign) {
	t.Helper()
	ctx := context.Background()
	rcs, sms = &recordingCarrier{}, &recordingCarrier{}
	// SMS is the default, RCS dedicated: see TestAContactNoLegCanFillIsStillSkipped.
	f.service.Carriers = connector.Registry{Default: sms,
		ByChannel: map[string]connector.Connector{"RCS": rcs}}

	rcsSender := f.seedApprovedSender("RCSFB"+uuid.NewString()[:4], "RCS")
	rcsTemplate := f.seedRCSCardTemplate(rcsSender, []string{"first_name", "offer"})
	smsSender := f.seedApprovedSender("SMSFB"+uuid.NewString()[:4], "SMS")
	smsTemplate := f.seedSMSTemplate(smsSender, "Hi {{first_name}}, we have news.")
	listID := f.seedListWithConsent(contacts)
	loaded, err := store.GetCampaign(ctx, f.service.DB, f.identity,
		f.seedCampaignWithFallback(rcsSender, rcsTemplate, listID, "SMS", smsSender, smsTemplate))
	if err != nil {
		t.Fatalf("load campaign: %v", err)
	}
	if _, _, err := f.service.LaunchCampaign(ctx, f.identity, loaded); err != nil {
		t.Fatalf("launch: %v", err)
	}
	return rcs, sms, loaded
}

func (f *fixture) failed(t *testing.T, messageID string) {
	t.Helper()
	if err := f.service.ApplyDeliveryReport(context.Background(), f.identity, connector.DeliveryReport{
		MessageID: messageID, Delivered: false, ErrorCode: "NON_RCS"}); err != nil {
		t.Fatalf("apply failure report: %v", err)
	}
}

var bothChannels = map[string]string{"RCS": "opted_in", "SMS": "opted_in"}

func named(first string) map[string]string {
	return map[string]string{"first_name": first, "offer": "20% off"}
}

// The RCS message bounces; the same person then gets the SMS, filled from
// their own fields. Nobody else does, and a repeated report sends nothing more.
func TestAFailedRCSMessageIsGivenItsSMSFallbackExactlyOnce(t *testing.T) {
	f := newFixture(t)
	rcs, sms, campaign := f.twoLegCampaign(t, map[string]contactSeed{
		"919820000201": {fields: named("Priya"), consent: bothChannels},
		"919820000202": {fields: named("Vikram"), consent: bothChannels},
	})
	if len(rcs.submissions) != 2 || len(sms.submissions) != 0 {
		t.Fatalf("launch sent %d RCS and %d SMS, want 2 and 0", len(rcs.submissions), len(sms.submissions))
	}
	var bounced, kept connector.Submission
	for _, s := range rcs.submissions {
		if strings.HasSuffix(s.Msisdn, "201") {
			bounced = s
		} else {
			kept = s
		}
	}
	f.failed(t, bounced.MessageID)
	f.failed(t, bounced.MessageID) // the carrier repeats itself

	if len(sms.submissions) != 1 || sms.submissions[0].Msisdn != "+919820000201" ||
		!strings.Contains(sms.submissions[0].Body, "Hi Priya") {
		t.Fatalf("SMS fallback = %+v, want one personalised message to the bounced contact", sms.submissions)
	}
	// The message that was not reported failed is left alone.
	if err := f.service.ApplyDeliveryReport(context.Background(), f.identity, connector.DeliveryReport{
		MessageID: kept.MessageID, Delivered: true}); err != nil {
		t.Fatal(err)
	}
	if len(sms.submissions) != 1 {
		t.Errorf("a delivered RCS message produced a fallback")
	}
	// Filed under the campaign, carried by SMS.
	var carried string
	var cost int64
	if err := f.service.ClickHouse.QueryRow(context.Background(), `
		SELECT delivered_channel, cost_minor FROM messages FINAL
		WHERE tenant_id = ? AND campaign_id = ? AND msisdn = ? AND delivered_channel = 'SMS'`,
		f.identity.TenantID, campaign.ID, "+919820000201").Scan(&carried, &cost); err != nil ||
		carried != "SMS" || cost <= 0 {
		t.Errorf("fallback row = %q cost %d (%v), want carried by SMS and charged", carried, cost, err)
	}
}

// The consent rules are not bypassed: no SMS consent, no SMS.
func TestTheFallbackNeverTextsSomeoneWhoDidNotOptInToSMS(t *testing.T) {
	f := newFixture(t)
	rcs, sms, _ := f.twoLegCampaign(t, map[string]contactSeed{
		"919820000211": {fields: named("Asha"), consent: map[string]string{"RCS": "opted_in"}},
	})
	if len(rcs.submissions) != 1 {
		t.Fatalf("RCS submissions = %d", len(rcs.submissions))
	}
	f.failed(t, rcs.submissions[0].MessageID)
	if len(sms.submissions) != 0 {
		t.Errorf("texted a contact with no SMS consent: %+v", sms.submissions)
	}
}

// A person who opted out between the RCS send and its failure is not texted.
func TestTheFallbackNeverTextsASuppressedNumber(t *testing.T) {
	f := newFixture(t)
	rcs, sms, _ := f.twoLegCampaign(t, map[string]contactSeed{
		"919820000221": {fields: named("Ravi"), consent: bothChannels},
	})
	f.exec(`INSERT INTO suppressions (tenant_id, identity, reason) VALUES ($1, '+919820000221', 'manual')`,
		f.identity.TenantID)
	f.failed(t, rcs.submissions[0].MessageID)
	if len(sms.submissions) != 0 {
		t.Errorf("texted a suppressed number: %+v", sms.submissions)
	}
}

// Someone stopped the campaign: its fallback stops with it.
func TestACancelledCampaignDoesNotFallBack(t *testing.T) {
	f := newFixture(t)
	rcs, sms, campaign := f.twoLegCampaign(t, map[string]contactSeed{
		"919820000231": {fields: named("Meera"), consent: bothChannels},
	})
	f.exec(`UPDATE campaigns SET status = 'cancelled', cancelled_at = now() WHERE id = $1`, campaign.ID)
	f.failed(t, rcs.submissions[0].MessageID)
	if len(sms.submissions) != 0 {
		t.Errorf("a cancelled campaign still fell back: %+v", sms.submissions)
	}
}

// The fallback's own failure is final: there is no third leg.
func TestAFailedFallbackDoesNotFallBackAgain(t *testing.T) {
	f := newFixture(t)
	rcs, sms, _ := f.twoLegCampaign(t, map[string]contactSeed{
		"919820000241": {fields: named("Nina"), consent: bothChannels},
	})
	f.failed(t, rcs.submissions[0].MessageID)
	if len(sms.submissions) != 1 {
		t.Fatalf("fallback submissions = %d", len(sms.submissions))
	}
	f.failed(t, sms.submissions[0].MessageID)
	if len(sms.submissions) != 1 || len(rcs.submissions) != 1 {
		t.Errorf("the fallback failing sent more: rcs=%d sms=%d", len(rcs.submissions), len(sms.submissions))
	}
}

// A campaign with no fallback leg is untouched by all of this.
func TestACampaignWithoutAFallbackSendsNothingOnFailure(t *testing.T) {
	f := newFixture(t)
	templateID := f.seedSMSTemplate(f.senderID, "Plain.")
	listID, _ := f.seedList("nofb", 2)
	campaign := f.seedCampaign(templateID, listID, "queued", 2)
	if _, _, err := f.service.LaunchCampaign(context.Background(), f.identity, campaign); err != nil {
		t.Fatal(err)
	}
	before := f.campaignMessageCount(campaign.ID)
	var id string
	if err := f.service.ClickHouse.QueryRow(context.Background(),
		`SELECT toString(id) FROM messages WHERE tenant_id = ? AND campaign_id = ? LIMIT 1`,
		f.identity.TenantID, campaign.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	f.failed(t, id)
	if after := f.campaignMessageCount(campaign.ID); after != before {
		t.Errorf("messages %d -> %d on a campaign with no fallback", before, after)
	}
}

// The claim is what makes "exactly once" true when two workers see the same
// failure at once, which the state machine alone does not cover.
func TestOnlyOneCallerWinsTheFallbackClaim(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	message, campaign := uuid.New(), uuid.New()
	first, err := store.ClaimFallback(ctx, f.service.DB, f.identity, message, campaign)
	second, err2 := store.ClaimFallback(ctx, f.service.DB, f.identity, message, campaign)
	if err != nil || err2 != nil || !first || second {
		t.Fatalf("claims = %v %v (%v %v), want true then false", first, second, err, err2)
	}
	if err := store.ReleaseFallback(ctx, f.service.DB, f.identity, message); err != nil {
		t.Fatal(err)
	}
	if again, _ := store.ClaimFallback(ctx, f.service.DB, f.identity, message, campaign); !again {
		t.Errorf("a released claim could not be taken again")
	}
}
