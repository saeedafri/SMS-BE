package sending_test

import (
	"context"
	"testing"

	"github.com/saeedafri/sms-be/internal/store"
)

// The cancelled half over a campaign that has a real list.
//
// This path had no test and a live 500 was the first thing to notice: the query
// named placeholders the args slice did not supply, which the planner cannot
// type and which fails at execution rather than at compile. Every earlier check
// passed because the campaigns it ran against had no list, so the function
// returned before building any SQL.
//
// It also fixes the audience rule in place for this half, which is the half
// that keeps it. A cancelled recipient is a counterfactual — who the run would
// have reached had it continued — so it is read against the list and the
// consent map as they stand, and a contact who never opted in was never in it.
func TestCancelledRecipientsQueryRunsOnACampaignWithARealList(t *testing.T) {
	f := newFixture(t)
	listID, optedIn := f.seedMixedConsentList("recipients query")
	template := f.seedTemplate()

	// Cancelled before its first page, so the whole audience went unreached.
	campaign := f.seedCampaign(template, listID, "cancelled", len(optedIn))

	rows, total, err := store.ListCancelledRecipients(context.Background(),
		f.service.DB, f.identity, campaign, 0, 50)
	if err != nil {
		t.Fatalf("cancelled: %v", err)
	}
	if total != len(optedIn) {
		t.Errorf("total = %d, want %d — the counterfactual is the same audience "+
			"the estimate counts and the fan-out walks", total, len(optedIn))
	}
	if len(rows) != len(optedIn) {
		t.Errorf("returned %d rows against a total of %d", len(rows), total)
	}
	for _, row := range rows {
		if !optedIn[row.Identity] {
			t.Errorf("returned %s, which never opted in on this channel", row.Identity)
		}
	}

	// A limit of 0 asks for the total alone — the caller needs it for the
	// envelope even on a page the dispatched half filled completely.
	none, sameTotal, err := store.ListCancelledRecipients(context.Background(),
		f.service.DB, f.identity, campaign, 0, 0)
	if err != nil {
		t.Fatalf("count only: %v", err)
	}
	if len(none) != 0 || sameTotal != total {
		t.Errorf("limit 0 returned %d rows and total %d, want 0 rows and %d",
			len(none), sameTotal, total)
	}

	// A campaign that completed has nobody it failed to reach, whatever its
	// cursor happens to say.
	sent := f.seedCampaign(template, listID, "sent", len(optedIn))
	if _, cancelled, err := store.ListCancelledRecipients(context.Background(),
		f.service.DB, f.identity, sent, 0, 50); err != nil {
		t.Fatalf("sent: %v", err)
	} else if cancelled != 0 {
		t.Errorf("a completed campaign reports %d cancelled recipients, want 0", cancelled)
	}
}
