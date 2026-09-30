package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Acceptance S1-S12 of
// docs/HANDOFF_TO_BACKEND_2026-09-30-operator-tenant-senders-and-exact-sender-filter.md.
// These need the databases: run them with `make test`.

func (h *harness) seedNamedSender(tenant account, header, channel, status string) string {
	h.t.Helper()
	var id string
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, $2, $3, 'IN', $4) RETURNING id`,
		tenant.TenantID, header, channel, status).Scan(&id); err != nil {
		h.t.Fatalf("seed sender %s: %v", header, err)
	}
	return id
}

// seedCampaignFrom inserts a campaign sent from an existing sender, created at
// the given offset in hours ago so a test can make it the oldest.
func (h *harness) seedCampaignFrom(tenant account, senderID, name string, hoursAgo int) {
	h.t.Helper()
	ctx := context.Background()
	var templateID string
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO templates (tenant_id, sender_id, name, channel, country, body, status)
		VALUES ($1, $2, $3, 'SMS', 'IN', 'Hello', 'approved') RETURNING id`,
		tenant.TenantID, senderID, name+" template").Scan(&templateID); err != nil {
		h.t.Fatalf("seed template: %v", err)
	}
	if _, err := h.admin.Exec(ctx, `
		INSERT INTO campaigns (tenant_id, name, channel, country, sender_id, template_id,
		    status, recipients, segments_per_message_min, segments_per_message_max,
		    cost_minor_min, cost_minor_max, currency, created_at)
		VALUES ($1, $2, 'SMS', 'IN', $3, $4, 'sent', 1, 1, 1, 100, 100, 'INR',
		        now() - make_interval(hours => $5))`,
		tenant.TenantID, name, senderID, templateID, hoursAgo); err != nil {
		h.t.Fatalf("seed campaign: %v", err)
	}
}

type opSenderPage struct {
	Total     int `json:"total"`
	SenderIDs []struct {
		ID      string `json:"id"`
		Header  string `json:"header"`
		Channel string `json:"channel"`
		Status  string `json:"status"`
	} `json:"senderIds"`
}

type opCampaignPage struct {
	Total     int          `json:"total"`
	Campaigns []opCampaign `json:"campaigns"`
}

func TestOperatorListsEverySenderATenantHasRegistered(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acme := h.newAccount("owner")
	ops := h.operatorToken()

	smsID := h.seedNamedSender(acme, "ACMERT", "SMS", "approved")       // S3
	rcsID := h.seedNamedSender(acme, "ACMERT", "RCS", "approved")       // S3
	h.seedNamedSender(acme, "NEVERUSED", "SMS", "approved")             // S1
	h.seedNamedSender(acme, "REJECTED1", "SMS", "rejected")             // S4
	h.seedNamedSender(acme, "PENDING1", "SMS", "pending_review")        // S4
	oldestOnly := h.seedNamedSender(acme, "OLDONLY", "SMS", "approved") // S2
	h.seedCampaignFrom(acme, oldestOnly, "oldest", 1000)
	for i := 0; i < 250; i++ {
		h.seedCampaignFrom(acme, smsID, fmt.Sprintf("recent %d", i), i%900)
	}

	var page opSenderPage
	h.operatorGet(ops, "/v1/operator/tenants/"+acme.TenantID.String()+"/senders?limit=200", &page)
	if page.Total != 6 {
		t.Fatalf("total = %d, want 6", page.Total)
	}
	seen := map[string]string{}
	for _, s := range page.SenderIDs {
		seen[s.ID] = s.Header + "/" + s.Channel + "/" + s.Status
	}
	for id, want := range map[string]string{
		smsID:      "ACMERT/SMS/approved",
		rcsID:      "ACMERT/RCS/approved",
		oldestOnly: "OLDONLY/SMS/approved",
	} {
		if seen[id] != want {
			t.Errorf("sender %s = %q, want %q", id, seen[id], want)
		}
	}
	joined := strings.Join(mapValues(seen), " ")
	for _, want := range []string{"NEVERUSED", "REJECTED1/SMS/rejected", "PENDING1/SMS/pending_review"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %s", want, joined)
		}
	}

	var filtered opSenderPage
	h.operatorGet(ops, "/v1/operator/tenants/"+acme.TenantID.String()+"/senders?status=rejected", &filtered)
	if filtered.Total != 1 {
		t.Errorf("status=rejected total = %d, want 1", filtered.Total)
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestOperatorTenantSendersRefuseBadPagingUnknownTenantsAndTenantTokens(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acme := h.newAccount("owner")
	ops := h.operatorToken()
	base := "/v1/operator/tenants/" + acme.TenantID.String() + "/senders"

	for _, query := range []string{"?limit=0", "?limit=201", "?limit="} { // S5
		res := h.do(http.MethodGet, base+query, ops, nil)
		if res.Code != http.StatusUnprocessableEntity || !strings.Contains(strings.ToLower(string(res.Body)), "limit") {
			t.Errorf("GET %s%s = %d %s, want 422 naming limit", base, query, res.Code, res.Body)
		}
	}
	unknown := "/v1/operator/tenants/" + uuid.NewString() + "/senders" // S6
	if res := h.do(http.MethodGet, unknown, ops, nil); res.Code != http.StatusNotFound {
		t.Errorf("unknown tenant = %d, want 404", res.Code)
	}
	if res := h.do(http.MethodGet, base, acme.Token, nil); res.Code != http.StatusUnauthorized { // S7
		t.Errorf("tenant token = %d, want 401", res.Code)
	}
	if res := h.do(http.MethodGet, base, "", nil); res.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", res.Code)
	}
}

func TestOperatorCampaignsFilterByExactSenderID(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acme := h.newAccount("owner")
	ops := h.operatorToken()

	smsRT := h.seedNamedSender(acme, "ACMERT", "SMS", "approved")
	rcsRT := h.seedNamedSender(acme, "ACMERT", "RCS", "approved")
	short := h.seedNamedSender(acme, "ACME", "SMS", "approved")
	h.seedCampaignFrom(acme, smsRT, "from sms rt", 1)
	h.seedCampaignFrom(acme, rcsRT, "from rcs rt", 2)
	h.seedCampaignFrom(acme, short, "from acme", 3)
	base := "/v1/operator/campaigns?tenantId=" + acme.TenantID.String()

	names := func(query string) []string {
		var page opCampaignPage
		h.operatorGet(ops, base+query, &page)
		out := []string{}
		for _, c := range page.Campaigns {
			out = append(out, c.Name)
		}
		return out
	}
	if got := names("&senderId=" + smsRT); len(got) != 1 || got[0] != "from sms rt" { // S8
		t.Errorf("SMS ACMERT = %v", got)
	}
	if got := names("&senderId=" + short); len(got) != 1 || got[0] != "from acme" { // S9
		t.Errorf("ACME = %v", got)
	}
	if got := names("&senderId=" + smsRT + "&sender=ACME"); len(got) != 1 || got[0] != "from sms rt" { // S12
		t.Errorf("senderId AND sender = %v", got)
	}
	if got := names("&senderId=" + short + "&sender=ACMERT"); len(got) != 0 { // S12
		t.Errorf("disjoint senderId and sender = %v, want none", got)
	}
	var none opCampaignPage
	h.operatorGet(ops, base+"&senderId="+uuid.NewString(), &none) // S11
	if none.Total != 0 || none.Campaigns == nil || len(none.Campaigns) != 0 {
		t.Errorf("unknown senderId = %+v, want total 0 and an empty array", none)
	}
	res := h.do(http.MethodGet, base+"&senderId=not-a-uuid", ops, nil) // S10
	if res.Code != http.StatusUnprocessableEntity || !strings.Contains(string(res.Body), "senderId") {
		t.Errorf("malformed senderId = %d %s, want 422 naming senderId", res.Code, res.Body)
	}
}
