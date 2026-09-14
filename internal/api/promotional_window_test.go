package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

var ist = time.FixedZone("IST", 5*3600+1800)

// at is 1 October 2026 at hh:mm:ss IST, a fixed day in the future.
func at(hour, minute, second int) time.Time {
	return time.Date(2026, 10, 1, hour, minute, second, 0, ist)
}

func (h *harness) categorisedTemplate(tenant account, category string) (sender, template string) {
	h.t.Helper()
	ctx := context.Background()
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, $2, 'SMS', 'IN', 'approved') RETURNING id`,
		tenant.TenantID, fmt.Sprintf("PRM%03d", h.nextSenderSeq())).Scan(&sender); err != nil {
		h.t.Fatalf("seed sender: %v", err)
	}
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO templates (tenant_id, sender_id, name, channel, country, body, status,
		    external_id, dlt_category)
		VALUES ($1, $2, $3, 'SMS', 'IN', 'Sale ends soon', 'approved', '12070195011234567890', $4)
		RETURNING id`, tenant.TenantID, sender, fmt.Sprintf("Window %d", h.senderSeq), category).
		Scan(&template); err != nil {
		h.t.Fatalf("seed template: %v", err)
	}
	return sender, template
}

func (h *harness) createCampaign(tenant account, category string, scheduledAt *time.Time) response {
	h.t.Helper()
	sender, template := h.categorisedTemplate(tenant, category)
	body := map[string]any{"name": "Window " + category, "channel": "SMS", "country": "IN",
		"senderId": sender, "templateId": template}
	if scheduledAt != nil {
		body["scheduledAt"] = scheduledAt.UTC().Format(time.RFC3339)
	}
	return h.do(http.MethodPost, "/v1/campaigns", tenant.Token, body)
}

func heldUntilOf(t *testing.T, res response) (string, bool) {
	t.Helper()
	var raw map[string]json.RawMessage
	res.decode(t, &raw)
	value, present := raw["heldUntil"]
	if !present {
		return "", false
	}
	var held *string
	_ = json.Unmarshal(value, &held)
	if held == nil {
		return "null", true
	}
	parsed, err := time.Parse(time.RFC3339, *held)
	if err != nil {
		t.Fatalf("heldUntil %q is not RFC 3339", *held)
	}
	return parsed.UTC().Format(time.RFC3339), true
}

// Ask 41 §2.1. A campaign held by the window says when it will launch: a fact
// about its schedule, the same on every read.
func TestAHeldCampaignSaysWhenItWillLaunch(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	nextMorning := "2026-10-02T04:30:00Z"
	sameMorning := "2026-10-01T04:30:00Z"
	cases := []struct {
		name     string
		category string
		schedule time.Time
		want     string
	}{
		{"promotional at 23:00", "PROMOTIONAL", at(23, 0, 0), nextMorning},
		{"transactional at 23:00", "TRANSACTIONAL", at(23, 0, 0), "null"},
		{"promotional at 11:00", "PROMOTIONAL", at(11, 0, 0), "null"},
		{"promotional at exactly 21:00:00", "PROMOTIONAL", at(21, 0, 0), nextMorning},
		{"promotional at 09:59:59", "PROMOTIONAL", at(9, 59, 59), sameMorning},
		{"promotional at 10:00:00", "PROMOTIONAL", at(10, 0, 0), "null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := h.createCampaign(tenant, tc.category, &tc.schedule)
			if res.Code != http.StatusCreated {
				t.Fatalf("create = %d\n%s", res.Code, res.Body)
			}
			held, present := heldUntilOf(t, res)
			if !present {
				t.Fatal("no heldUntil field")
			}
			if held != tc.want {
				t.Errorf("heldUntil = %s, want %s", held, tc.want)
			}
		})
	}
}

// Ask 41 §2.3. "Send now" outside the window would be refused message by
// message; it is refused whole, before anything is created or held.
func TestSendNowOutsideTheWindowIsRefused(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)

	h.server.Now = func() time.Time { return at(22, 0, 0) }
	var before int
	_ = h.admin.QueryRow(context.Background(), `SELECT count(*) FROM campaigns WHERE tenant_id = $1`,
		tenant.TenantID).Scan(&before)
	refused := h.createCampaign(tenant, "PROMOTIONAL", nil)
	var body struct {
		Error struct {
			Code, Message string
		} `json:"error"`
	}
	refused.decode(t, &body)
	if refused.Code != http.StatusUnprocessableEntity || body.Error.Code != "outside_promotional_window" ||
		!strings.Contains(body.Error.Message, "10:00") {
		t.Fatalf("send-now promotional at 22:00 = %d %s, want 422 outside_promotional_window naming 10:00",
			refused.Code, refused.Body)
	}
	var after int
	_ = h.admin.QueryRow(context.Background(), `SELECT count(*) FROM campaigns WHERE tenant_id = $1`,
		tenant.TenantID).Scan(&after)
	if after != before {
		t.Errorf("%d campaign rows created by a refused send-now", after-before)
	}

	if res := h.createCampaign(tenant, "TRANSACTIONAL", nil); res.Code != http.StatusCreated {
		t.Errorf("send-now transactional at 22:00 = %d, want 201\n%s", res.Code, res.Body)
	}
	h.server.Now = func() time.Time { return at(12, 0, 0) }
	if res := h.createCampaign(tenant, "PROMOTIONAL", nil); res.Code != http.StatusCreated {
		t.Errorf("send-now promotional at 12:00 = %d, want 201\n%s", res.Code, res.Body)
	}
}

// Ask 41 §2.4. Resuming a paused promotional campaign outside the window is
// refused, and it stays paused.
func TestResumeOutsideTheWindowIsRefused(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	campaign := h.seedCampaign(tenant, "paused")
	if _, err := h.admin.Exec(context.Background(), `
		UPDATE templates SET dlt_category = 'PROMOTIONAL', external_id = '12070195011234567890'
		 WHERE id = (SELECT template_id FROM campaigns WHERE id = $1)`, campaign); err != nil {
		t.Fatal(err)
	}
	h.server.Now = func() time.Time { return at(22, 0, 0) }
	res := h.do(http.MethodPost, "/v1/campaigns/"+campaign+"/resume", tenant.Token, nil)
	if res.Code != http.StatusUnprocessableEntity || !strings.Contains(string(res.Body), "outside_promotional_window") {
		t.Fatalf("resume at 22:00 = %d %s, want 422 outside_promotional_window", res.Code, res.Body)
	}
	var status string
	_ = h.admin.QueryRow(context.Background(), `SELECT status FROM campaigns WHERE id = $1`, campaign).Scan(&status)
	if status != "paused" {
		t.Errorf("campaign is %q after a refused resume, want paused", status)
	}
	if n := h.campaignMessages(tenant, campaign); n != 0 {
		t.Errorf("%d messages dispatched by a refused resume", n)
	}
}

func (h *harness) campaignMessages(tenant account, campaign string) int {
	h.t.Helper()
	conn, err := h.server.ClickHouse.Conn(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	var n uint64
	_ = conn.QueryRow(context.Background(),
		`SELECT count() FROM messages WHERE tenant_id = ? AND campaign_id = toUUID(?)`,
		tenant.TenantID, campaign).Scan(&n)
	return int(n)
}

// Ask 41 §2.2. Held campaigns are not due, so they are never selected and cannot
// fill the scheduler's page ahead of a campaign that is.
func TestHeldCampaignsDoNotBlockTheQueue(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	ctx := context.Background()
	held := h.seedNamedCampaign(tenant, "Held", "scheduled")
	if _, err := h.admin.Exec(ctx, `
		UPDATE templates SET dlt_category = 'PROMOTIONAL', external_id = '12070195011234567890'
		 WHERE id = (SELECT template_id FROM campaigns WHERE id = $1)`, held); err != nil {
		t.Fatal(err)
	}
	if _, err := h.admin.Exec(ctx, `
		UPDATE campaigns SET scheduled_at = $2, held_until = $3 WHERE id = $1`,
		held, at(22, 0, 0), time.Date(2026, 10, 2, 10, 0, 0, 0, ist)); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if _, err := h.admin.Exec(ctx, `
		INSERT INTO campaigns (tenant_id, name, channel, country, sender_id, template_id, status,
		    scheduled_at, held_until, recipients, segments_per_message_min, segments_per_message_max,
		    cost_minor_min, cost_minor_max, currency)
		SELECT tenant_id, 'Held ' || n, channel, country, sender_id, template_id, 'scheduled',
		       scheduled_at, held_until, 0, 1, 1, 0, 0, 'INR'
		  FROM campaigns, generate_series(1, 149) AS n WHERE id = $1`, held); err != nil {
		t.Fatalf("seed held campaigns: %v", err)
	}
	transactional := h.seedNamedCampaign(tenant, "Transactional", "scheduled")
	if _, err := h.admin.Exec(ctx, `UPDATE campaigns SET scheduled_at = $2 WHERE id = $1`,
		transactional, at(22, 1, 0)); err != nil {
		t.Fatal(err)
	}

	h.server.Now = func() time.Time { return at(22, 2, 0) }
	var status string
	for tick := 0; tick < 3; tick++ {
		_ = h.server.LaunchDueCampaigns(ctx)
		_ = h.admin.QueryRow(ctx, `SELECT status FROM campaigns WHERE id = $1`, transactional).Scan(&status)
		if status != "scheduled" {
			break
		}
	}
	if status == "scheduled" || status == "queued" {
		t.Fatalf("the transactional campaign due at 22:01 is still %q behind 150 held ones", status)
	}
}
