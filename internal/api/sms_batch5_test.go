package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
)

// linkTemplate is an approved Indian SMS sender and a transactional template
// carrying this body.
func (h *harness) linkTemplate(tenant account, body string) (sender, template string) {
	h.t.Helper()
	ctx := context.Background()
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, $2, 'SMS', 'IN', 'approved') RETURNING id`,
		tenant.TenantID, fmt.Sprintf("LNK%03d", h.nextSenderSeq())).Scan(&sender); err != nil {
		h.t.Fatalf("seed sender: %v", err)
	}
	h.registerEntity(tenant.TenantID, "IN")
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO templates (tenant_id, sender_id, name, channel, country, body, status,
		    external_id, dlt_category)
		VALUES ($1, $2, $3, 'SMS', 'IN', $4, 'approved', '12070195011234567890', 'TRANSACTIONAL')
		RETURNING id`, tenant.TenantID, sender, fmt.Sprintf("Link %d", h.nextSenderSeq()), body).
		Scan(&template); err != nil {
		h.t.Fatalf("seed template: %v", err)
	}
	return sender, template
}

func (h *harness) approveLink(tenant account, host, status string) {
	h.t.Helper()
	if _, err := h.admin.Exec(context.Background(), `
		INSERT INTO registrations (tenant_id, country, object_key, status, fields)
		VALUES ($1, 'IN', 'cta_whitelist', $2, jsonb_build_object('type', 'url', 'value', $3::text))`,
		tenant.TenantID, status, host); err != nil {
		h.t.Fatalf("list %s: %v", host, err)
	}
}

func (h *harness) campaignOn(tenant account, sender, template string, extra map[string]any) response {
	h.t.Helper()
	body := map[string]any{"name": "Batch 5", "channel": "SMS", "country": "IN",
		"senderId": sender, "templateId": template}
	for k, v := range extra {
		body[k] = v
	}
	return h.do(http.MethodPost, "/v1/campaigns", tenant.Token, body)
}

// Batch 5 rows S1 to S7: a link the operator would scrub is refused before
// anything is created, naming its host.
func TestOnlyApprovedLinksMaySendInIndia(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	h.server.ApprovedLinkCountries = map[string]bool{"IN": true}
	h.server.LinkBaseURL = "https://go.relay.test"
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	refused := func(name, body, host string) {
		t.Helper()
		sender, template := h.linkTemplate(tenant, body)
		res := h.campaignOn(tenant, sender, template, nil)
		if res.Code != http.StatusUnprocessableEntity || res.errorCode(t) != "url_not_whitelisted" ||
			!strings.Contains(string(res.Body), host) {
			t.Errorf("%s: %d %s, want 422 url_not_whitelisted naming %s", name, res.Code, res.Body, host)
		}
	}
	allowed := func(name, body string, extra map[string]any) {
		t.Helper()
		sender, template := h.linkTemplate(tenant, body)
		if res := h.campaignOn(tenant, sender, template, extra); res.Code != http.StatusCreated {
			t.Errorf("%s: %d %s, want 201", name, res.Code, res.Body)
		}
	}

	refused("S1 unlisted host", "Offer at https://acme.example/sale", "acme.example")
	h.approveLink(tenant, "pending.example", "pending_review")
	refused("S3 pending entry", "Offer at https://pending.example/sale", "pending.example")
	h.approveLink(tenant, "acme.example", "approved")
	allowed("S2 approved host", "Offer at https://acme.example/sale", nil)
	refused("S4 subdomain", "Offer at https://deals.acme.example/x", "deals.acme.example")
	allowed("S5 www.", "Offer at https://www.acme.example/x", nil)
	h.approveLink(tenant, "bit.ly", "approved")
	refused("S6 listed shortener", "Offer at https://bit.ly/abc", "bit.ly")
	refused("S7 {{link}} host unlisted", "Tap {{link}} now", "go.relay.test")
	h.approveLink(tenant, "go.relay.test", "approved")
	allowed("S7 {{link}} host listed", "Tap {{link}} now",
		map[string]any{"link": map[string]any{"destination": "https://acme.example/x"}})
	allowed("no link needs no list", "Your order has shipped", nil)

	// The send path refuses too, at no cost.
	sender, template := h.linkTemplate(tenant, "{{message}}")
	before := h.walletBalance(tenant)
	var sent struct {
		Status    string  `json:"status"`
		CostMinor int64   `json:"costMinor"`
		ErrorCode *string `json:"errorCode"`
	}
	h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{"senderId": sender,
		"templateId": template, "to": "9876543210", "body": "See https://evil.example/x"}).decode(t, &sent)
	if sent.Status != "rejected" || sent.ErrorCode == nil || *sent.ErrorCode != "url_not_whitelisted" ||
		sent.CostMinor != 0 || h.walletBalance(tenant) != before {
		t.Errorf("API send with an unlisted link = %+v, want rejected url_not_whitelisted at cost 0", sent)
	}
}

// The approved-links list is many entries, each host once.
func TestTheApprovedLinksListHoldsManyHostsOnceEach(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tenant := h.newAccount("owner")
	h.registerEntity(tenant.TenantID, "IN")
	add := func(kind, value string) int {
		return h.do(http.MethodPost, "/v1/registrations", tenant.Token, map[string]any{
			"country": "IN", "objectKey": "cta_whitelist",
			"fields": map[string]any{"type": kind, "value": value}}).Code
	}
	if a, b := add("url", "one.example"), add("url", "two.example"); a != http.StatusCreated || b != http.StatusCreated {
		t.Fatalf("two hosts = %d, %d, want 201 each", a, b)
	}
	if code := add("url", "ONE.example"); code != http.StatusConflict {
		t.Errorf("the same host again = %d, want 409", code)
	}
	if code := add("fax", "three.example"); code != http.StatusUnprocessableEntity {
		t.Errorf("an unknown kind = %d, want 422", code)
	}
	if code := add("url", "has space.example"); code != http.StatusUnprocessableEntity {
		t.Errorf("a value with a space = %d, want 422", code)
	}
}

// Batch 5 rows S8 to S11: quiet hours are the hours of silence.
func TestQuietHoursHoldALaunchAndRefuseAResume(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	night := map[string]any{"from": "22:00", "to": "06:00", "zone": "UTC"}
	insideAt := time.Date(2026, 11, 23, 23, 0, 0, 0, time.UTC)
	h.server.Now = func() time.Time { return insideAt }
	sender, template := h.linkTemplate(tenant, "Your order has shipped")

	held := h.campaignOn(tenant, sender, template, map[string]any{"quietHours": night})
	var campaign struct {
		ID        string         `json:"id"`
		Status    string         `json:"status"`
		HeldUntil *string        `json:"heldUntil"`
		Counts    map[string]int `json:"counts"`
		Quiet     map[string]any `json:"quietHours"`
	}
	held.decode(t, &campaign)
	if held.Code != http.StatusCreated || campaign.Status != "scheduled" || campaign.HeldUntil == nil ||
		!strings.HasPrefix(*campaign.HeldUntil, "2026-11-24T06:00:00") || campaign.Quiet["zone"] != "UTC" {
		t.Errorf("S8 inside the window = %d %s", held.Code, held.Body)
	}
	for state, count := range campaign.Counts {
		if count != 0 {
			t.Errorf("S8 held campaign counts %s = %d, want 0", state, count)
		}
	}

	h.server.Now = func() time.Time { return insideAt.Add(-12 * time.Hour) }
	open := h.campaignOn(tenant, sender, template, map[string]any{"quietHours": night})
	campaign.HeldUntil = nil
	open.decode(t, &campaign)
	if open.Code != http.StatusCreated || campaign.HeldUntil != nil || campaign.Status == "scheduled" {
		t.Errorf("S9 outside the window = %d %s, want not held", open.Code, open.Body)
	}

	for name, quiet := range map[string]any{
		"equal times":    map[string]any{"from": "22:00", "to": "22:00", "zone": "UTC"},
		"bad zone":       map[string]any{"from": "22:00", "to": "06:00", "zone": "Mars/Base"},
		"undeclared key": map[string]any{"from": "22:00", "to": "06:00", "zone": "UTC", "days": 5},
	} {
		if res := h.campaignOn(tenant, sender, template, map[string]any{"quietHours": quiet}); res.Code != http.StatusUnprocessableEntity {
			t.Errorf("S10 %s = %d, want 422", name, res.Code)
		}
	}

	if _, err := h.admin.Exec(context.Background(),
		`UPDATE campaigns SET status = 'paused', paused_at = now() WHERE id = $1`, campaign.ID); err != nil {
		t.Fatal(err)
	}
	h.server.Now = func() time.Time { return insideAt }
	resume := h.do(http.MethodPost, "/v1/campaigns/"+campaign.ID+"/resume", tenant.Token, nil)
	var after struct {
		Status string `json:"status"`
	}
	h.do(http.MethodGet, "/v1/campaigns/"+campaign.ID, tenant.Token, nil).decode(t, &after)
	if resume.Code != http.StatusUnprocessableEntity || resume.errorCode(t) != "quiet_hours" || after.Status != "paused" {
		t.Errorf("S11 resume inside = %d %s, status %s; want 422 quiet_hours and still paused",
			resume.Code, resume.Body, after.Status)
	}
}

type fakeDomains struct {
	cname string
	txt   map[string][]string
	https bool
}

func (f *fakeDomains) CNAME(context.Context, string) (string, error) { return f.cname, nil }
func (f *fakeDomains) TXT(_ context.Context, host string) ([]string, error) {
	return f.txt[host], nil
}
func (f *fakeDomains) ServesHTTPS(context.Context, string) bool { return f.https }

// Batch 5 rows S12 to S16: a tenant's own link domain.
func TestALinkDomainIsUsableOnlyOnceActive(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	dns := &fakeDomains{txt: map[string][]string{}}
	h.server.Domains, h.server.LinkDomainTarget = dns, "links.relay.test"
	tenant := h.newAccount("owner")
	host := "go-" + uuid.NewString()[:8] + ".acme.example"

	if res := h.do(http.MethodPost, "/v1/links/domains", tenant.Token,
		map[string]any{"domain": "https://" + host}); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("S12 a URL, not a host = %d, want 422", res.Code)
	}
	created := h.do(http.MethodPost, "/v1/links/domains", tenant.Token, map[string]any{"domain": host})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	if res := h.do(http.MethodPost, "/v1/links/domains", tenant.Token,
		map[string]any{"domain": strings.ToUpper(host)}); res.Code != http.StatusConflict {
		t.Errorf("S13 the same host in capitals = %d, want 409", res.Code)
	}
	var domain struct {
		ID         string `json:"id"`
		Status     string `json:"status"`
		DNSRecords []struct {
			Type, Host, Value string
		} `json:"dnsRecords"`
	}
	created.decode(t, &domain)

	// S16: a short link on a domain that is not active yet is refused.
	if res := h.do(http.MethodPost, "/v1/links", tenant.Token, map[string]any{
		"destination": "https://acme.example/x", "domainId": domain.ID}); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("S16 link on a pending domain = %d, want 422", res.Code)
	}

	for _, record := range domain.DNSRecords {
		if record.Type == "TXT" {
			dns.txt[record.Host] = []string{record.Value}
		}
	}
	dns.cname, dns.https = "links.relay.test", true
	var stages []string
	for i := 0; i < 3; i++ {
		res := h.do(http.MethodPost, "/v1/links/domains/"+domain.ID+"/validate", tenant.Token, nil)
		res.decode(t, &domain)
		stages = append(stages, domain.Status)
	}
	if strings.Join(stages, ",") != "validating,issuing_certificate,active" {
		t.Errorf("S14 three checks = %v, want validating, issuing_certificate, active", stages)
	}

	link := h.do(http.MethodPost, "/v1/links", tenant.Token, map[string]any{
		"destination": "https://acme.example/x", "domainId": domain.ID})
	if link.Code != http.StatusCreated || !strings.Contains(string(link.Body), "https://"+host+"/l/") {
		t.Errorf("a link on the active domain = %d %s", link.Code, link.Body)
	}
	if res := h.do(http.MethodDelete, "/v1/links/domains/"+domain.ID, tenant.Token, nil); res.Code != http.StatusConflict {
		t.Errorf("S15 delete a domain a link uses = %d, want 409", res.Code)
	}
}

// Batch 5 rows S17 to S19: the body is judged before availability.
func TestLookupsJudgeTheBodyFirstThenAvailability(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tenant := h.newAccount("owner")
	if res := h.do(http.MethodPost, "/v1/lookups", tenant.Token, map[string]any{"msisdn": "nope"}); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("S17 nope = %d, want 422", res.Code)
	}
	numbers := make([]string, 101)
	for i := range numbers {
		numbers[i] = fmt.Sprintf("+9198765%05d", i)
	}
	if res := h.do(http.MethodPost, "/v1/lookups/batch", tenant.Token, map[string]any{"msisdns": numbers}); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("S18 101 numbers = %d, want 422", res.Code)
	}
	if res := h.do(http.MethodPost, "/v1/lookups", tenant.Token,
		map[string]any{"msisdn": "+919876543212"}); res.Code != http.StatusConflict || res.errorCode(t) != "lookup_unavailable" {
		t.Errorf("S19 with no provider = %d, want 409 lookup_unavailable", res.Code)
	}

	h.server.Lookup = connector.FakeLineLookup{}
	var batch struct {
		Results []struct {
			Msisdn   string `json:"msisdn"`
			LineType string `json:"lineType"`
		} `json:"results"`
	}
	h.do(http.MethodPost, "/v1/lookups/batch", tenant.Token,
		map[string]any{"msisdns": []string{"+919876543212", "garbage", "+919876543210"}}).decode(t, &batch)
	got := []string{}
	for _, row := range batch.Results {
		got = append(got, row.LineType)
	}
	if strings.Join(got, ",") != "mobile,unknown,landline" {
		t.Errorf("batch = %v, want one row per number with garbage unknown", got)
	}
}

// Batch 5 rows S20 and S21: the DND register is a stated fact, never a switch,
// and its key is listed by name only.
func TestTheDNDRegisterIsAStepAndAKeyByNameOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tenant := h.newAccount("owner")
	h.server.ConfigKeys = map[string]bool{"DND_MODE": true}
	dndStep := func() string {
		res := h.do(http.MethodGet, "/v1/capabilities", tenant.Token, nil)
		var body struct {
			Channels []struct {
				Channel string `json:"channel"`
				Setup   []struct {
					Step, Status string
				} `json:"setup"`
			} `json:"channels"`
		}
		res.decode(t, &body)
		for _, channel := range body.Channels {
			for _, step := range channel.Setup {
				if channel.Channel == "SMS" && step.Step == "dnd_register" {
					return step.Status
				}
			}
		}
		return ""
	}
	if got := dndStep(); got != "blocked" {
		t.Errorf("S20 deny mode = %q, want blocked", got)
	}
	h.server.DNDMode = "operator"
	if got := dndStep(); got != "done" {
		t.Errorf("S20 operator mode = %q, want done", got)
	}
	body := string(h.do(http.MethodGet, "/v1/operator/channels", h.operatorToken(), nil).Body)
	if !strings.Contains(body, `{"name":"DND_MODE","present":true}`) {
		t.Errorf("S21 DND_MODE not listed by name and presence:\n%s", body)
	}
}
