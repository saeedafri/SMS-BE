package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The operator console's live campaign view: a tenant search box, the
// campaign list narrowed to what is still sending, and one campaign's overview.
// These need the databases: run them with `make test`.

func TestOperatorCampaignOverviewShowsProgressDeliveryAndWhyMessagesFailed(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acme := h.newAccount("owner")
	h.fundWallet(acme)
	ops := h.operatorToken()

	delivered, absent, refused := "+919877600010", "+919877600001", "+919877600000"
	campaignID := h.launchCampaign(acme, ops, "Live overview", []string{delivered, absent, refused})

	// Mid-send: the carrier refused one at submit, the other two await receipts.
	var midSend struct {
		Progress struct {
			Created, InFlight, Settled int
			Percent                    *float64
		} `json:"progress"`
	}
	h.operatorGet(ops, "/v1/operator/campaigns/"+campaignID, &midSend)
	if p := midSend.Progress; p.Created != 3 || p.InFlight != 2 || p.Settled != 1 ||
		p.Percent == nil || *p.Percent != 33.33 {
		t.Errorf("mid-send progress = %+v (percent %v), want 3 created, 2 in flight, 1 settled, 33.33%%",
			p, p.Percent)
	}
	h.drainSandbox()

	var overview struct {
		opCampaign
		Progress struct {
			Expected, Created, InFlight, Settled int
			Percent                              *float64
		} `json:"progress"`
		DeliveryRate *float64 `json:"deliveryRate"`
		ByChannel    []struct {
			Channel   string `json:"channel"`
			Messages  int    `json:"messages"`
			Delivered int    `json:"delivered"`
		} `json:"byChannel"`
		FailureReasons []struct {
			Status    string  `json:"status"`
			ErrorCode *string `json:"errorCode"`
			Messages  int     `json:"messages"`
		} `json:"failureReasons"`
		LastActivityAt *string `json:"lastActivityAt"`
	}
	h.operatorGet(ops, "/v1/operator/campaigns/"+campaignID, &overview)

	if overview.ID != campaignID || overview.Name != "Live overview" ||
		overview.TenantID != acme.TenantID.String() || overview.TenantName == "" {
		t.Errorf("campaign = %+v", overview.opCampaign)
	}
	if m := overview.Messages; m.Total != 3 || m.Delivered != 1 || m.Failed != 2 {
		t.Errorf("messages = %+v, want 3 total, 1 delivered, 2 failed", m)
	}
	p := overview.Progress
	if p.Expected != 3 || p.Created != 3 || p.Settled != 3 || p.InFlight != 0 ||
		p.Percent == nil || *p.Percent != 100 {
		t.Errorf("progress = %+v (percent %v), want all 3 settled, 100%%", p, p.Percent)
	}
	if overview.DeliveryRate == nil || *overview.DeliveryRate != 33.33 {
		t.Errorf("deliveryRate = %v, want 33.33", overview.DeliveryRate)
	}
	if len(overview.ByChannel) != 1 || overview.ByChannel[0].Channel != "SMS" ||
		overview.ByChannel[0].Messages != 3 || overview.ByChannel[0].Delivered != 1 {
		t.Errorf("byChannel = %+v", overview.ByChannel)
	}
	failed := 0
	for _, reason := range overview.FailureReasons {
		if reason.Status != "failed" || reason.ErrorCode == nil || *reason.ErrorCode == "" {
			t.Errorf("failure reason %+v: want a failed status with the carrier's code", reason)
		}
		failed += reason.Messages
	}
	if failed != 2 {
		t.Errorf("failure reasons cover %d messages, want 2: %+v", failed, overview.FailureReasons)
	}
	if overview.LastActivityAt == nil {
		t.Error("lastActivityAt is null for a campaign that sent")
	}

	for _, path := range []string{"/v1/operator/campaigns/" + uuid.NewString(),
		"/v1/operator/campaigns/not-a-uuid"} {
		if res := h.do(http.MethodGet, path, ops, nil); res.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, res.Code)
		}
	}
	if res := h.do(http.MethodGet, "/v1/operator/campaigns/"+campaignID, acme.Token, nil); res.Code != http.StatusUnauthorized {
		t.Errorf("tenant token = %d, want 401", res.Code)
	}
}

func TestOperatorCampaignOverviewOfACampaignThatHasNotSentYet(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acme := h.newAccount("owner")
	ops := h.operatorToken()
	sender := h.seedNamedSender(acme, "LIVEQ", "SMS", "approved")
	h.seedCampaignFrom(acme, sender, "not yet", 0)

	var page opCampaignPage
	h.operatorGet(ops, "/v1/operator/campaigns?tenantId="+acme.TenantID.String(), &page)
	if len(page.Campaigns) != 1 {
		t.Fatalf("seeded campaign not listed: %+v", page)
	}
	var overview struct {
		Progress struct {
			Expected, Created int
			Percent           *float64
		} `json:"progress"`
		DeliveryRate   *float64 `json:"deliveryRate"`
		ByChannel      []any    `json:"byChannel"`
		FailureReasons []any    `json:"failureReasons"`
		LastActivityAt *string  `json:"lastActivityAt"`
	}
	h.operatorGet(ops, "/v1/operator/campaigns/"+page.Campaigns[0].ID, &overview)
	if overview.Progress.Expected != 1 || overview.Progress.Created != 0 ||
		overview.Progress.Percent == nil || *overview.Progress.Percent != 0 {
		t.Errorf("progress = %+v, want 1 expected, none created, 0%%", overview.Progress)
	}
	if overview.DeliveryRate != nil || overview.LastActivityAt != nil {
		t.Errorf("deliveryRate %v lastActivityAt %v, want both null before any send",
			overview.DeliveryRate, overview.LastActivityAt)
	}
	if overview.ByChannel == nil || overview.FailureReasons == nil {
		t.Error("byChannel and failureReasons must be [] rather than null")
	}
}

func TestOperatorFiltersCampaignsBySeveralStatusesAtOnce(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acme := h.newAccount("owner")
	ops := h.operatorToken()
	sender := h.seedNamedSender(acme, "LIVEST", "SMS", "approved")
	h.seedCampaignFrom(acme, sender, "done", 2)
	h.seedCampaignFrom(acme, sender, "running", 1)
	h.seedCampaignFrom(acme, sender, "held", 0)
	h.seedCampaignFrom(acme, sender, "later", 0)
	h.seedCampaignFrom(acme, sender, "broke", 3)
	h.seedCampaignFrom(acme, sender, "stopped", 4)
	for name, set := range map[string]string{
		"running": "status = 'sending'",
		"held":    "status = 'queued'",
		"later":   "status = 'scheduled', scheduled_at = now() + interval '1 hour'",
		"broke":   "status = 'failed'",
		"stopped": "status = 'cancelled', cancelled_at = now()",
	} {
		if _, err := h.admin.Exec(context.Background(),
			`UPDATE campaigns SET `+set+` WHERE tenant_id = $1 AND name = $2`,
			acme.TenantID, name); err != nil {
			t.Fatal(err)
		}
	}
	base := "/v1/operator/campaigns?tenantId=" + acme.TenantID.String()

	// The handoff's tabs: Live, Finished, All.
	for status, want := range map[string]int{
		"scheduled,queued,sending,paused": 3, "sent,failed,cancelled": 3, "": 6,
		"sending": 1, "sent, queued": 2} {
		var page opCampaignPage
		h.operatorGet(ops, base+"&status="+url.QueryEscape(status), &page)
		if page.Total != want {
			t.Errorf("status=%q total = %d, want %d", status, page.Total, want)
		}
	}
	if res := h.do(http.MethodGet, base+"&status=sending,bogus", ops, nil); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("status=sending,bogus = %d, want 422", res.Code)
	}
}

type opTenantSuggestions struct {
	Tenants []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Country string `json:"country"`
		Status  string `json:"status"`
	} `json:"tenants"`
}

func TestOperatorTenantSearchSuggestsAsTheOperatorTypes(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	alpha, beta := h.newAccount("owner"), h.newAccount("owner")
	ops := h.operatorToken()
	marker := "zq" + uuid.NewString()[:8]
	for tenant, name := range map[uuid.UUID]string{
		alpha.TenantID: marker + " Zulu", beta.TenantID: "Beta " + marker} {
		if _, err := h.admin.Exec(context.Background(),
			`UPDATE tenants SET name = $1 WHERE id = $2`, name, tenant); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.admin.Exec(context.Background(),
		`UPDATE tenants SET status = 'suspended' WHERE id = $1`, beta.TenantID); err != nil {
		t.Fatal(err)
	}
	suggest := func(query string) opTenantSuggestions {
		var out opTenantSuggestions
		h.operatorGet(ops, "/v1/operator/tenants/suggest?"+query, &out)
		return out
	}

	// Part of the name; the one starting with it comes first, though
	// alphabetically it is last.
	got := suggest("q=" + url.QueryEscape(marker))
	if len(got.Tenants) != 2 || got.Tenants[0].ID != alpha.TenantID.String() ||
		got.Tenants[1].ID != beta.TenantID.String() {
		t.Fatalf("suggestions = %+v, want alpha (prefix) then beta", got.Tenants)
	}
	if got.Tenants[1].Status != "suspended" || got.Tenants[0].Country != "IN" {
		t.Errorf("suggestion fields = %+v", got.Tenants)
	}
	if got := suggest("q=" + url.QueryEscape("BETA "+marker)); len(got.Tenants) != 1 ||
		got.Tenants[0].ID != beta.TenantID.String() {
		t.Errorf("case-insensitive search = %+v, want beta only", got.Tenants)
	}
	if got := suggest("q=" + beta.TenantID.String()); len(got.Tenants) != 1 ||
		got.Tenants[0].Name != "Beta "+marker {
		t.Errorf("search by id = %+v, want beta", got.Tenants)
	}
	// A typed % or _ is a character to find, not a wildcard.
	if got := suggest("q=" + url.QueryEscape("%"+marker)); len(got.Tenants) != 0 {
		t.Errorf("%%-prefixed search = %+v, want nothing", got.Tenants)
	}
	if got := suggest("q=" + url.QueryEscape(marker[:4]+"_"+marker[5:])); len(got.Tenants) != 0 {
		t.Errorf("_ matched as a wildcard: %+v", got.Tenants)
	}
	if got := suggest("limit=1"); len(got.Tenants) != 1 {
		t.Errorf("limit=1 returned %d", len(got.Tenants))
	}

	for _, query := range []string{"limit=0", "limit=51", "limit=x"} {
		if res := h.do(http.MethodGet, "/v1/operator/tenants/suggest?"+query, ops, nil); res.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d, want 422", query, res.Code)
		}
	}
	if res := h.do(http.MethodGet, "/v1/operator/tenants/suggest?q=a", alpha.Token, nil); res.Code != http.StatusUnauthorized {
		t.Errorf("tenant token = %d, want 401", res.Code)
	}
}

func keysOf(t *testing.T, what string, value any) string {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want an object", what, value)
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func sortedList(keys ...string) string {
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// Every field the handoff documents is there, and nothing it does not.
func TestOperatorLiveCampaignResponsesMatchTheHandoff(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acme := h.newAccount("owner")
	h.fundWallet(acme)
	ops := h.operatorToken()
	campaignID := h.launchCampaign(acme, ops, "Shape check",
		[]string{"+919877700010", "+919877700001", "+919877700000"})
	h.drainSandbox()

	row := []string{"id", "name", "tenantId", "tenantName", "channel", "fallbackChannel",
		"country", "status", "sender", "template", "listName", "recipients", "withheld",
		"estimatedCostMinorMin", "estimatedCostMinorMax", "currency", "retryOf", "createdBy",
		"createdAt", "scheduledAt", "sendStartedAt", "pausedAt", "cancelledAt", "messages"}
	counts := sortedList("total", "queued", "sent", "delivered", "read", "failed",
		"rejected", "costMinor")

	var list map[string]any
	h.operatorGet(ops, "/v1/operator/campaigns?campaignId="+campaignID, &list)
	listed := list["campaigns"].([]any)[0]
	if got := keysOf(t, "list row", listed); got != sortedList(row...) {
		t.Errorf("list row keys = %s", got)
	}

	var overview map[string]any
	h.operatorGet(ops, "/v1/operator/campaigns/"+campaignID, &overview)
	want := sortedList(append(row, "progress", "deliveryRate", "byChannel",
		"failureReasons", "lastActivityAt")...)
	if got := keysOf(t, "overview", overview); got != want {
		t.Errorf("overview keys = %s\nwant          %s", got, want)
	}
	if got := keysOf(t, "messages", overview["messages"]); got != counts {
		t.Errorf("messages keys = %s", got)
	}
	if got := keysOf(t, "progress", overview["progress"]); got !=
		sortedList("expected", "created", "inFlight", "settled", "percent") {
		t.Errorf("progress keys = %s", got)
	}
	channels := overview["byChannel"].([]any)
	if len(channels) != 1 {
		t.Fatalf("byChannel = %v", channels)
	}
	if got := keysOf(t, "byChannel[0]", channels[0]); got != sortedList("channel", "messages",
		"queued", "sent", "delivered", "read", "failed", "rejected", "segments",
		"costMinorByCurrency", "deliveryRate") {
		t.Errorf("byChannel keys = %s", got)
	}
	channel := channels[0].(map[string]any)
	if channel["segments"] != float64(3) {
		t.Errorf("byChannel segments = %v, want 3", channel["segments"])
	}
	if _, ok := channel["costMinorByCurrency"].(map[string]any)["INR"]; !ok {
		t.Errorf("costMinorByCurrency = %v, want an INR entry", channel["costMinorByCurrency"])
	}
	reasons := overview["failureReasons"].([]any)
	if len(reasons) == 0 {
		t.Fatal("failureReasons is empty for a campaign with two failures")
	}
	if got := keysOf(t, "failureReasons[0]", reasons[0]); got !=
		sortedList("status", "errorCode", "messages") {
		t.Errorf("failureReasons keys = %s", got)
	}

	var suggestions map[string]any
	h.operatorGet(ops, "/v1/operator/tenants/suggest?q="+acme.TenantID.String(), &suggestions)
	tenants := suggestions["tenants"].([]any)
	if len(tenants) != 1 {
		t.Fatalf("suggestions = %v", tenants)
	}
	if got := keysOf(t, "tenant suggestion", tenants[0]); got !=
		sortedList("id", "name", "country", "status") {
		t.Errorf("suggestion keys = %s", got)
	}
}

// Ten by default, A to Z; throttled shows as throttled; q is bounded.
func TestOperatorTenantSearchDefaultsAndBounds(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	ops := h.operatorToken()
	marker := "zr" + uuid.NewString()[:8]
	// Created in reverse, so creation order cannot pass for alphabetical.
	var throttled uuid.UUID
	for i := 10; i >= 0; i-- {
		tenant := h.newAccount("owner").TenantID
		if _, err := h.admin.Exec(context.Background(),
			`UPDATE tenants SET name = $1 WHERE id = $2`,
			fmt.Sprintf("%s %02d", marker, i), tenant); err != nil {
			t.Fatal(err)
		}
		if i == 3 {
			throttled = tenant
		}
	}
	if _, err := h.admin.Exec(context.Background(),
		`UPDATE tenants SET throttled_at = now(), throttled_rate_per_second = 1 WHERE id = $1`,
		throttled); err != nil {
		t.Fatal(err)
	}

	var got opTenantSuggestions
	h.operatorGet(ops, "/v1/operator/tenants/suggest?q="+marker, &got)
	if len(got.Tenants) != 10 {
		t.Fatalf("default limit returned %d, want 10", len(got.Tenants))
	}
	for i, tenant := range got.Tenants {
		if want := fmt.Sprintf("%s %02d", marker, i); tenant.Name != want {
			t.Errorf("suggestion %d = %q, want %q", i, tenant.Name, want)
		}
		if want := map[bool]string{true: "throttled", false: "active"}[tenant.ID ==
			throttled.String()]; tenant.Status != want {
			t.Errorf("%s status = %q, want %q", tenant.Name, tenant.Status, want)
		}
	}

	var everyone opTenantSuggestions
	h.operatorGet(ops, "/v1/operator/tenants/suggest", &everyone)
	if len(everyone.Tenants) != 10 {
		t.Errorf("empty q returned %d, want 10", len(everyone.Tenants))
	}
	h.operatorGet(ops, "/v1/operator/tenants/suggest?q="+strings.Repeat("a", 100), &everyone)
	if res := h.do(http.MethodGet, "/v1/operator/tenants/suggest?q="+strings.Repeat("a", 101),
		ops, nil); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("q of 101 chars = %d, want 422", res.Code)
	}
}

// The failure table stops at the twenty most common reasons.
func TestOperatorCampaignOverviewListsTheTwentyCommonestFailureReasons(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acme := h.newAccount("owner")
	ops := h.operatorToken()
	sender := h.seedNamedSender(acme, "LIVEFR", "SMS", "approved")
	h.seedCampaignFrom(acme, sender, "many failures", 0)
	var page opCampaignPage
	h.operatorGet(ops, "/v1/operator/campaigns?tenantId="+acme.TenantID.String(), &page)
	campaignID := uuid.MustParse(page.Campaigns[0].ID)

	ctx := context.Background()
	conn, err := h.server.ClickHouse.Conn(ctx)
	if err != nil {
		t.Fatalf("clickhouse: %v", err)
	}
	batch, err := conn.PrepareBatch(ctx, `INSERT INTO messages (
		tenant_id, id, campaign_id, channel, country, sender_header, msisdn, status,
		error_code, fraud_flag, segments, cost_minor, currency,
		created_at, updated_at, version)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	now := time.Now().UTC()
	add := func(state, code string, n int) {
		for i := 0; i < n; i++ {
			if err := batch.Append(acme.TenantID, uuid.New(), campaignID, "SMS", "IN",
				"LIVEFR", fmt.Sprintf("+9198766%05d", i), state, code,
				"none", uint8(1), int64(0), "INR", now, now, uint64(1)); err != nil {
				t.Fatalf("append: %v", err)
			}
		}
	}
	add("undelivered", "UNDELIV:001", 5)
	add("rejected", "insufficient_balance", 4)
	for i := 0; i < 20; i++ {
		add("undelivered", fmt.Sprintf("UNDELIV:1%02d", i), 1)
	}
	if err := batch.Send(); err != nil {
		t.Fatalf("send: %v", err)
	}

	var overview struct {
		FailureReasons []struct {
			Status    string  `json:"status"`
			ErrorCode *string `json:"errorCode"`
			Messages  int     `json:"messages"`
		} `json:"failureReasons"`
		Messages opCounts `json:"messages"`
	}
	h.operatorGet(ops, "/v1/operator/campaigns/"+campaignID.String(), &overview)
	reasons := overview.FailureReasons
	if len(reasons) != 20 {
		t.Fatalf("failureReasons has %d, want 20", len(reasons))
	}
	first, second := reasons[0], reasons[1]
	if first.Status != "failed" || first.ErrorCode == nil || *first.ErrorCode != "UNDELIV:001" || first.Messages != 5 {
		t.Errorf("first reason = %+v, want UNDELIV:001 x5", first)
	}
	if second.Status != "rejected" || second.ErrorCode == nil ||
		*second.ErrorCode != "insufficient_balance" || second.Messages != 4 {
		t.Errorf("second reason = %+v, want rejected insufficient_balance x4", second)
	}
	if m := overview.Messages; m.Failed != 25 || m.Rejected != 4 {
		t.Errorf("messages = %+v, want 25 failed, 4 rejected", m)
	}
}

