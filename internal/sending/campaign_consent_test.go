package sending_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/saeedafri/sms-be/internal/store"
)

// seedMixedConsentList puts one list on the tenant whose members answered
// differently on SMS. Returns the list and the msisdns that opted in.
func (f *fixture) seedMixedConsentList(name string) (uuid.UUID, map[string]bool) {
	f.t.Helper()
	listID := uuid.New()
	optedIn := map[string]bool{}
	consents := []string{
		`{"SMS":"opted_in"}`, `{"SMS":"opted_in"}`, `{"SMS":"opted_in"}`,
		`{"SMS":"unknown"}`, `{"SMS":"opted_out"}`,
		`{"RCS":"opted_in"}`, // opted in, on a different channel
		`{}`,                 // never answered at all
	}
	ctx := context.Background()
	if err := store.WithTenant(ctx, f.service.DB, f.identity.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO contact_lists (id, tenant_id, name) VALUES ($1, $2, $3)`,
			listID, f.identity.TenantID, name); err != nil {
			return err
		}
		for i, consent := range consents {
			msisdn := fmt.Sprintf("+9197654%05d", i+1)
			contactID := uuid.New()
			if _, err := tx.Exec(ctx, `
				INSERT INTO contacts (id, tenant_id, msisdn, country, fields, consent)
				VALUES ($1, $2, $3, 'IN', '{}'::jsonb, $4::jsonb)`,
				contactID, f.identity.TenantID, msisdn, consent); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO contact_list_members (list_id, contact_id, tenant_id)
				VALUES ($1, $2, $3)`, listID, contactID, f.identity.TenantID); err != nil {
				return err
			}
			if consent == `{"SMS":"opted_in"}` {
				optedIn[msisdn] = true
			}
		}
		return nil
	}); err != nil {
		f.t.Fatalf("seed mixed-consent list: %v", err)
	}
	return listID, optedIn
}

// A campaign reaches only contacts who opted in on its channel, and the
// audience the estimate quotes is the audience the send walks.
//
// It was not. The estimate counted opted-in contacts; the fan-out paged the
// list with no consent predicate at all. Measured on production before this
// test existed: 2,500 contacts with no SMS consent key, a campaign that quoted
// ZERO recipients, and 2,500 messages dispatched. Suppression still caught
// anyone who had sent STOP, so the gap was never "we ignore opt-outs" — it was
// "we never check opt-in", which for A2P is the wrong side of the line.
func TestACampaignReachesOnlyContactsWhoOptedInOnItsChannel(t *testing.T) {
	f := newFixture(t)
	listID, optedIn := f.seedMixedConsentList("consent verification")

	reachable, err := store.ReachableOnChannel(context.Background(), f.service.DB,
		f.identity, &listID, "SMS")
	if err != nil {
		t.Fatalf("reachable: %v", err)
	}
	if reachable != len(optedIn) {
		t.Fatalf("estimate counts %d, want %d", reachable, len(optedIn))
	}

	// The pager the fan-out actually walks must return the same audience. This
	// is the assertion that was missing: the two used different rules.
	listRef := listID
	walked := map[string]bool{}
	cursor := ""
	for range 10 {
		contacts, next, err := store.ListContactsAfter(context.Background(),
			f.service.DB, f.identity, &listRef, "SMS", cursor, 3)
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		for _, contact := range contacts {
			walked[contact.Msisdn] = true
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(walked) != len(optedIn) {
		t.Errorf("fan-out would walk %d contacts, the estimate quoted %d — the send "+
			"and the quote must describe the same audience", len(walked), len(optedIn))
	}
	for msisdn := range walked {
		if !optedIn[msisdn] {
			t.Errorf("fan-out would reach %s, which never opted in on SMS", msisdn)
		}
	}
}
