package api_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// seedCampaign puts a campaign straight into a given status. Creating one
// through POST /v1/campaigns would also dispatch it, which is the opposite of
// what the transition matrix needs to look at.
func (h *harness) seedCampaign(tenant account, status string) string {
	h.t.Helper()
	ctx := context.Background()
	var senderID, templateID, campaignID string
	// A distinct header per call: sender ids are unique per tenant, channel and
	// country, and the transition matrix seeds one campaign per cell.
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, $2, 'SMS', 'IN', 'approved') RETURNING id`,
		tenant.TenantID, fmt.Sprintf("HLT%03d", h.nextSenderSeq())).Scan(&senderID); err != nil {
		h.t.Fatalf("seed sender: %v", err)
	}
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO templates (tenant_id, sender_id, name, channel, country, body, status)
		VALUES ($1, $2, $3, 'SMS', 'IN', 'Hello', 'approved') RETURNING id`,
		tenant.TenantID, senderID,
		fmt.Sprintf("Halt fixture %d", h.senderSeq)).Scan(&templateID); err != nil {
		h.t.Fatalf("seed template: %v", err)
	}
	pausedAt, cancelledAt := "NULL", "NULL"
	if status == "paused" {
		pausedAt = "now()"
	}
	if status == "cancelled" {
		cancelledAt = "now()"
	}
	if err := h.admin.QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO campaigns (tenant_id, name, channel, country, sender_id, template_id,
		    status, recipients, segments_per_message, cost_minor_min, cost_minor_max,
		    currency, paused_at, cancelled_at)
		VALUES ($1, 'Halt fixture', 'SMS', 'IN', $2, $3, $4, 100, 1, 1200, 1200,
		        'INR', %s, %s) RETURNING id`, pausedAt, cancelledAt),
		tenant.TenantID, senderID, templateID, status).Scan(&campaignID); err != nil {
		h.t.Fatalf("seed campaign: %v", err)
	}
	return campaignID
}

type campaignBody struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	PausedAt    *string `json:"pausedAt"`
	CancelledAt *string `json:"cancelledAt"`
	Counts      struct {
		Queued    int `json:"queued"`
		Sent      int `json:"sent"`
		Delivered int `json:"delivered"`
		Failed    int `json:"failed"`
		Read      int `json:"read"`
		Cancelled int `json:"cancelled"`
	} `json:"counts"`
}

// The whole transition matrix in one table, because the rules are only worth
// anything if every cell holds. Legal cells answer 200; everything else 409.
func TestTheCampaignHaltTransitionMatrix(t *testing.T) {
	legal := map[string]map[string]bool{
		"pause":  {"sending": true, "queued": true, "scheduled": true},
		"resume": {"paused": true},
		"cancel": {"sending": true, "queued": true, "scheduled": true, "paused": true},
	}
	statuses := []string{"scheduled", "queued", "sending", "paused", "sent", "failed", "cancelled"}

	h := newSendHarness(t)
	acct := h.newAccount("owner")

	for _, action := range []string{"pause", "resume", "cancel"} {
		for _, status := range statuses {
			campaign := h.seedCampaign(acct, status)
			response := h.do(http.MethodPost,
				fmt.Sprintf("/v1/campaigns/%s/%s", campaign, action), acct.Token, nil)

			want := http.StatusConflict
			if legal[action][status] {
				want = http.StatusOK
			}
			if response.Code != want {
				t.Errorf("%s from %q = %d, want %d\n%s",
					action, status, response.Code, want, response.Body)
			}
		}
	}
}

// 404 is checked BEFORE the transition, so a probe cannot tell "not yours" from
// "wrong state" — and an id that does not exist is a route that answers, not a
// route that is missing.
func TestHaltingACampaignThatIsNotYoursIs404BeforeAny409(t *testing.T) {
	h := newSendHarness(t)
	mine := h.newAccount("owner")
	theirs := h.newAccount("owner")

	// Terminal on purpose: if the transition were checked first this would be
	// a 409 and would leak that the campaign exists.
	theirCampaign := h.seedCampaign(theirs, "sent")

	for _, action := range []string{"pause", "resume", "cancel"} {
		response := h.do(http.MethodPost,
			fmt.Sprintf("/v1/campaigns/%s/%s", theirCampaign, action), mine.Token, nil)
		if response.Code != http.StatusNotFound {
			t.Errorf("%s on another tenant's campaign = %d, want 404\n%s",
				action, response.Code, response.Body)
		}
	}

	missing := "00000000-0000-0000-0000-000000000000"
	response := h.do(http.MethodPost, "/v1/campaigns/"+missing+"/pause", mine.Token, nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("pause on a missing campaign = %d, want 404 — an entity miss, "+
			"not a route miss\n%s", response.Code, response.Body)
	}
}

func TestPauseSetsPausedAtAndResumeClearsIt(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	campaign := h.seedCampaign(acct, "sending")

	paused := h.do(http.MethodPost, "/v1/campaigns/"+campaign+"/pause", acct.Token, nil)
	if paused.Code != http.StatusOK {
		t.Fatalf("pause = %d\n%s", paused.Code, paused.Body)
	}
	var body campaignBody
	paused.decode(t, &body)
	if body.Status != "paused" {
		t.Fatalf("status = %q, want paused", body.Status)
	}
	if body.PausedAt == nil {
		t.Fatal("pausedAt is null on a paused campaign")
	}

	resumed := h.do(http.MethodPost, "/v1/campaigns/"+campaign+"/resume", acct.Token, nil)
	if resumed.Code != http.StatusOK {
		t.Fatalf("resume = %d\n%s", resumed.Code, resumed.Body)
	}
	resumed.decode(t, &body)
	if body.Status != "sending" {
		t.Fatalf("status = %q after resume, want sending", body.Status)
	}
	if body.PausedAt != nil {
		t.Fatalf("pausedAt = %v after resume, want null — a stale pause instant "+
			"is how held time gets counted twice", *body.PausedAt)
	}
}

// Cancel while paused: both instants stand, and that is legal. The earlier one
// is the campaign's real stop time, which is why pausedAt is not cleared.
func TestCancellingAPausedCampaignKeepsBothInstants(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	campaign := h.seedCampaign(acct, "paused")

	cancelled := h.do(http.MethodPost, "/v1/campaigns/"+campaign+"/cancel", acct.Token, nil)
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel = %d\n%s", cancelled.Code, cancelled.Body)
	}
	var body campaignBody
	cancelled.decode(t, &body)
	if body.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", body.Status)
	}
	if body.CancelledAt == nil {
		t.Fatal("cancelledAt is null on a cancelled campaign")
	}
	if body.PausedAt == nil {
		t.Fatal("pausedAt was cleared by the cancel — the earlier instant is the " +
			"campaign's real stop time and it has just been thrown away")
	}
	if *body.PausedAt > *body.CancelledAt {
		t.Fatalf("pausedAt %s is after cancelledAt %s", *body.PausedAt, *body.CancelledAt)
	}
}

// Two halts arriving together must not both succeed at deciding the campaign's
// fate. Exactly one 200; the rest 409.
func TestConcurrentHaltsResolveToExactlyOneWinner(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	campaign := h.seedCampaign(acct, "sending")

	const attempts = 8
	codes := make([]int, attempts)
	var wait sync.WaitGroup
	for i := range attempts {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response := h.do(http.MethodPost,
				"/v1/campaigns/"+campaign+"/pause", acct.Token, nil)
			codes[i] = response.Code
		}()
	}
	wait.Wait()

	won := 0
	for i, code := range codes {
		switch code {
		case http.StatusOK:
			won++
		case http.StatusConflict:
		default:
			t.Fatalf("concurrent pause %d = %d, want 200 or 409", i, code)
		}
	}
	if won != 1 {
		t.Fatalf("%d of %d concurrent pauses succeeded, want exactly 1 — two "+
			"winners is two settlements of the same campaign", won, attempts)
	}
}

// The three fields the contract made required. Always emitted, null when unset:
// omitempty on any of them breaks the generated client.
func TestEveryCampaignCarriesTheHaltFieldsEvenWhenUnset(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	h.seedCampaign(acct, "queued")

	list := h.do(http.MethodGet, "/v1/campaigns", acct.Token, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d\n%s", list.Code, list.Body)
	}
	var campaigns []map[string]any
	list.decode(t, &campaigns)
	if len(campaigns) == 0 {
		t.Fatal("no campaigns came back")
	}
	for _, campaign := range campaigns {
		for _, key := range []string{"pausedAt", "cancelledAt"} {
			if _, present := campaign[key]; !present {
				t.Fatalf("%s is absent from a campaign — it is a required contract "+
					"key whose VALUE may be null, not an optional key", key)
			}
		}
		counts, _ := campaign["counts"].(map[string]any)
		if _, present := counts["cancelled"]; !present {
			t.Fatal("counts.cancelled is absent — the KPI tile renders 0, it does " +
				"not disappear")
		}
	}
}

// A cancelled campaign must not still report its undispatched recipients as
// queued. They were never dispatched, never charged, and the funnel renders
// queued and cancelled in the same track.
func TestACancelledCampaignReportsItsUndispatchedRecipientsAsCancelled(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	campaign := h.seedCampaign(acct, "sending") // seeded with recipients = 100

	cancelled := h.do(http.MethodPost, "/v1/campaigns/"+campaign+"/cancel", acct.Token, nil)
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel = %d\n%s", cancelled.Code, cancelled.Body)
	}
	var body campaignBody
	cancelled.decode(t, &body)

	if body.Counts.Queued != 0 {
		t.Fatalf("counts.queued = %d after a cancel, want 0", body.Counts.Queued)
	}
	if body.Counts.Cancelled != 100 {
		t.Fatalf("counts.cancelled = %d, want 100 — nothing was dispatched, so "+
			"every recipient is cancelled", body.Counts.Cancelled)
	}
}

// nextSenderSeq hands out a distinct number per harness, so seeded senders do
// not collide on the tenant/header/channel/country uniqueness.
func (h *harness) nextSenderSeq() int {
	h.senderSeq++
	return h.senderSeq
}

// The 409 message is customer-visible text. Built by appending "d" to the
// action it read "This campaign cannot be canceld", which shipped to production
// before anyone said it out loud.
func TestTheHaltConflictMessageIsSpelledCorrectly(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	// Terminal, so all three actions conflict.
	campaign := h.seedCampaign(acct, "sent")

	want := map[string]string{
		"pause":  "This campaign cannot be paused from its current state.",
		"resume": "This campaign cannot be resumed from its current state.",
		"cancel": "This campaign cannot be cancelled from its current state.",
	}
	for action, message := range want {
		response := h.do(http.MethodPost,
			fmt.Sprintf("/v1/campaigns/%s/%s", campaign, action), acct.Token, nil)
		if response.Code != http.StatusConflict {
			t.Fatalf("%s = %d, want 409\n%s", action, response.Code, response.Body)
		}
		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		response.decode(t, &body)
		if body.Error.Message != message {
			t.Errorf("%s message = %q, want %q", action, body.Error.Message, message)
		}
	}
}
