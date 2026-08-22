package sending_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"
)

// A campaign abandoned mid-fan-out must land on what actually happened.
//
// Fan-out sets 'sending' and then sets a terminal status. A deploy, a panic or
// a ClickHouse blip between those two leaves the row at 'sending' with nothing
// running to move it — the customer watches a campaign send forever, and the
// delivered-versus-failed split they are billed against never appears. 786 of
// 1,200 campaigns were sitting in exactly that state when this was written.
func TestStuckCampaignsLandOnWhatActuallySent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	operatorPool, err := store.OpenOperatorPool(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("open operator pool: %v", err)
	}
	t.Cleanup(operatorPool.Close)

	templateID := f.seedTemplate()
	// Nothing ever left this one.
	silent := f.seedStuckCampaign(templateID, "Silent", time.Now().UTC().Add(-time.Hour))
	// This one got a message out before it was abandoned.
	partial := f.seedStuckCampaign(templateID, "Partial", time.Now().UTC().Add(-time.Hour))
	if _, err := f.service.Send(ctx, f.identity, sending.SendRequest{
		SenderID: f.senderID, Msisdn: "9876543210", Body: "Hello",
		CampaignID: &partial,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	// Started a moment ago: still plausibly running, and landing it would
	// report a send finished while messages were still leaving.
	running := f.seedStuckCampaign(templateID, "Running", time.Now().UTC())

	// ClickHouse makes a fresh insert visible to this filter within a few
	// hundred milliseconds. Irrelevant in production — the sweep runs every 15
	// minutes against campaigns 15 minutes old — so waiting keeps the test
	// about the sweep rather than about insert visibility.
	deadline := time.Now().Add(5 * time.Second)
	for {
		counts, err := store.CountCampaignMessages(ctx, f.service.ClickHouse,
			f.identity.TenantID, partial)
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if counts.Queued+counts.Sent+counts.Delivered > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	landed, err := sending.ReconcileStuckCampaigns(ctx, operatorPool, f.service.DB,
		f.service.ClickHouse, sending.StuckCampaignWindow, 100)
	if err != nil {
		t.Fatalf("reconcile campaigns: %v", err)
	}
	if landed < 2 {
		t.Fatalf("landed %d campaigns, want at least the 2 stuck ones", landed)
	}

	if status := f.campaignStatus(silent); status != "failed" {
		t.Errorf("a campaign that sent nothing landed as %q, want failed", status)
	}
	if status := f.campaignStatus(partial); status != "sent" {
		t.Errorf("a campaign that got messages out landed as %q, want sent", status)
	}
	if status := f.campaignStatus(running); status != "sending" {
		t.Errorf("a campaign that started moments ago was landed as %q — the sweep "+
			"reported a send finished while it was still running", status)
	}
}

func (f *fixture) seedTemplate() uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	if err := store.WithTenant(context.Background(), f.service.DB, f.identity.TenantID,
		func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `
		INSERT INTO templates (id, tenant_id, sender_id, name, channel, country, body, status)
		VALUES ($1, $2, $3, 'Reconcile fixture', 'SMS', 'IN', 'Hello', 'approved')`,
				id, f.identity.TenantID, f.senderID)
			return err
		}); err != nil {
		f.t.Fatalf("seed template: %v", err)
	}
	return id
}

func (f *fixture) seedStuckCampaign(templateID uuid.UUID, name string, startedAt time.Time) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	if err := store.WithTenant(context.Background(), f.service.DB, f.identity.TenantID,
		func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `
		INSERT INTO campaigns (id, tenant_id, name, channel, country, sender_id,
		    template_id, status, send_started_at, recipients, segments_per_message,
		    cost_minor_min, cost_minor_max, currency)
		VALUES ($1, $2, $3, 'SMS', 'IN', $4, $5, 'sending', $6, 1, 1, 12, 12, 'INR')`,
				id, f.identity.TenantID, name, f.senderID, templateID, startedAt)
			return err
		}); err != nil {
		f.t.Fatalf("seed campaign: %v", err)
	}
	return id
}

func (f *fixture) campaignStatus(id uuid.UUID) string {
	f.t.Helper()
	var status string
	if err := store.WithTenant(context.Background(), f.service.DB, f.identity.TenantID,
		func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(),
				`SELECT status FROM campaigns WHERE id = $1`, id).Scan(&status)
		}); err != nil {
		f.t.Fatalf("read campaign status: %v", err)
	}
	return status
}
