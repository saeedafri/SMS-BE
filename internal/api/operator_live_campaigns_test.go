package api_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"

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
	for name, status := range map[string]string{"running": "sending", "held": "queued"} {
		if _, err := h.admin.Exec(context.Background(),
			`UPDATE campaigns SET status = $1 WHERE tenant_id = $2 AND name = $3`,
			status, acme.TenantID, name); err != nil {
			t.Fatal(err)
		}
	}
	base := "/v1/operator/campaigns?tenantId=" + acme.TenantID.String()

	for status, want := range map[string]int{
		"queued,sending,paused": 2, "sending": 1, "sent, queued": 2, "": 3} {
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
