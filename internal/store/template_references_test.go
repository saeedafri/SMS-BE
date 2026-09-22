package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// A template is referenced by four things and only two of them are foreign
// keys. A campaign's template_id and fallback_template_id are enforced by the
// database, so a delete there fails whatever we do; a journey holds its steps
// as JSONB and nothing stops a step from pointing at a template that is gone.
// A count that walks foreign keys alone misses it, and the customer's journey
// then sends nothing with no row anywhere saying why.
func TestAJourneyStepCountsAsUseEvenWithNoForeignKeyBehindIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tenantID := uuid.New()
	admin := seedTenants(t, ctx, tenantID)
	pool := appPool(t, ctx)
	identity := store.Identity{TenantID: tenantID}

	var senderID uuid.UUID
	if err := admin.QueryRow(ctx,
		`INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		 VALUES ($1, 'REFCNT', 'SMS', 'IN', 'approved') RETURNING id`,
		tenantID).Scan(&senderID); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	newTemplate := func(name string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := admin.QueryRow(ctx,
			`INSERT INTO templates (tenant_id, sender_id, name, channel, country, body)
			 VALUES ($1, $2, $3, 'SMS', 'IN', 'Hello {{first_name}}') RETURNING id`,
			tenantID, senderID, name).Scan(&id); err != nil {
			t.Fatalf("seed template %s: %v", name, err)
		}
		return id
	}
	inJourney := newTemplate("used by a journey")
	inCampaign := newTemplate("used by a campaign")
	asFallback := newTemplate("used as a fallback")
	unused := newTemplate("used by nothing")

	if _, err := admin.Exec(ctx,
		`INSERT INTO journeys (tenant_id, name, trigger_type, steps)
		 VALUES ($1, 'refcount', 'list_entry',
		         jsonb_build_array(
		           jsonb_build_object('type', 'wait', 'id', 's1', 'durationMinutes', 60),
		           jsonb_build_object('type', 'send', 'id', 's2', 'channel', 'SMS',
		                              'senderId', $2::text, 'templateId', $3::text)))`,
		tenantID, senderID, inJourney); err != nil {
		t.Fatalf("seed journey: %v", err)
	}
	if _, err := admin.Exec(ctx,
		`INSERT INTO campaigns (tenant_id, name, channel, country, sender_id,
		                        template_id, fallback_template_id)
		 VALUES ($1, 'refcount', 'SMS', 'IN', $2, $3, $4)`,
		tenantID, senderID, inCampaign, asFallback); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}

	for _, want := range []struct {
		name     string
		id       uuid.UUID
		expected store.TemplateReferences
	}{
		{"journey step", inJourney, store.TemplateReferences{Journeys: 1}},
		{"campaign", inCampaign, store.TemplateReferences{Campaigns: 1}},
		{"campaign fallback", asFallback, store.TemplateReferences{CampaignFallback: 1}},
		{"nothing", unused, store.TemplateReferences{}},
	} {
		refs, err := store.CountTemplateReferences(ctx, pool, identity, want.id)
		if err != nil {
			t.Fatalf("%s: count: %v", want.name, err)
		}
		if refs != want.expected {
			t.Errorf("%s: references = %+v, want %+v", want.name, refs, want.expected)
		}
	}
}

// A template nothing references can go, and it goes for good.
func TestATemplateNothingReferencesCanBeDeleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tenantID := uuid.New()
	admin := seedTenants(t, ctx, tenantID)
	pool := appPool(t, ctx)
	identity := store.Identity{TenantID: tenantID}

	var senderID uuid.UUID
	if err := admin.QueryRow(ctx,
		`INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		 VALUES ($1, 'DELTPL', 'SMS', 'IN', 'approved') RETURNING id`,
		tenantID).Scan(&senderID); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	created, err := store.CreateTemplate(ctx, pool, identity, store.Template{
		SenderID: senderID, Name: "to be deleted", Channel: "SMS", Country: "IN",
		Body: strptr("Hello"), Variables: []string{},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := store.DeleteTemplate(ctx, pool, identity, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.GetTemplate(ctx, pool, identity, created.ID); err != store.ErrNotFound {
		t.Errorf("after delete, read = %v, want ErrNotFound", err)
	}
	if err := store.DeleteTemplate(ctx, pool, identity, created.ID); err != store.ErrNotFound {
		t.Errorf("deleting it twice = %v, want ErrNotFound", err)
	}
}

func strptr(s string) *string { return &s }
