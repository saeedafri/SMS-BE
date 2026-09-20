package sending_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/saeedafri/sms-be/internal/connector"
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
	id := uuid.New()
	f.exec(`INSERT INTO templates (id, tenant_id, sender_id, name, channel, country,
	            variables, status, category, rcs_content,
	            carrier_vendor, carrier_template_id, carrier_status, carrier_submitted_at)
	        VALUES ($1, $2, $3, $4, 'RCS', 'IN', $5, 'approved', 'UTILITY',
	                $6::jsonb, 'airtel', $7, 'approved', now())`,
		id, f.identity.TenantID, senderID, "RCS card "+id.String()[:8], variables,
		`{"kind":"card","text":"Hi {{first_name}}","card":{"title":"{{offer}} just for you",`+
			`"description":"Tap below"},"suggestions":[{"text":"See {{offer}}",`+
			`"url":"https://example.test/o"}]}`,
		"carrier-"+id.String()[:12])
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
