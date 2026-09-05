package sending_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"
)

// The brake, at the level where dispatch actually happens.
//
// Fan-out is a loop that pages a contact list, so "stop dispatching" means the
// loop reads the campaign's status between pages and stops. These tests are
// deterministic on purpose: the halt is applied BEFORE the launch rather than
// raced against a running one, because a test that depends on winning a race
// against a five-hundred-recipient page proves nothing on a slow machine.

func (f *fixture) seedList(name string, contacts int) (uuid.UUID, []string) {
	f.t.Helper()
	listID := uuid.New()
	msisdns := make([]string, 0, contacts)
	if err := store.WithTenant(context.Background(), f.service.DB, f.identity.TenantID,
		func(tx pgx.Tx) error {
			if _, err := tx.Exec(context.Background(),
				`INSERT INTO contact_lists (id, tenant_id, name) VALUES ($1, $2, $3)`,
				listID, f.identity.TenantID, name); err != nil {
				return err
			}
			// Membership is its own table: a contact can be on several lists.
			for i := range contacts {
				msisdn := fmt.Sprintf("+9198765%05d", i+1)
				msisdns = append(msisdns, msisdn)
				contactID := uuid.New()
				if _, err := tx.Exec(context.Background(), `
					INSERT INTO contacts (id, tenant_id, msisdn, country, fields, consent)
					VALUES ($1, $2, $3, 'IN', '{}'::jsonb, '{"SMS":"opted_in"}'::jsonb)`,
					contactID, f.identity.TenantID, msisdn); err != nil {
					return err
				}
				if _, err := tx.Exec(context.Background(), `
					INSERT INTO contact_list_members (list_id, contact_id, tenant_id)
					VALUES ($1, $2, $3)`,
					listID, contactID, f.identity.TenantID); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
		f.t.Fatalf("seed list: %v", err)
	}
	return listID, msisdns
}

func (f *fixture) seedCampaign(templateID, listID uuid.UUID, status string, recipients int) store.Campaign {
	f.t.Helper()
	id := uuid.New()
	if err := store.WithTenant(context.Background(), f.service.DB, f.identity.TenantID,
		func(tx pgx.Tx) error {
			pausedAt, cancelledAt := "NULL", "NULL"
			if status == "paused" {
				pausedAt = "now()"
			}
			if status == "cancelled" {
				cancelledAt = "now()"
			}
			_, err := tx.Exec(context.Background(), fmt.Sprintf(`
				INSERT INTO campaigns (id, tenant_id, name, channel, country, sender_id,
				    template_id, list_id, status, recipients, segments_per_message,
				    cost_minor_min, cost_minor_max, currency, paused_at, cancelled_at)
				VALUES ($1, $2, 'Halt fixture', 'SMS', 'IN', $3, $4, $5, $6, $7, 1,
				        12, 12, 'INR', %s, %s)`, pausedAt, cancelledAt),
				id, f.identity.TenantID, f.senderID, templateID, listID, status, recipients)
			return err
		}); err != nil {
		f.t.Fatalf("seed campaign: %v", err)
	}
	campaign, err := store.GetCampaign(context.Background(), f.service.DB, f.identity, id)
	if err != nil {
		f.t.Fatalf("read back campaign: %v", err)
	}
	return campaign
}

func (f *fixture) campaignMessageCount(campaignID uuid.UUID) int {
	f.t.Helper()
	var total uint64
	if err := f.service.ClickHouse.QueryRow(context.Background(),
		`SELECT count(DISTINCT id) FROM messages WHERE tenant_id = ? AND campaign_id = ?`,
		f.identity.TenantID, campaignID).Scan(&total); err != nil {
		f.t.Fatalf("count campaign messages: %v", err)
	}
	return int(total)
}

// "No further recipient is dispatched." Not a smaller next batch — none.
func TestAPausedCampaignDispatchesNobodyAndStaysPaused(t *testing.T) {
	f := newFixture(t)
	template := f.seedTemplate()
	listID, _ := f.seedList("paused list", 5)
	campaign := f.seedCampaign(template, listID, "paused", 5)
	before := f.balance()

	sent, failed, err := f.service.LaunchCampaign(context.Background(), f.identity, campaign)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if sent != 0 || failed != 0 {
		t.Fatalf("a paused campaign dispatched %d sent / %d failed, want none", sent, failed)
	}
	if recorded := f.campaignMessageCount(campaign.ID); recorded != 0 {
		t.Fatalf("%d messages were recorded for a paused campaign", recorded)
	}
	if status := f.campaignStatus(campaign.ID); status != "paused" {
		t.Fatalf("status = %q after launching a paused campaign, want paused — "+
			"landing it as sent or failed silently undoes the brake", status)
	}
	if after := f.balance(); after != before {
		t.Fatalf("a paused campaign spent %d", before-after)
	}
}

// Cancel is terminal, and its recipients cost nothing: they were never
// dispatched, so they were never charged and there is nothing to refund.
func TestACancelledCampaignDispatchesNobodyAndIsNeverLandedAsSent(t *testing.T) {
	f := newFixture(t)
	template := f.seedTemplate()
	listID, _ := f.seedList("cancelled list", 5)
	campaign := f.seedCampaign(template, listID, "cancelled", 5)
	before := f.balance()

	sent, _, err := f.service.LaunchCampaign(context.Background(), f.identity, campaign)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if sent != 0 {
		t.Fatalf("a cancelled campaign dispatched %d", sent)
	}
	if status := f.campaignStatus(campaign.ID); status != "cancelled" {
		t.Fatalf("status = %q, want cancelled — cancel is terminal", status)
	}
	if after := f.balance(); after != before {
		t.Fatalf("a cancelled campaign spent %d — its recipients were never sent to", before-after)
	}
}

// Resume continues from where it stopped: nobody sent twice, nobody skipped.
func TestResumingDispatchesOnlyTheRecipientsAfterTheCursor(t *testing.T) {
	f := newFixture(t)
	template := f.seedTemplate()
	listID, msisdns := f.seedList("resume list", 6)
	campaign := f.seedCampaign(template, listID, "sending", len(msisdns))

	// A cursor sitting after the first three contacts, which is the state a
	// pause partway through the list leaves behind.
	cursor := f.contactCursorAfter(listID, 3)
	if err := store.SaveDispatchCursor(context.Background(), f.service.DB,
		f.identity, campaign.ID, cursor); err != nil {
		t.Fatalf("save cursor: %v", err)
	}
	campaign, err := store.GetCampaign(context.Background(), f.service.DB, f.identity, campaign.ID)
	if err != nil {
		t.Fatalf("re-read campaign: %v", err)
	}

	sent, failed, err := f.service.LaunchCampaign(context.Background(), f.identity, campaign)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if sent+failed != 3 {
		t.Fatalf("resumed dispatch covered %d recipients, want the 3 after the cursor — "+
			"more means someone was sent to twice, fewer means someone was skipped",
			sent+failed)
	}
	if recorded := f.campaignMessageCount(campaign.ID); recorded != 3 {
		t.Fatalf("%d messages recorded, want 3", recorded)
	}
}

// A campaign that finishes normally still ends 'sent', and the halt machinery
// must not have changed that.
func TestACampaignWithNoHaltStillCompletesNormally(t *testing.T) {
	f := newFixture(t)
	template := f.seedTemplate()
	listID, msisdns := f.seedList("normal list", 4)
	campaign := f.seedCampaign(template, listID, "queued", len(msisdns))

	sent, _, err := f.service.LaunchCampaign(context.Background(), f.identity, campaign)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if sent != len(msisdns) {
		t.Fatalf("sent %d of %d", sent, len(msisdns))
	}
	if status := f.campaignStatus(campaign.ID); status != "sent" {
		t.Fatalf("status = %q, want sent", status)
	}
}

// contactCursorAfter returns the paging cursor that a fan-out would hold after
// consuming exactly n contacts, by asking the same store function fan-out uses.
func (f *fixture) contactCursorAfter(listID uuid.UUID, n int) string {
	f.t.Helper()
	listRef := listID
	_, cursor, err := store.ListContactsAfter(context.Background(), f.service.DB,
		f.identity, &listRef, "", n)
	if err != nil {
		f.t.Fatalf("list contacts: %v", err)
	}
	if cursor == "" {
		f.t.Fatalf("no cursor after %d contacts — the list is too short to resume from", n)
	}
	return cursor
}

var _ = sending.DefaultValidityWindow
