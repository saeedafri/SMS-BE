package sending_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/domain/audience"
	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"
)

// recordingCarrier is a stand-in RCS gateway that keeps what it was handed.
type recordingCarrier struct {
	submissions []connector.Submission
}

func (*recordingCarrier) Name() string { return "airtel" }

func (*recordingCarrier) Health(context.Context) connector.Health {
	return connector.Health{Healthy: true}
}

func (c *recordingCarrier) Submit(_ context.Context,
	submissions []connector.Submission) ([]connector.Receipt, error) {

	c.submissions = append(c.submissions, submissions...)
	receipts := make([]connector.Receipt, 0, len(submissions))
	for _, submission := range submissions {
		receipts = append(receipts, connector.Receipt{
			MessageID: submission.MessageID, Accepted: true,
			CarrierRef: "ref-" + submission.MessageID,
		})
	}
	return receipts, nil
}

// A campaign personalises its body from each contact's own fields. On RCS the
// CARRIER holds the approved template and renders it from what we pass, so a
// campaign that personalises the body and hands the carrier nothing writes
// "Hi Priya" into our log and puts "Hi " on the handset — with no error
// anywhere.
func TestAnRCSCampaignFillsTheCarrierTemplateFromEachContactsFields(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	carrier := &recordingCarrier{}
	f.service.Carriers = connector.Registry{
		Default:   f.service.Connector,
		ByChannel: map[string]connector.Connector{"RCS": carrier},
	}

	senderID := f.seedApprovedSender("RCSCMP", "RCS")
	templateID := f.seedCarrierApprovedRCSTemplate(senderID)
	listID := f.seedListWithNamedContacts(map[string]string{
		"919820000011": "Priya",
		"919820000012": "Vikram",
	})
	campaignID := f.seedRCSCampaign(senderID, templateID, listID)

	campaign, err := store.GetCampaign(ctx, f.service.DB, f.identity, campaignID)
	if err != nil {
		t.Fatalf("load campaign: %v", err)
	}
	if _, _, err := f.service.LaunchCampaign(ctx, f.identity, campaign); err != nil {
		t.Fatalf("launch: %v", err)
	}

	if len(carrier.submissions) != 2 {
		t.Fatalf("carrier saw %d submissions, want 2", len(carrier.submissions))
	}
	byNumber := map[string]string{}
	for _, submission := range carrier.submissions {
		for _, variable := range submission.TemplateVariables {
			if variable.Name == "first_name" {
				byNumber[submission.Msisdn] = variable.Value
			}
		}
		if submission.CarrierTemplateID == "" {
			t.Errorf("submission to %s carried no carrier template id", submission.Msisdn)
		}
	}
	if byNumber["+919820000011"] != "Priya" || byNumber["+919820000012"] != "Vikram" {
		t.Errorf("first_name values = %v, want each contact's own", byNumber)
	}
}

func (f *fixture) seedApprovedSender(header, channel string) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	f.exec(`INSERT INTO sender_ids (id, tenant_id, header, channel, country, status)
	        VALUES ($1, $2, $3, $4, 'IN', 'approved')`,
		id, f.identity.TenantID, header, channel)
	if channel == "RCS" {
		f.attachRCSAgent(id)
	}
	return id
}

// attachRCSAgent gives an RCS sender the brand identity it now cannot send
// without. There is no shared agent left to fall back to, so a sender with none
// is refused at the gate before the carrier is ever called — which reaches a
// test asserting what the carrier received as "carrier saw 0 submissions", with
// nothing naming the cause.
//
// Launched on AIRTEL because that is what the stub in this package calls itself
// and therefore what dedicatedCarrier resolves the route to.
func (f *fixture) attachRCSAgent(senderID uuid.UUID) {
	f.t.Helper()
	agentID := uuid.New()
	f.exec(`INSERT INTO rcs_agents (id, tenant_id, display_name, use_case, country,
	            status, verification_status, verification_reviewed_at)
	        VALUES ($1, $2, $3, 'TRANSACTIONAL', 'IN', 'live', 'approved', now())`,
		agentID, f.identity.TenantID, "Sending fixture "+agentID.String()[:8])
	// Unique per agent: rcs_launch_carrier_identity is UNIQUE on
	// (carrier, carrier_agent_id), so a fixed literal would collide with the
	// previous test's row on the second run.
	f.exec(`INSERT INTO rcs_agent_carrier_launches
	            (agent_id, tenant_id, carrier, status, carrier_agent_id, submitted_at)
	        VALUES ($1, $2, 'AIRTEL', 'approved', $3, now())`,
		agentID, f.identity.TenantID, "airtel-"+agentID.String())
	f.exec(`UPDATE sender_ids SET rcs_agent_id = $1 WHERE id = $2`, agentID, senderID)
}

// seedCarrierApprovedRCSTemplate is approved on BOTH sides: Relay's review and
// the carrier's. Anything less is refused at the gate, which is tested
// elsewhere — this one is about what reaches the carrier once it passes.
func (f *fixture) seedCarrierApprovedRCSTemplate(senderID uuid.UUID) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	f.exec(`INSERT INTO templates (id, tenant_id, sender_id, name, channel, country,
	            variables, status, category, rcs_content,
	            carrier_vendor, carrier_template_id, carrier_status, carrier_submitted_at)
	        VALUES ($1, $2, $3, $4, 'RCS', 'IN', ARRAY['first_name'], 'approved', 'UTILITY',
	                $5::jsonb, 'airtel', $6, 'approved', now())`,
		id, f.identity.TenantID, senderID, "RCS campaign "+id.String()[:8],
		`{"kind":"text","text":"Hi {{first_name}}, welcome.","suggestions":[]}`,
		"carrier-"+id.String()[:12])
	return id
}

func (f *fixture) seedListWithNamedContacts(namesByMsisdn map[string]string) uuid.UUID {
	f.t.Helper()
	listID := uuid.New()
	f.exec(`INSERT INTO contact_lists (id, tenant_id, name) VALUES ($1, $2, 'RCS campaign list')`,
		listID, f.identity.TenantID)

	for msisdn, name := range namesByMsisdn {
		contactID := uuid.New()
		f.exec(`INSERT INTO contacts (id, tenant_id, msisdn, country, fields, consent)
		        VALUES ($1, $2, $3, 'IN', $4::jsonb, $5::jsonb)`,
			contactID, f.identity.TenantID, "+"+msisdn,
			`{"first_name":"`+name+`"}`, `{"RCS":"opted_in"}`)
		f.exec(`INSERT INTO contact_list_members (list_id, contact_id, tenant_id)
		        VALUES ($1, $2, $3)`, listID, contactID, f.identity.TenantID)
	}
	return listID
}

func (f *fixture) seedRCSCampaign(senderID, templateID, listID uuid.UUID) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	f.exec(`INSERT INTO campaigns (id, tenant_id, name, channel, country, list_id,
	            sender_id, template_id, status, recipients, segments_per_message_min, segments_per_message_max,
	            cost_minor_min, cost_minor_max, currency)
	        VALUES ($1, $2, 'RCS personalised', 'RCS', 'IN', $3, $4, $5, 'queued',
	                2, 1, 1, 35, 70, 'INR')`,
		id, f.identity.TenantID, listID, senderID, templateID)
	return id
}

// exec runs one statement inside the tenant's own RLS context, which is how
// every other write in these tests reaches the database.
func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if err := store.WithTenant(context.Background(), f.service.DB, f.identity.TenantID,
		func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), sql, args...)
			return err
		}); err != nil {
		f.t.Fatalf("seed: %v", err)
	}
}

// An RCS template keeps its words in a card, and template.Body is null on
// purpose. The fan-out read that null body, found no slots in an empty string,
// and so skipped nobody — on the one channel where the carrier renders from
// what we pass. Every contact was dispatched and charged for, and the card's
// own {{offer}} was never looked at.
func TestAnRCSCardCampaignSkipsContactsItCannotFill(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	carrier := &recordingCarrier{}
	f.service.Carriers = connector.Registry{
		Default:   f.service.Connector,
		ByChannel: map[string]connector.Connector{"RCS": carrier},
	}

	senderID := f.seedApprovedSender("RCSCARD", "RCS")
	// The card needs an offer. Nobody on this list has one.
	templateID := f.seedRCSCardTemplate(senderID, []string{"first_name", "offer"})
	listID := f.seedListWithNamedContacts(map[string]string{
		"919820000021": "Priya",
		"919820000022": "Vikram",
	})
	campaign, err := store.GetCampaign(ctx, f.service.DB, f.identity,
		f.seedRCSCampaign(senderID, templateID, listID))
	if err != nil {
		t.Fatalf("load campaign: %v", err)
	}

	sent, failed, err := f.service.LaunchCampaign(ctx, f.identity, campaign)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if len(carrier.submissions) != 0 {
		t.Fatalf("carrier saw %d submissions; a card nobody can fill must reach no handset",
			len(carrier.submissions))
	}
	if sent != 0 || failed != 0 {
		t.Fatalf("sent=%d failed=%d; a skipped contact is neither sent nor failed", sent, failed)
	}
}

// The other half of the same walk: when every slot in the card DOES resolve,
// the message goes, and the text handed to an operator that holds no template
// of its own is the card's text with the values in it.
func TestAnRCSCardCampaignFillsEveryStringItSends(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	carrier := &recordingCarrier{}
	f.service.Carriers = connector.Registry{
		Default:   f.service.Connector,
		ByChannel: map[string]connector.Connector{"RCS": carrier},
	}

	senderID := f.seedApprovedSender("RCSFILL", "RCS")
	templateID := f.seedRCSCardTemplate(senderID, []string{"first_name", "offer"})
	listID := f.seedListWithFields(map[string]map[string]string{
		"919820000031": {"first_name": "Priya", "offer": "20% off"},
	})
	campaign, err := store.GetCampaign(ctx, f.service.DB, f.identity,
		f.seedRCSCampaign(senderID, templateID, listID))
	if err != nil {
		t.Fatalf("load campaign: %v", err)
	}
	if _, _, err := f.service.LaunchCampaign(ctx, f.identity, campaign); err != nil {
		t.Fatalf("launch: %v", err)
	}

	if len(carrier.submissions) != 1 {
		t.Fatalf("carrier saw %d submissions, want 1", len(carrier.submissions))
	}
	submission := carrier.submissions[0]
	if strings.Contains(submission.Body, "{{") {
		t.Errorf("a token survived into the dispatched text: %q", submission.Body)
	}
	if submission.Body != "Hi Priya" {
		t.Errorf("dispatched text = %q, want the card's own text filled", submission.Body)
	}
	values := map[string]string{}
	for _, variable := range submission.TemplateVariables {
		values[variable.Name] = variable.Value
	}
	if values["offer"] != "20% off" {
		t.Errorf("the carrier was handed offer=%q; it holds the card and renders it from this",
			values["offer"])
	}
}

// seedRCSCardTemplate is the shape RCS exists for: no body, and the words in a
// card and its suggestions rather than in a top-level string.
func (f *fixture) seedRCSCardTemplate(senderID uuid.UUID, variables []string) uuid.UUID {
	f.t.Helper()
	return f.seedRCSCard(senderID, variables,
		`{"kind":"card","text":"Hi {{first_name}}","card":{"title":"{{offer}} just for you",`+
			`"description":"Tap below"},"suggestions":[{"text":"See {{offer}}",`+
			`"url":"https://example.test/o"}]}`)
}

// seedRCSCard is an approved, carrier-registered RCS template with exactly this
// content and these declared variables.
func (f *fixture) seedRCSCard(senderID uuid.UUID, variables []string, content string) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	f.exec(`INSERT INTO templates (id, tenant_id, sender_id, name, channel, country,
	            variables, status, category, rcs_content,
	            carrier_vendor, carrier_template_id, carrier_status, carrier_submitted_at)
	        VALUES ($1, $2, $3, $4, 'RCS', 'IN', $5, 'approved', 'UTILITY',
	                $6::jsonb, 'airtel', $7, 'approved', now())`,
		id, f.identity.TenantID, senderID, "RCS card "+id.String()[:8], variables,
		content, "carrier-"+id.String()[:12])
	return id
}

// seedListWithFields gives each contact whatever columns the test names, rather
// than only a first name.
func (f *fixture) seedListWithFields(fieldsByMsisdn map[string]map[string]string) uuid.UUID {
	f.t.Helper()
	listID := uuid.New()
	f.exec(`INSERT INTO contact_lists (id, tenant_id, name) VALUES ($1, $2, 'RCS card list')`,
		listID, f.identity.TenantID)
	for msisdn, fields := range fieldsByMsisdn {
		encoded, err := json.Marshal(fields)
		if err != nil {
			f.t.Fatalf("encode fields: %v", err)
		}
		contactID := uuid.New()
		f.exec(`INSERT INTO contacts (id, tenant_id, msisdn, country, fields, consent)
		        VALUES ($1, $2, $3, 'IN', $4::jsonb, $5::jsonb)`,
			contactID, f.identity.TenantID, "+"+msisdn, encoded, `{"RCS":"opted_in"}`)
		f.exec(`INSERT INTO contact_list_members (list_id, contact_id, tenant_id)
		        VALUES ($1, $2, $3)`, listID, contactID, f.identity.TenantID)
	}
	return listID
}

// twoLeg is an RCS campaign with an SMS fallback, and a stub per leg that keeps
// what it was handed.
//
// SMS is the Registry DEFAULT, not a ByChannel entry, on purpose: a channel with
// its own gateway is "dedicated", and the gate then demands a carrier-approved
// template for it — right for RCS, where the carrier holds the template, wrong
// for SMS, which goes over an operator bind. Registering the SMS stub by channel
// would have every fallback refused for a reason production never produces.
type twoLeg struct {
	f          *fixture
	rcs, sms   *recordingCarrier
	rcsSender  uuid.UUID
	rcsCard    uuid.UUID
	smsSender  uuid.UUID
	smsMessage uuid.UUID
}

// newTwoLeg builds the legs. The RCS card needs {{first_name}} and {{offer}};
// the SMS needs only {{first_name}}. So a contact with no offer is exactly the
// one the primary cannot fill and the fallback can.
func newTwoLeg(t *testing.T, rcsGateway connector.Connector) *twoLeg {
	t.Helper()
	leg := &twoLeg{f: newFixture(t), rcs: &recordingCarrier{}, sms: &recordingCarrier{}}
	if rcsGateway == nil {
		rcsGateway = leg.rcs
	}
	leg.f.service.Carriers = connector.Registry{
		Default:   leg.sms,
		ByChannel: map[string]connector.Connector{"RCS": rcsGateway},
	}
	leg.rcsSender = leg.f.seedApprovedSender("RCSP"+uuid.NewString()[:4], "RCS")
	leg.rcsCard = leg.f.seedRCSCardTemplate(leg.rcsSender, []string{"first_name", "offer"})
	leg.smsSender = leg.f.seedApprovedSender("SMSF"+uuid.NewString()[:4], "SMS")
	leg.smsMessage = leg.f.seedSMSTemplate(leg.smsSender, "Hi {{first_name}}, your order shipped.")
	return leg
}

// launch sends the campaign to these contacts and returns it with its counts.
func (l *twoLeg) launch(t *testing.T, seeds map[string]contactSeed) (uuid.UUID, int, int) {
	t.Helper()
	ctx := context.Background()
	listID := l.f.seedListWithConsent(seeds)
	campaignID := l.f.seedCampaignWithFallback(l.rcsSender, l.rcsCard, listID,
		"SMS", l.smsSender, l.smsMessage)
	campaign, err := store.GetCampaign(ctx, l.f.service.DB, l.f.identity, campaignID)
	if err != nil {
		t.Fatalf("load campaign: %v", err)
	}
	sent, failed, err := l.f.service.LaunchCampaign(ctx, l.f.identity, campaign)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	return campaignID, sent, failed
}

// rows reads back every message row the campaign wrote, by number.
func (l *twoLeg) rows(t *testing.T, campaignID uuid.UUID) map[string]store.MessageRecord {
	t.Helper()
	records, _, err := store.QueryMessages(context.Background(), l.f.service.ClickHouse,
		l.f.identity.TenantID, store.MessageFilter{CampaignID: &campaignID, Limit: 200})
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	out := map[string]store.MessageRecord{}
	for _, record := range records {
		if _, twice := out[record.Msisdn]; twice {
			t.Fatalf("%s has two message rows; a recipient is sent once, whatever leg carries them",
				record.Msisdn)
		}
		out[record.Msisdn] = record
	}
	return out
}

var both = map[string]string{"RCS": "opted_in", "SMS": "opted_in"}

// T2, T9, T10, T12. A recipient the RCS card cannot be filled for is carried
// by the SMS fallback instead of dropped — once, at the SMS price, on a row
// that says SMS carried it while staying filed under the RCS campaign.
func TestTheFallbackCarriesTheRecipientsThePrimaryCannotFill(t *testing.T) {
	leg := newTwoLeg(t, nil)
	campaignID, sent, failed := leg.launch(t, map[string]contactSeed{
		"919820000061": {fields: map[string]string{"first_name": "Priya"}, consent: both},
		"919820000062": {fields: map[string]string{"first_name": "Vikram", "offer": "20% off"},
			consent: both},
	})

	if sent != 2 || failed != 0 {
		t.Fatalf("sent=%d failed=%d, want 2 and 0", sent, failed)
	}
	if len(leg.rcs.submissions) != 1 || leg.rcs.submissions[0].Msisdn != "+919820000062" {
		t.Fatalf("RCS carried %d messages, want only the contact with an offer", len(leg.rcs.submissions))
	}
	if len(leg.sms.submissions) != 1 || leg.sms.submissions[0].Msisdn != "+919820000061" {
		t.Fatalf("SMS carried %d messages, want only the contact without an offer", len(leg.sms.submissions))
	}
	if body := leg.sms.submissions[0].Body; body != "Hi Priya, your order shipped." {
		t.Errorf("fallback body = %q, want the SMS template filled", body)
	}

	rows := leg.rows(t, campaignID)
	fellBack, stayed := rows["+919820000061"], rows["+919820000062"]
	if fellBack.Channel != "RCS" {
		t.Errorf("fallback row filed under %q, want the campaign's own channel RCS", fellBack.Channel)
	}
	if fellBack.DeliveredChannel == nil || *fellBack.DeliveredChannel != "SMS" {
		t.Errorf("fallback row deliveredChannel = %v, want SMS", fellBack.DeliveredChannel)
	}
	if stayed.DeliveredChannel == nil || *stayed.DeliveredChannel != "RCS" {
		t.Errorf("primary row deliveredChannel = %v, want RCS", stayed.DeliveredChannel)
	}

	// T9: charged at the SMS rate for the SMS body's segments, never the RCS price.
	smsRate, err := store.FindPricingRate(context.Background(), leg.f.service.DB,
		leg.f.identity.TenantID, "IN", "SMS", "")
	if err != nil {
		t.Fatalf("sms rate: %v", err)
	}
	if want := int64(fellBack.Segments) * smsRate.PerSegmentMinor; fellBack.CostMinor != want {
		t.Errorf("fallback cost = %d, want %d (SMS rate x SMS segments)", fellBack.CostMinor, want)
	}
}

// The audience is the PRIMARY channel's. Somebody who consented to SMS and
// never to RCS is not in an RCS campaign's audience, and a fallback must not
// bring them in — that would message a person about a campaign they never
// agreed to receive, on a channel chosen only because they happen to have it.
func TestAFallbackNeverAddsSomeoneOutsideThePrimaryAudience(t *testing.T) {
	leg := newTwoLeg(t, nil)
	campaignID, sent, _ := leg.launch(t, map[string]contactSeed{
		// Fillable on BOTH legs, so nothing but the audience rule can stop them.
		"919820000071": {fields: map[string]string{"first_name": "Asha", "offer": "20% off"},
			consent: map[string]string{"SMS": "opted_in"}},
	})
	if sent != 0 || len(leg.sms.submissions)+len(leg.rcs.submissions) != 0 {
		t.Fatalf("sent %d to a contact who never consented to this campaign's channel", sent)
	}
	if rows := leg.rows(t, campaignID); len(rows) != 0 {
		t.Fatalf("%d rows written for somebody outside the audience", len(rows))
	}
}

// T6, the compliance case. Consent is per channel: somebody opted in to RCS and
// not to SMS must not receive the SMS fallback. Skipped, never sent — a test
// that passes by sending is a failure.
func TestAFallbackIsNeverSentWithoutItsOwnConsent(t *testing.T) {
	leg := newTwoLeg(t, nil)
	_, sent, failed := leg.launch(t, map[string]contactSeed{
		"919820000081": {fields: map[string]string{"first_name": "Ravi"},
			consent: map[string]string{"RCS": "opted_in"}},
	})
	if len(leg.sms.submissions) != 0 {
		t.Fatal("the SMS fallback went to a contact who never consented to SMS")
	}
	if sent != 0 || failed != 0 {
		t.Fatalf("sent=%d failed=%d; an unfillable contact with no usable fallback is skipped", sent, failed)
	}
}

// T7. Suppression beats consent on every leg.
func TestASuppressedContactIsSentNeitherLeg(t *testing.T) {
	leg := newTwoLeg(t, nil)
	leg.f.exec(`INSERT INTO suppressions (tenant_id, identity, msisdn, reason)
	            VALUES ($1, '+919820000091', '+919820000091', 'opted_out_keyword')`,
		leg.f.identity.TenantID)
	_, sent, _ := leg.launch(t, map[string]contactSeed{
		"919820000091": {fields: map[string]string{"first_name": "Meera"}, consent: both},
	})
	if sent != 0 || len(leg.sms.submissions)+len(leg.rcs.submissions) != 0 {
		t.Fatal("a suppressed contact was sent a message on some leg")
	}
}

// T8. A fallback whose sender is not approved is refused by the gate like any
// other send — recorded, with its code, and never silently dropped.
func TestAnUnapprovedFallbackSenderIsRefusedWithItsCode(t *testing.T) {
	leg := newTwoLeg(t, nil)
	leg.f.exec(`UPDATE sender_ids SET status = 'pending_review' WHERE id = $1`, leg.smsSender)
	campaignID, sent, failed := leg.launch(t, map[string]contactSeed{
		"919820000101": {fields: map[string]string{"first_name": "Kiran"}, consent: both},
	})
	if len(leg.sms.submissions) != 0 {
		t.Fatal("an unapproved fallback sender reached the carrier")
	}
	if sent != 0 || failed != 1 {
		t.Fatalf("sent=%d failed=%d, want the refusal counted", sent, failed)
	}
	row, ok := leg.rows(t, campaignID)["+919820000101"]
	if !ok {
		t.Fatal("the refusal was not recorded; a tenant asking why is owed a row")
	}
	if row.ErrorCode == nil || *row.ErrorCode != "sender_not_approved" {
		t.Errorf("error code = %v, want sender_not_approved", row.ErrorCode)
	}
	if row.DeliveredChannel != nil {
		t.Errorf("deliveredChannel = %q on a refusal; nothing carried it", *row.DeliveredChannel)
	}
}

// reachabilityCarrier is an RCS gateway that answers capability lookups: only
// the numbers in `capable` can receive RCS.
type reachabilityCarrier struct {
	recordingCarrier
	capable map[string]bool
	asked   int
}

func (*reachabilityCarrier) Vendor() string { return "airtel" }

func (c *reachabilityCarrier) Capability(context.Context, string, string) (connector.RCSCapability, error) {
	return connector.RCSCapability{}, nil
}

func (c *reachabilityCarrier) Reachable(_ context.Context, _ string, msisdns []string) ([]string, error) {
	c.asked++
	var out []string
	for _, msisdn := range msisdns {
		if c.capable[msisdn] {
			out = append(out, msisdn)
		}
	}
	return out, nil
}

// T3, ask 60. A handset the carrier says cannot receive RCS goes by the
// fallback even though the RCS message could be filled — and the page is asked
// about in ONE lookup, not one per contact.
func TestAnUnreachableHandsetGoesByTheFallback(t *testing.T) {
	gateway := &reachabilityCarrier{capable: map[string]bool{"+919820000112": true}}
	leg := newTwoLeg(t, gateway)
	full := map[string]string{"first_name": "Dev", "offer": "20% off"}
	campaignID, sent, _ := leg.launch(t, map[string]contactSeed{
		"919820000111": {fields: full, consent: both},
		"919820000112": {fields: full, consent: both},
	})
	if sent != 2 {
		t.Fatalf("sent = %d, want 2", sent)
	}
	if gateway.asked != 1 {
		t.Errorf("reachability asked %d times for one page, want 1", gateway.asked)
	}
	if len(gateway.submissions) != 1 || gateway.submissions[0].Msisdn != "+919820000112" {
		t.Errorf("RCS carried %d, want only the capable handset", len(gateway.submissions))
	}
	if len(leg.sms.submissions) != 1 || leg.sms.submissions[0].Msisdn != "+919820000111" {
		t.Errorf("SMS carried %d, want only the handset without RCS", len(leg.sms.submissions))
	}
	if row := leg.rows(t, campaignID)["+919820000111"]; row.DeliveredChannel == nil ||
		*row.DeliveredChannel != "SMS" {
		t.Errorf("deliveredChannel = %v, want SMS", row.DeliveredChannel)
	}
}

// T20. A campaign where every recipient falls back completes normally: the
// primary leg sends nothing, and that is not an error.
func TestEveryRecipientFallingBackIsNotAnError(t *testing.T) {
	leg := newTwoLeg(t, nil)
	campaignID, sent, failed := leg.launch(t, map[string]contactSeed{
		"919820000121": {fields: map[string]string{"first_name": "A"}, consent: both},
		"919820000122": {fields: map[string]string{"first_name": "B"}, consent: both},
		"919820000123": {fields: map[string]string{"first_name": "C"}, consent: both},
	})
	if sent != 3 || failed != 0 || len(leg.rcs.submissions) != 0 {
		t.Fatalf("sent=%d failed=%d rcs=%d, want all three by SMS", sent, failed, len(leg.rcs.submissions))
	}
	if status := leg.f.campaignStatus(campaignID); status != "sent" {
		t.Errorf("campaign status = %q, want sent", status)
	}
}

// T14, at the service: the worked example, scaled. An RCS card needing
// {{product}} with an SMS fallback that needs nothing; of five contacts two have
// no product. recipients stays at five — those two are a delivery by another
// route, not a loss to be explained.
func TestTheEstimateCountsTheFallbackAsDeliveryNotLoss(t *testing.T) {
	f := newFixture(t)
	rcsSender := f.seedApprovedSender("RCSE"+uuid.NewString()[:4], "RCS")
	card := f.seedRCSCard(rcsSender, []string{"product"},
		`{"kind":"card","card":{"title":"Meet {{product}}","description":"Tap below"}}`)
	smsSender := f.seedApprovedSender("SMSE"+uuid.NewString()[:4], "SMS")
	plain := f.seedSMSTemplate(smsSender, "We have something new at Acme.")

	with := map[string]string{"product": "Widget"}
	listID := f.seedListWithConsent(map[string]contactSeed{
		"919820000131": {fields: with, consent: both},
		"919820000132": {fields: with, consent: both},
		"919820000133": {fields: with, consent: both},
		"919820000134": {fields: map[string]string{}, consent: both},
		"919820000135": {fields: map[string]string{}, consent: both},
	})
	ctx := context.Background()
	rcsTemplate, err := store.GetTemplate(ctx, f.service.DB, f.identity, card)
	if err != nil {
		t.Fatalf("load card: %v", err)
	}
	smsTemplate, err := store.GetTemplate(ctx, f.service.DB, f.identity, plain)
	if err != nil {
		t.Fatalf("load sms: %v", err)
	}
	estimate, err := f.service.EstimateCampaign(ctx, f.identity, &listID, "IN", "RCS",
		rcsTemplate, &sending.FallbackEstimate{Channel: "SMS", Template: smsTemplate}, time.Now())
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if estimate.Recipients != 5 || estimate.FallbackForced != 2 ||
		estimate.FallbackEligible != 5 || estimate.VariableSkipped != 0 {
		t.Fatalf("estimate = recipients %d, forced %d, eligible %d, skipped %d; "+
			"want 5, 2, 5, 0", estimate.Recipients, estimate.FallbackForced,
			estimate.FallbackEligible, estimate.VariableSkipped)
	}
}

// And the other half: a contact no leg can fill is still skipped, so the
// fallback is a second chance rather than a way round the rule.
func TestAContactNoLegCanFillIsStillSkipped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rcs := &recordingCarrier{}
	sms := &recordingCarrier{}
	// SMS is the DEFAULT rather than a ByChannel entry on purpose. A channel
	// with its own gateway is "dedicated", and the gate then demands a
	// carrier-approved template for it — true for RCS, where Airtel and Vi
	// hold the template, and not true for SMS, which goes over an operator
	// bind. Registering the stub by channel would have this test refuse every
	// fallback message for a reason production never produces.
	f.service.Carriers = connector.Registry{
		Default:   sms,
		ByChannel: map[string]connector.Connector{"RCS": rcs},
	}

	rcsSender := f.seedApprovedSender("RCSNONE", "RCS")
	rcsTemplate := f.seedRCSCardTemplate(rcsSender, []string{"first_name", "offer"})
	smsSender := f.seedApprovedSender("SMSNONE", "SMS")
	smsTemplate := f.seedSMSTemplate(smsSender, "Hi {{first_name}}, your order shipped.")

	// Neither column anywhere: the card needs an offer, the SMS needs a name.
	listID := f.seedListWithConsent(map[string]contactSeed{
		"919820000051": {fields: map[string]string{"city": "Pune"},
			consent: map[string]string{"RCS": "opted_in", "SMS": "opted_in"}},
	})
	campaign, err := store.GetCampaign(ctx, f.service.DB, f.identity,
		f.seedCampaignWithFallback(rcsSender, rcsTemplate, listID,
			"SMS", smsSender, smsTemplate))
	if err != nil {
		t.Fatalf("load campaign: %v", err)
	}
	sent, failed, err := f.service.LaunchCampaign(ctx, f.identity, campaign)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if len(rcs.submissions)+len(sms.submissions) != 0 {
		t.Fatalf("%d RCS and %d SMS messages left; no leg can fill for this contact",
			len(rcs.submissions), len(sms.submissions))
	}
	if sent != 0 || failed != 0 {
		t.Errorf("sent=%d failed=%d; a skipped contact is neither", sent, failed)
	}
}

type contactSeed struct {
	fields  map[string]string
	consent map[string]string
}

func (f *fixture) seedListWithConsent(seeds map[string]contactSeed) uuid.UUID {
	f.t.Helper()
	listID := uuid.New()
	f.exec(`INSERT INTO contact_lists (id, tenant_id, name) VALUES ($1, $2, 'Two-leg list')`,
		listID, f.identity.TenantID)
	for msisdn, seed := range seeds {
		fields, err := json.Marshal(seed.fields)
		if err != nil {
			f.t.Fatalf("encode fields: %v", err)
		}
		consent, err := json.Marshal(seed.consent)
		if err != nil {
			f.t.Fatalf("encode consent: %v", err)
		}
		contactID := uuid.New()
		f.exec(`INSERT INTO contacts (id, tenant_id, msisdn, country, fields, consent)
		        VALUES ($1, $2, $3, 'IN', $4::jsonb, $5::jsonb)`,
			contactID, f.identity.TenantID, "+"+msisdn, fields, consent)
		f.exec(`INSERT INTO contact_list_members (list_id, contact_id, tenant_id)
		        VALUES ($1, $2, $3)`, listID, contactID, f.identity.TenantID)
	}
	return listID
}

func (f *fixture) seedSMSTemplate(senderID uuid.UUID, body string) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	f.exec(`INSERT INTO templates (id, tenant_id, sender_id, name, channel, country,
	            body, variables, status, category)
	        VALUES ($1, $2, $3, $4, 'SMS', 'IN', $5, $6, 'approved', 'UTILITY')`,
		id, f.identity.TenantID, senderID, "SMS fallback "+id.String()[:8], body,
		// Declared from the body, as the templates API does. A fixed list here
		// would declare slots the body does not use, and every contact would
		// read as unfillable for a column the message never asks for.
		audience.SlotsIn(body))
	return id
}

func (f *fixture) seedCampaignWithFallback(senderID, templateID, listID uuid.UUID,
	fallbackChannel string, fallbackSender, fallbackTemplate uuid.UUID) uuid.UUID {

	f.t.Helper()
	id := uuid.New()
	f.exec(`INSERT INTO campaigns (id, tenant_id, name, channel, country, list_id,
	            sender_id, template_id, fallback_channel, fallback_sender_id,
	            fallback_template_id, status, recipients,
	            segments_per_message_min, segments_per_message_max,
	            cost_minor_min, cost_minor_max, currency)
	        VALUES ($1, $2, 'Two-leg campaign', 'RCS', 'IN', $3, $4, $5, $6, $7, $8,
	                'queued', 2, 1, 1, 35, 70, 'INR')`,
		id, f.identity.TenantID, listID, senderID, templateID,
		fallbackChannel, fallbackSender, fallbackTemplate)
	return id
}
