package sending_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"
)

// An SMS template names its slots {{var}}, and a single send supplies values
// for them. Before this, the values were passed to the CARRIER's template
// registry — which SMS does not have — and the text went out with the slot
// still in it. India's operators match it against the registered DLT template
// and deliver "Dear ," with the name missing, and nothing reports a failure.
func TestASingleSMSFillsItsTemplateSlotsFromTheSuppliedVariables(t *testing.T) {
	f := newFixture(t)
	carrier := &namedGateway{vendor: "videocon"}
	f.service.Carriers = connector.Registry{Default: carrier}

	templateID := uuid.New()
	if err := store.WithTenant(context.Background(), f.service.DB, f.identity.TenantID,
		func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `
			INSERT INTO templates (id, tenant_id, sender_id, name, channel, country,
			    body, variables, status, category)
			VALUES ($1, $2, $3, 'Registration', 'SMS', 'IN',
			    'Dear {{var}}, Your registration has been done.', ARRAY['var'],
			    'approved', 'UTILITY')`,
				templateID, f.identity.TenantID, f.senderID)
			return err
		}); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	if _, err := f.service.Send(context.Background(), f.identity, sending.SendRequest{
		SenderID: f.senderID, TemplateID: &templateID, Msisdn: "+919820000031",
		Body:      "Dear {{var}}, Your registration has been done.",
		Variables: map[string]string{"var": "Rahul"},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(carrier.submissions) != 1 {
		t.Fatalf("carrier saw %d submissions, want 1", len(carrier.submissions))
	}
	if body := carrier.submissions[0].Body; body != "Dear Rahul, Your registration has been done." {
		t.Errorf("the operator was handed %q, want the name filled in", body)
	}
}

// A slot the caller did not name still occupies its position.
//
// Airtel's placeholders are positional — {{1}} is whichever variable the
// template listed first — and it refuses a send carrying fewer values than the
// template declares. A silently shortened list would move every later variable
// one place to the left, putting the discount code where the customer's name
// should be.
//
// The gate now refuses such a send before it reaches a carrier at all, so in
// practice this list arrives complete. The rule is asserted here anyway,
// directly, because it is a property of how a carrier reads the list rather
// than of who is allowed to send one — and the end-to-end test that used to
// cover it was proving it by dispatching a message with a hole in it.
func TestAnUnsuppliedVariableKeepsItsPosition(t *testing.T) {
	template := store.Template{Variables: []string{"first_name", "order_id", "amount"}}

	filled := sending.TemplateVariables(template, map[string]string{
		"order_id": "A-2", "amount": "499",
	})

	if len(filled) != 3 {
		t.Fatalf("got %d values, want 3: a shortened list shifts every later slot", len(filled))
	}
	want := []struct{ name, value string }{
		{"first_name", ""}, {"order_id", "A-2"}, {"amount", "499"},
	}
	for i, expected := range want {
		if filled[i].Name != expected.name || filled[i].Value != expected.value {
			t.Errorf("slot %d = %+v, want %s=%q", i+1, filled[i], expected.name, expected.value)
		}
	}
}
