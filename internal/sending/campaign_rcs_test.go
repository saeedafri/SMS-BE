package sending_test

import (
	"context"
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
	return id
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
	            sender_id, template_id, status, recipients, segments_per_message,
	            cost_minor_min, cost_minor_max, currency)
	        VALUES ($1, $2, 'RCS personalised', 'RCS', 'IN', $3, $4, $5, 'queued',
	                2, 1, 35, 70, 'INR')`,
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
