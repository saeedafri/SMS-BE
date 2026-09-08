package sending_test

import (
	"context"
	"testing"

	"github.com/saeedafri/sms-be/internal/store"
)

// The recipients endpoint over a campaign that has a real list.
//
// This path had no test and a live 500 was the first thing to notice: the query
// named placeholders the args slice did not supply, which the planner cannot
// type and which fails at execution rather than at compile. Every earlier check
// passed because the campaigns it ran against had no list, so the function
// returned before building any SQL.
func TestCampaignRecipientsQueryRunsOnACampaignWithARealList(t *testing.T) {
	f := newFixture(t)
	listID, optedIn := f.seedMixedConsentList("recipients query")
	template := f.seedTemplate()
	campaign := f.seedCampaign(template, listID, "sent", len(optedIn))

	for _, state := range []string{"", "dispatched", "cancelled"} {
		rows, total, err := store.ListCampaignRecipients(context.Background(),
			f.service.DB, f.identity, campaign, state, 1, 50)
		if err != nil {
			t.Fatalf("state=%q: %v", state, err)
		}
		// The audience is the consent-filtered list, not every member.
		if state != "cancelled" && total != len(optedIn) {
			t.Errorf("state=%q total = %d, want %d — the audience is the same one "+
				"the estimate counts and the fan-out walks", state, total, len(optedIn))
		}
		for _, row := range rows {
			if !optedIn[row.Identity] {
				t.Errorf("state=%q returned %s, which never opted in", state, row.Identity)
			}
		}
	}

	// A campaign that completed has nobody it failed to reach.
	_, cancelled, err := store.ListCampaignRecipients(context.Background(),
		f.service.DB, f.identity, campaign, "cancelled", 1, 50)
	if err != nil {
		t.Fatalf("cancelled: %v", err)
	}
	if cancelled != 0 {
		t.Errorf("a completed campaign reports %d cancelled recipients, want 0", cancelled)
	}
}
