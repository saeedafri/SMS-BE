package sending_test

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"
)

// {{link}}: every recipient gets their own tracked short link, made as their
// message is built. BACKEND_REQUEST_link-variable.md, asks L3 and L6.

const linkBase = "https://links.relay.test"

var shortLink = regexp.MustCompile(`https://links\.relay\.test/l/([A-Za-z0-9]{8})\b`)

// linkCode is the one tracked code in a message, failing the test if there is
// not exactly one.
func linkCode(t *testing.T, text string) string {
	t.Helper()
	found := shortLink.FindAllStringSubmatch(text, -1)
	if len(found) != 1 {
		t.Fatalf("message %q carries %d tracked links, want 1", text, len(found))
	}
	return found[0][1]
}

// linked gives the campaign a link spec, as POST /v1/campaigns stores it.
func (f *fixture) linked(campaignID uuid.UUID, spec map[string]any) store.Campaign {
	f.t.Helper()
	encoded, _ := json.Marshal(spec)
	f.exec(`UPDATE campaigns SET link = $2::jsonb WHERE id = $1`, campaignID, string(encoded))
	campaign, err := store.GetCampaign(context.Background(), f.service.DB, f.identity, campaignID)
	if err != nil {
		f.t.Fatalf("reload campaign: %v", err)
	}
	return campaign
}

func (f *fixture) campaignLinks(campaignID uuid.UUID) []store.ShortLink {
	f.t.Helper()
	links, _, err := store.ListShortLinks(context.Background(), f.service.DB, f.identity,
		store.LinkFilter{CampaignID: &campaignID}, 1, 200)
	if err != nil {
		f.t.Fatalf("list links: %v", err)
	}
	return links
}

// smsLinkCampaign is an SMS campaign whose template uses {{link}}, with an
// SMS stub recording what is sent.
func (f *fixture) smsLinkCampaign(body string, contacts map[string]contactSeed,
	spec map[string]any) (*recordingCarrier, store.Campaign) {

	f.t.Helper()
	sms := &recordingCarrier{}
	f.service.Carriers = connector.Registry{Default: sms}
	f.service.LinkBaseURL = linkBase
	template := f.seedSMSTemplate(f.senderID, body)
	listID := f.seedListWithConsent(contacts)
	campaign := f.seedCampaign(template, listID, "queued", len(contacts))
	if spec != nil {
		campaign = f.linked(campaign.ID, spec)
	}
	return sms, campaign
}

var smsOnly = map[string]string{"SMS": "opted_in"}

func (f *fixture) launch(campaign store.Campaign) (int, int) {
	f.t.Helper()
	sent, failed, err := f.service.LaunchCampaign(context.Background(), f.identity, campaign)
	if err != nil {
		f.t.Fatalf("launch: %v", err)
	}
	return sent, failed
}

// A3, A4 (two people), A10's half: each message carries ITS person's link, the
// code is eight characters on the configured base, and a contact column called
// "link" changes nothing.
func TestEachRecipientGetsTheirOwnLinkInTheirMessage(t *testing.T) {
	f := newFixture(t)
	sms, campaign := f.smsLinkCampaign("Sale today: {{link}}", map[string]contactSeed{
		"919820000301": {consent: smsOnly},
		"919820000302": {fields: map[string]string{"link": "https://untracked.example/"}, consent: smsOnly},
	}, map[string]any{"destination": "https://shop.example.org/sale"})

	if sent, failed := f.launch(campaign); sent != 2 || failed != 0 {
		t.Fatalf("sent=%d failed=%d, want 2 and 0", sent, failed)
	}
	codes := map[string]string{}
	for _, s := range sms.submissions {
		if !strings.HasPrefix(s.Body, "Sale today: "+linkBase+"/l/") {
			t.Errorf("body = %q", s.Body)
		}
		codes[s.Msisdn] = linkCode(t, s.Body)
	}
	if codes["+919820000301"] == codes["+919820000302"] {
		t.Fatalf("two recipients share code %q", codes["+919820000301"])
	}
	links := f.campaignLinks(campaign.ID)
	if len(links) != 2 {
		t.Fatalf("%d links made, want one per recipient", len(links))
	}
	for _, link := range links {
		if codes[*link.Recipient] != link.Code || link.Destination != "https://shop.example.org/sale" {
			t.Errorf("link %+v does not match what %s was sent (%s)", link, *link.Recipient, codes[*link.Recipient])
		}
	}
	// The log keeps what each handset got.
	for msisdn, code := range codes {
		var text *string
		if err := f.service.ClickHouse.QueryRow(context.Background(), `
			SELECT rendered_text FROM messages FINAL
			WHERE tenant_id = ? AND campaign_id = ? AND msisdn = ?`,
			f.identity.TenantID, campaign.ID, msisdn).Scan(&text); err != nil || text == nil ||
			linkCode(t, *text) != code {
			t.Errorf("logged text for %s = %v (%v), want code %s", msisdn, text, err, code)
		}
	}
}

// A4: sending the same campaign again — a retry, a restart that replays a page —
// lands every person on the link they already have.
func TestARetryLandsOnTheSameLink(t *testing.T) {
	f := newFixture(t)
	sms, campaign := f.smsLinkCampaign("Tap {{link}}", map[string]contactSeed{
		"919820000311": {consent: smsOnly}, "919820000312": {consent: smsOnly},
	}, map[string]any{"destination": "https://shop.example.org/"})
	f.launch(campaign)
	first := map[string]string{}
	for _, s := range sms.submissions {
		first[s.Msisdn] = linkCode(t, s.Body)
	}
	sms.submissions = nil
	f.launch(campaign)
	for _, s := range sms.submissions {
		if got := linkCode(t, s.Body); got != first[s.Msisdn] {
			t.Errorf("%s was sent %s again after %s", s.Msisdn, got, first[s.Msisdn])
		}
	}
	if n := len(f.campaignLinks(campaign.ID)); n != 2 {
		t.Errorf("%d links after a retry, want still 2", n)
	}
}

// A4, the drip half: an instalment replayed after a crash (its cursor never
// saved) sends its people the links they were already given.
func TestADripResumeLandsOnTheSameLink(t *testing.T) {
	f := newFixture(t)
	sms, campaign := f.smsLinkCampaign("Tap {{link}}", map[string]contactSeed{
		"919820000321": {consent: smsOnly}, "919820000322": {consent: smsOnly},
		"919820000323": {consent: smsOnly},
	}, map[string]any{"destination": "https://shop.example.org/"})
	f.exec(`UPDATE campaigns SET drip_batch_size = 2, drip_interval_minutes = 10 WHERE id = $1`, campaign.ID)
	reload := func() store.Campaign {
		loaded, err := store.GetCampaign(context.Background(), f.service.DB, f.identity, campaign.ID)
		if err != nil {
			t.Fatal(err)
		}
		return loaded
	}
	f.launch(reload())
	first := map[string]string{}
	for _, s := range sms.submissions {
		first[s.Msisdn] = linkCode(t, s.Body)
	}
	if len(first) != 2 {
		t.Fatalf("first instalment sent %d", len(first))
	}
	// The crash: the instalment went out and its cursor did not stick.
	f.exec(`UPDATE campaigns SET dispatch_cursor = NULL, status = 'queued' WHERE id = $1`, campaign.ID)
	sms.submissions = nil
	f.launch(reload())
	for _, s := range sms.submissions {
		if code, had := first[s.Msisdn]; had && linkCode(t, s.Body) != code {
			t.Errorf("%s resumed onto a new link", s.Msisdn)
		}
	}
	f.exec(`UPDATE campaigns SET status = 'queued' WHERE id = $1`, campaign.ID)
	f.launch(reload())
	if n := len(f.campaignLinks(campaign.ID)); n != 3 {
		t.Errorf("%d links for three people across a replayed drip, want 3", n)
	}
}

// A6 and section 7: a link the customer pre-made through bulk for this campaign
// and this person is the one they are sent, with its own destination.
func TestAPreMadeLinkIsTheOneSent(t *testing.T) {
	f := newFixture(t)
	sms, campaign := f.smsLinkCampaign("Tap {{link}}", map[string]contactSeed{
		"919820000331": {consent: smsOnly}, "919820000332": {consent: smsOnly},
	}, map[string]any{"destination": "https://shop.example.org/everyone"})
	person := "+919820000331"
	made, err := store.CreateShortLinks(context.Background(), f.service.DB, f.identity,
		[]store.ShortLink{{Destination: "https://shop.example.org/vip",
			CampaignID: &campaign.ID, Recipient: &person}})
	if err != nil {
		t.Fatal(err)
	}
	f.launch(campaign)
	for _, s := range sms.submissions {
		if s.Msisdn == person && linkCode(t, s.Body) != made[0].Code {
			t.Errorf("pre-made person sent %q, want code %s", s.Body, made[0].Code)
		}
	}
	links := f.campaignLinks(campaign.ID)
	if len(links) != 2 {
		t.Fatalf("%d links, want the pre-made one and one new", len(links))
	}
	for _, link := range links {
		want := "https://shop.example.org/everyone"
		if *link.Recipient == person {
			want = "https://shop.example.org/vip"
		}
		if link.Destination != want {
			t.Errorf("%s's link goes to %s, want %s", *link.Recipient, link.Destination, want)
		}
	}
}

// A9 and L6: a destination that is no longer acceptable when the message is
// built — India refuses public shorteners — refuses the message as
// variable_unresolved. Nothing is sent without its link, and no link is made.
func TestADestinationInvalidBySendTimeRefusesTheMessage(t *testing.T) {
	for name, spec := range map[string]map[string]any{
		"shortener": {"destination": "https://bit.ly/abc"},
		"expired": {"destination": "https://shop.example.org/",
			"expiresAt": time.Now().Add(-time.Hour).Format(time.RFC3339)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			sms, campaign := f.smsLinkCampaign("Tap {{link}}", map[string]contactSeed{
				"919820000341": {consent: smsOnly},
			}, spec)
			sent, failed := f.launch(campaign)
			if sent != 0 || failed != 1 || len(sms.submissions) != 0 {
				t.Fatalf("sent=%d failed=%d submitted=%d, want the message refused", sent, failed, len(sms.submissions))
			}
			row := (&twoLeg{f: f}).rows(t, campaign.ID)["+919820000341"]
			if row.ErrorCode == nil || *row.ErrorCode != "variable_unresolved" || row.CostMinor != 0 {
				t.Errorf("row = code %v cost %d, want variable_unresolved at no cost", row.ErrorCode, row.CostMinor)
			}
			if n := len(f.campaignLinks(campaign.ID)); n != 0 {
				t.Errorf("%d links made for a refused message", n)
			}
			if balance := f.balance(); balance != 1_000_000 {
				t.Errorf("balance = %d, want the untouched 1000000: nothing is held for a refusal", balance)
			}
		})
	}
}

// A pre-made link that has expired is not sent either: the recipient would tap
// a link that answers 410. Refused, like any link that cannot be made.
func TestAnExpiredPreMadeLinkRefusesTheMessage(t *testing.T) {
	f := newFixture(t)
	sms, campaign := f.smsLinkCampaign("Tap {{link}}", map[string]contactSeed{
		"919820000345": {consent: smsOnly},
	}, map[string]any{"destination": "https://shop.example.org/"})
	person := "+919820000345"
	gone := time.Now().Add(-time.Minute)
	if _, err := store.CreateShortLinks(context.Background(), f.service.DB, f.identity,
		[]store.ShortLink{{Destination: "https://shop.example.org/old", CampaignID: &campaign.ID,
			Recipient: &person, ExpiresAt: &gone}}); err != nil {
		t.Fatal(err)
	}
	if sent, failed := f.launch(campaign); sent != 0 || failed != 1 || len(sms.submissions) != 0 {
		t.Fatalf("sent=%d failed=%d, want the message refused rather than sent with a dead link", sent, failed)
	}
}

// Without a configured base there is no URL to build, so the same refusal.
func TestNoLinkBaseRefusesRatherThanSendingWithoutTheLink(t *testing.T) {
	f := newFixture(t)
	sms, campaign := f.smsLinkCampaign("Tap {{link}}", map[string]contactSeed{
		"919820000351": {consent: smsOnly},
	}, map[string]any{"destination": "https://shop.example.org/"})
	f.service.LinkBaseURL = ""
	if sent, failed := f.launch(campaign); sent != 0 || failed != 1 || len(sms.submissions) != 0 {
		t.Fatalf("sent=%d failed=%d submitted=%d, want refused", sent, failed, len(sms.submissions))
	}
}

// A number the gate refuses gets no link: nothing was sent to it. An unapproved
// sender is refused by the gate and by nothing before it.
func TestARecipientTheGateRefusesGetsNoLink(t *testing.T) {
	f := newFixture(t)
	sms, campaign := f.smsLinkCampaign("Tap {{link}}", map[string]contactSeed{
		"919820000361": {consent: smsOnly},
	}, map[string]any{"destination": "https://shop.example.org/"})
	f.exec(`UPDATE sender_ids SET status = 'pending_review' WHERE id = $1`, f.senderID)
	sent, failed := f.launch(campaign)
	if sent != 0 || failed != 1 || len(sms.submissions) != 0 {
		t.Fatalf("sent=%d failed=%d, want the gate's refusal", sent, failed)
	}
	if row := (&twoLeg{f: f}).rows(t, campaign.ID)["+919820000361"]; row.ErrorCode == nil ||
		*row.ErrorCode != "sender_not_approved" {
		t.Errorf("refusal = %v, want the gate's own reason, not the link's", row.ErrorCode)
	}
	if n := len(f.campaignLinks(campaign.ID)); n != 0 {
		t.Errorf("%d links made for a number the gate refused", n)
	}
}

// A campaign whose template has {{link}} and no link spec — none can exist
// after POST /v1/campaigns, but rows older than the rule can — skips by the name
// link, exactly as the estimate counted it.
func TestNoSpecSkipsByTheNameLink(t *testing.T) {
	f := newFixture(t)
	sms, campaign := f.smsLinkCampaign("Tap {{link}}", map[string]contactSeed{
		"919820000371": {fields: map[string]string{"link": "https://x.example/"}, consent: smsOnly},
	}, nil)
	if sent, failed := f.launch(campaign); sent != 0 || failed != 0 || len(sms.submissions) != 0 {
		t.Fatalf("sent=%d failed=%d; a contact's own link column must not stand in", sent, failed)
	}
}

// A5 and A7 (RCS button URL): the RCS card carries {{link}} in its button; the
// handset bounces; the SMS fallback carries the SAME code.
func TestTheFallbackLegCarriesTheSameLinkAsThePrimary(t *testing.T) {
	f := newFixture(t)
	rcs, sms := &recordingCarrier{}, &recordingCarrier{}
	f.service.Carriers = connector.Registry{Default: sms,
		ByChannel: map[string]connector.Connector{"RCS": rcs}}
	f.service.LinkBaseURL = linkBase
	rcsSender := f.seedApprovedSender("RCSL"+uuid.NewString()[:4], "RCS")
	card := f.seedRCSCard(rcsSender, []string{"first_name", "link"},
		`{"kind":"card","text":"Hi {{first_name}}","card":{"title":"Our sale","description":"Tap below"},`+
			`"suggestions":[{"text":"Shop","url":"{{link}}"}]}`)
	smsSender := f.seedApprovedSender("SMSL"+uuid.NewString()[:4], "SMS")
	text := f.seedSMSTemplate(smsSender, "Hi {{first_name}}, shop at {{link}}")
	listID := f.seedListWithConsent(map[string]contactSeed{
		"919820000381": {fields: map[string]string{"first_name": "Priya"}, consent: bothChannels},
	})
	campaign := f.linked(f.seedCampaignWithFallback(rcsSender, card, listID, "SMS", smsSender, text),
		map[string]any{"destination": "https://shop.example.org/"})
	f.launch(campaign)
	if len(rcs.submissions) != 1 {
		t.Fatalf("RCS submissions = %d", len(rcs.submissions))
	}
	primary := ""
	for _, variable := range rcs.submissions[0].TemplateVariables {
		if variable.Name == "link" {
			primary = variable.Value
		}
	}
	code := linkCode(t, primary)
	if primary != linkBase+"/l/"+code {
		t.Errorf("the button's link = %q, want the bare short URL", primary)
	}
	f.failed(t, rcs.submissions[0].MessageID)
	if len(sms.submissions) != 1 || linkCode(t, sms.submissions[0].Body) != code {
		t.Fatalf("SMS fallback = %+v, want the primary's code %s", sms.submissions, code)
	}
	if n := len(f.campaignLinks(campaign.ID)); n != 1 {
		t.Errorf("%d links for one person across two legs, want 1", n)
	}
}

// A {{link}} held only by the FALLBACK template still fills: the primary card
// needs an offer this contact lacks, so the SMS carries them, with a link.
func TestALinkHeldOnlyByTheFallbackFills(t *testing.T) {
	leg := newTwoLeg(t, nil)
	leg.f.service.LinkBaseURL = linkBase
	text := leg.f.seedSMSTemplate(leg.smsSender, "Hi {{first_name}}: {{link}}")
	listID := leg.f.seedListWithConsent(map[string]contactSeed{
		"919820000391": {fields: map[string]string{"first_name": "Ravi"}, consent: both},
	})
	campaign := leg.f.linked(leg.f.seedCampaignWithFallback(leg.rcsSender, leg.rcsCard, listID,
		"SMS", leg.smsSender, text), map[string]any{"destination": "https://shop.example.org/"})
	leg.f.launch(campaign)
	if len(leg.sms.submissions) != 1 || len(leg.rcs.submissions) != 0 {
		t.Fatalf("rcs=%d sms=%d, want the fallback to carry them", len(leg.rcs.submissions), len(leg.sms.submissions))
	}
	linkCode(t, leg.sms.submissions[0].Body)
}

// A7, the Email half: the body an email carries has the link in it.
func TestAnEmailBodyCarriesTheLink(t *testing.T) {
	f := newFixture(t)
	mail := &recordingCarrier{}
	f.service.Carriers = connector.Registry{Default: mail}
	f.service.LinkBaseURL = linkBase
	sender := f.seedApprovedSender("mail"+uuid.NewString()[:4]+"@relay.test", "EMAIL")
	template := uuid.New()
	f.exec(`INSERT INTO templates (id, tenant_id, sender_id, name, channel, country, body, variables, status, category)
	        VALUES ($1, $2, $3, 'Email link', 'EMAIL', 'IN', 'Your offer: {{link}}', '{link}', 'approved', 'UTILITY')`,
		template, f.identity.TenantID, sender)
	listID := uuid.New()
	f.exec(`INSERT INTO contact_lists (id, tenant_id, name) VALUES ($1, $2, 'Email list')`, listID, f.identity.TenantID)
	contact := uuid.New()
	f.exec(`INSERT INTO contacts (id, tenant_id, msisdn, email, country, fields, consent)
	        VALUES ($1, $2, '+919820000401', 'asha@example.org', 'IN', '{}', '{"EMAIL":"opted_in"}')`,
		contact, f.identity.TenantID)
	f.exec(`INSERT INTO contact_list_members (list_id, contact_id, tenant_id) VALUES ($1, $2, $3)`,
		listID, contact, f.identity.TenantID)
	campaignID := uuid.New()
	f.exec(`INSERT INTO campaigns (id, tenant_id, name, channel, country, list_id, sender_id, template_id,
	            status, recipients, segments_per_message_min, segments_per_message_max,
	            cost_minor_min, cost_minor_max, currency, link)
	        VALUES ($1, $2, 'Email link', 'EMAIL', 'IN', $3, $4, $5, 'queued', 1, 1, 1, 1, 1, 'INR',
	                '{"destination":"https://shop.example.org/"}')`,
		campaignID, f.identity.TenantID, listID, sender, template)
	campaign, err := store.GetCampaign(context.Background(), f.service.DB, f.identity, campaignID)
	if err != nil {
		t.Fatal(err)
	}
	if sent, failed := f.launch(campaign); sent != 1 || failed != 0 {
		t.Fatalf("sent=%d failed=%d", sent, failed)
	}
	if len(mail.submissions) != 1 || !strings.HasPrefix(mail.submissions[0].Body, "Your offer: "+linkBase+"/l/") {
		t.Fatalf("email = %+v", mail.submissions)
	}
	linkCode(t, mail.submissions[0].Body)
}

// A8 and A14: the estimate counts {{link}} at the finished link's fixed length
// at BOTH ends. A body exactly at the 160-character boundary without the link
// is two segments with it, with no range. Without a link spec every recipient
// is skipped, by the name link.
func TestTheEstimateCountsTheLinkAtItsFixedLength(t *testing.T) {
	f := newFixture(t)
	f.service.LinkBaseURL = linkBase
	body := strings.Repeat("a", 160) + "{{link}}"
	listID := f.seedListWithConsent(map[string]contactSeed{
		"919820000411": {consent: smsOnly}, "919820000412": {consent: smsOnly},
	})
	template, err := store.GetTemplate(context.Background(), f.service.DB, f.identity,
		f.seedSMSTemplate(f.senderID, body))
	if err != nil {
		t.Fatal(err)
	}
	with, err := f.service.EstimateCampaign(context.Background(), f.identity, &listID, "IN", "SMS",
		template, nil, &store.LinkSpec{Destination: "https://shop.example.org/"}, time.Now())
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if with.SegmentsPerMessageMin != 2 || with.SegmentsPerMessageMax != 2 ||
		with.Recipients != 2 || with.VariableSkipped != 0 {
		t.Errorf("with a link = %+v, want 2 recipients at exactly 2 segments", with)
	}
	without, err := f.service.EstimateCampaign(context.Background(), f.identity, &listID, "IN", "SMS",
		template, nil, nil, time.Now())
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if without.Recipients != 0 || without.VariableSkipped != 2 || without.VariableSkippedByName["link"] != 2 {
		t.Errorf("without a link = %+v, want everyone skipped by the name link", without)
	}
}

// The transactional API is not a campaign: a caller there names its own
// variables, and one called link still fills {{link}} as it did before.
func TestASingleSendStillFillsLinkFromItsOwnVariables(t *testing.T) {
	f := newFixture(t)
	template := f.seedSMSTemplate(f.senderID, "Track at {{link}}")
	result, err := f.service.Send(context.Background(), f.identity, sending.SendRequest{
		SenderID: f.senderID, TemplateID: &template, Msisdn: "+919820000421",
		Body: "Track at {{link}}", Variables: map[string]string{"link": "https://track.example/1"}})
	if err != nil || result.Status == "rejected" {
		t.Fatalf("send = %+v, %v; want it sent with the caller's own link", result, err)
	}
}
