package api_test

import (
	"context"
	"testing"
)

// A campaign given a send time goes out when that time comes, and not before.
func TestAScheduledCampaignLaunchesWhenDueAndNotBefore(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	due := h.seedNamedCampaign(tenant, "Due", "scheduled")
	later := h.seedNamedCampaign(tenant, "Later", "scheduled")
	ctx := context.Background()
	if _, err := h.admin.Exec(ctx, `UPDATE campaigns SET scheduled_at = now() - interval '1 minute'
		WHERE id = $1`, due); err != nil {
		t.Fatalf("schedule due: %v", err)
	}
	if _, err := h.admin.Exec(ctx, `UPDATE campaigns SET scheduled_at = now() + interval '1 day'
		WHERE id = $1`, later); err != nil {
		t.Fatalf("schedule later: %v", err)
	}

	if err := h.server.LaunchDueCampaigns(ctx); err != nil {
		t.Logf("scheduler reported: %v", err)
	}

	status := func(id string) string {
		var value string
		if err := h.admin.QueryRow(ctx, `SELECT status FROM campaigns WHERE id = $1`, id).
			Scan(&value); err != nil {
			t.Fatalf("read status: %v", err)
		}
		return value
	}
	if got := status(due); got == "scheduled" || got == "queued" {
		t.Errorf("due campaign is still %q — nothing launched it", got)
	}
	if got := status(later); got != "scheduled" {
		t.Errorf("campaign due tomorrow is %q, want still scheduled", got)
	}
}
