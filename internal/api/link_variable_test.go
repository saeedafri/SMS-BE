package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saeedafri/sms-be/internal/domain/audience"
)

// The {{link}} variable at the API: BACKEND_REQUEST_link-variable.md, L2, L4,
// L5 and L8, rows A1, A2, A10, A11, A12 and A14.

const testLinkBase = "https://links.relay.test"

var (
	needsLink  = "This message uses {{link}}, so the campaign needs a link. Add one under link."
	unusedLink = "This campaign has a link, but its message does not use {{link}}. Add {{link}} to the template or remove the link."
)

// linkTemplates seeds an approved SMS sender and two templates on it: one with
// {{link}}, one without.
func linkTemplates(t *testing.T, h *harness, acct account) (sender, withLink, without string) {
	t.Helper()
	sender = h.approvedSender(acct)
	seed := func(body string) string {
		var id string
		if err := h.admin.QueryRow(context.Background(), `
			INSERT INTO templates (tenant_id, sender_id, name, channel, country, body, variables, status)
			VALUES ($1, $2, $3, 'SMS', 'IN', $4, $5, 'approved') RETURNING id`,
			acct.TenantID, sender, fmt.Sprintf("link %d", rand.Int()), body,
			audience.SlotsIn(body)).Scan(&id); err != nil {
			t.Fatalf("seed template: %v", err)
		}
		return id
	}
	return sender, seed("Sale today: {{link}}"), seed("Sale today in store.")
}

func errorMessage(t *testing.T, res response) string {
	t.Helper()
	var body struct{ Error struct{ Message string } }
	_ = json.Unmarshal(res.Body, &body)
	return body.Error.Message
}

func scheduledCampaign(sender, template string, extra map[string]any) map[string]any {
	body := map[string]any{"name": "Link " + fmt.Sprint(rand.Int()), "channel": "SMS", "country": "IN",
		"senderId": sender, "templateId": template,
		"scheduledAt": time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

// A1: the four ways, plus a {{link}} held only by the fallback.
func TestACampaignLinkAndItsTemplateGoTogether(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	h.server.LinkBaseURL = testLinkBase
	acct := h.newAccount("owner")
	h.fundWallet(acct)
	sender, withLink, without := linkTemplates(t, h, acct)
	link := map[string]any{"destination": "https://shop.example.org/sale", "label": "diwali"}

	res := h.do(http.MethodPost, "/v1/campaigns", acct.Token,
		scheduledCampaign(sender, withLink, map[string]any{"link": link}))
	if res.Code != http.StatusCreated {
		t.Fatalf("template with {{link}} and a link = %d %s, want 201", res.Code, res.Body)
	}
	var created struct {
		ID   string `json:"id"`
		Link *struct{ Destination, Label string }
	}
	_ = json.Unmarshal(res.Body, &created)
	if created.Link == nil || created.Link.Destination != "https://shop.example.org/sale" || created.Link.Label != "diwali" {
		t.Errorf("created link = %+v, want it echoed", created.Link)
	}
	got := h.do(http.MethodGet, "/v1/campaigns/"+created.ID, acct.Token, nil)
	if !strings.Contains(string(got.Body), `"destination":"https://shop.example.org/sale"`) {
		t.Errorf("GET campaign = %s, want its link", got.Body)
	}

	for name, c := range map[string]struct {
		body map[string]any
		want string
	}{
		"template with {{link}}, no link": {scheduledCampaign(sender, withLink, nil), needsLink},
		"null link":                       {scheduledCampaign(sender, withLink, map[string]any{"link": nil}), needsLink},
		"link, template without":          {scheduledCampaign(sender, without, map[string]any{"link": link}), unusedLink},
		"only the fallback has {{link}}": {scheduledCampaign(sender, without, map[string]any{
			"fallback": map[string]any{"channel": "SMS", "senderId": sender, "templateId": withLink}}), needsLink},
		"not an object":  {scheduledCampaign(sender, withLink, map[string]any{"link": "https://x.example"}), "link must be an object with a destination."},
		"unknown field":  {scheduledCampaign(sender, withLink, map[string]any{"link": map[string]any{"destination": "https://shop.example.org/", "campaignId": "x"}}), `link has no field "campaignId".`},
		"expired":        {scheduledCampaign(sender, withLink, map[string]any{"link": map[string]any{"destination": "https://shop.example.org/", "expiresAt": "2020-01-01T00:00:00Z"}}), "expiresAt must be in the future."},
		"points at self": {scheduledCampaign(sender, withLink, map[string]any{"link": map[string]any{"destination": testLinkBase + "/l/abcdefgh"}}), "destination cannot point at Relay itself."},
	} {
		res := h.do(http.MethodPost, "/v1/campaigns", acct.Token, c.body)
		if res.Code != 422 || errorMessage(t, res) != c.want {
			t.Errorf("%s = %d %s, want 422 %q", name, res.Code, res.Body, c.want)
		}
	}

	if res := h.do(http.MethodPost, "/v1/campaigns", acct.Token,
		scheduledCampaign(sender, without, nil)); res.Code != http.StatusCreated {
		t.Errorf("neither = %d %s, want 201", res.Code, res.Body)
	}
	// The fallback-only case is accepted once it has its link.
	if res := h.do(http.MethodPost, "/v1/campaigns", acct.Token, scheduledCampaign(sender, without,
		map[string]any{"link": link, "fallback": map[string]any{
			"channel": "SMS", "senderId": sender, "templateId": withLink}})); res.Code != http.StatusCreated {
		t.Errorf("fallback-only with a link = %d %s, want 201", res.Code, res.Body)
	}
}

// A2: one rule, one sentence. A campaign's destination is refused in exactly
// the words POST /v1/links uses.
func TestACampaignLinkIsRefusedInTheWordsOfPostLinks(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	h.server.LinkBaseURL = testLinkBase
	acct := h.newAccount("owner")
	sender, withLink, _ := linkTemplates(t, h, acct)
	for _, destination := range []string{"ftp://shop.example.org/", "https://bit.ly/abc", ""} {
		one := h.do(http.MethodPost, "/v1/links", acct.Token, map[string]any{"destination": destination})
		campaign := h.do(http.MethodPost, "/v1/campaigns", acct.Token,
			scheduledCampaign(sender, withLink, map[string]any{"link": map[string]any{"destination": destination}}))
		if one.Code != 422 || campaign.Code != 422 || errorMessage(t, one) == "" ||
			errorMessage(t, one) != errorMessage(t, campaign) {
			t.Errorf("%q: POST /v1/links %d %q, campaign %d %q; want the same 422 sentence",
				destination, one.Code, errorMessage(t, one), campaign.Code, errorMessage(t, campaign))
		}
	}
}

// A retry child keeps its parent's link.
func TestARetryKeepsItsParentsLink(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	h.server.LinkBaseURL = testLinkBase
	acct := h.newAccount("owner")
	h.fundWallet(acct)
	sender, withLink, _ := linkTemplates(t, h, acct)
	res := h.do(http.MethodPost, "/v1/campaigns", acct.Token, scheduledCampaign(sender, withLink,
		map[string]any{"link": map[string]any{"destination": "https://shop.example.org/again"}}))
	var parent struct{ ID string }
	_ = json.Unmarshal(res.Body, &parent)
	child := h.do(http.MethodPost, "/v1/campaigns", acct.Token,
		scheduledCampaign(sender, withLink, map[string]any{"retryOf": parent.ID}))
	if child.Code != http.StatusCreated || !strings.Contains(string(child.Body), "https://shop.example.org/again") {
		t.Errorf("retry = %d %s, want the parent's link", child.Code, child.Body)
	}
}

// L4 and A10: the base comes from configuration, not from the request, and
// GET /v1/links/config says what it is.
func TestTheLinkBaseIsConfiguredAndPublished(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	h.server.LinkBaseURL = testLinkBase
	acct := h.newAccount("owner")
	res := h.do(http.MethodGet, "/v1/links/config", acct.Token, nil)
	var config struct {
		BaseURL    string `json:"baseUrl"`
		CodeLength int    `json:"codeLength"`
	}
	_ = json.Unmarshal(res.Body, &config)
	if res.Code != 200 || config.BaseURL != testLinkBase || config.CodeLength != 8 {
		t.Fatalf("config = %d %s", res.Code, res.Body)
	}
	made := h.do(http.MethodPost, "/v1/links", acct.Token, map[string]any{"destination": "https://shop.example.org/"})
	var link linkResult
	_ = json.Unmarshal(made.Body, &link)
	if link.ShortURL != testLinkBase+"/l/"+link.Code {
		t.Errorf("shortUrl = %q, want it on the configured base", link.ShortURL)
	}
	if res := h.do(http.MethodGet, "/v1/links/config", "", nil); res.Code != 401 {
		t.Errorf("anonymous config = %d, want 401", res.Code)
	}

	// Unset, today's behaviour stands: the request's own origin.
	h.server.LinkBaseURL = ""
	res = h.do(http.MethodGet, "/v1/links/config", acct.Token, nil)
	_ = json.Unmarshal(res.Body, &config)
	if res.Code != 200 || !strings.HasPrefix(config.BaseURL, "http") || config.BaseURL == testLinkBase {
		t.Errorf("unset config = %d %s, want the request's origin", res.Code, res.Body)
	}
}

// A11: bulk twice for the same campaign and people still makes two sets.
func TestBulkTwiceStillMakesTwoSets(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	campaign := "6f1c2b9a-0000-4000-8000-" + fmt.Sprintf("%012d", rand.Int63n(1e12))
	for range 2 {
		if res := h.do(http.MethodPost, "/v1/links/bulk", acct.Token, map[string]any{
			"destination": "https://shop.example.org/", "campaignId": campaign,
			"recipients": []string{"+919810000601", "+919810000602"}}); res.Code != 201 {
			t.Fatalf("bulk = %d %s", res.Code, res.Body)
		}
	}
	res := h.do(http.MethodGet, "/v1/links?campaignId="+campaign, acct.Token, nil)
	var page struct{ Total int }
	_ = json.Unmarshal(res.Body, &page)
	if page.Total != 4 {
		t.Errorf("%d links after two bulk calls for two people, want 4", page.Total)
	}
}

type testLink struct {
	Code        string  `json:"code"`
	ShortURL    string  `json:"shortUrl"`
	Destination string  `json:"destination"`
	CampaignID  *string `json:"campaignId"`
	Recipient   string  `json:"recipient"`
	Label       string  `json:"label"`
	ExpiresAt   string  `json:"expiresAt"`
}

func listLinks(t *testing.T, h *harness, token string) []testLink {
	t.Helper()
	res := h.do(http.MethodGet, "/v1/links", token, nil)
	var page struct{ Links []testLink }
	_ = json.Unmarshal(res.Body, &page)
	return page.Links
}

// A12 and L8: a test send with {{link}} carries a real link — for that
// number, no campaign, labelled a test, a week to live — and the message holds
// it. Without a link it is refused as on create; a number the gate refuses gets
// no link at all.
func TestATestSendFillsLinkWithARealLink(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	h.server.LinkBaseURL = testLinkBase
	acct := h.newAccount("owner")
	sender, withLink, without := linkTemplates(t, h, acct)
	send := func(template string, extra map[string]any) response {
		body := map[string]any{"senderId": sender, "templateId": template,
			"body": "Sale today: {{link}}", "recipients": []string{"9810000701"}}
		for k, v := range extra {
			body[k] = v
		}
		return h.do(http.MethodPost, "/v1/messages/test", acct.Token, body)
	}
	link := map[string]any{"destination": "https://shop.example.org/test"}

	// Unfunded: the gate refuses for balance, and no link is made.
	refused := send(withLink, map[string]any{"link": link})
	if refused.Code != 202 || !strings.Contains(string(refused.Body), "insufficient_balance") {
		t.Fatalf("unfunded test send = %d %s", refused.Code, refused.Body)
	}
	if links := listLinks(t, h, acct.Token); len(links) != 0 {
		t.Fatalf("%d links made for a refused test send, want none", len(links))
	}

	h.fundWallet(acct)
	res := send(withLink, map[string]any{"link": link,
		"variables": map[string]string{"link": "https://untracked.example/"}})
	if res.Code != 202 || strings.Contains(string(res.Body), "rejected") {
		t.Fatalf("test send = %d %s", res.Code, res.Body)
	}
	links := listLinks(t, h, acct.Token)
	if len(links) != 1 {
		t.Fatalf("links = %+v, want one", links)
	}
	made := links[0]
	expires, _ := time.Parse(time.RFC3339, made.ExpiresAt)
	if made.CampaignID != nil || made.Recipient != "+919810000701" || made.Label != "test send" ||
		made.Destination != "https://shop.example.org/test" ||
		expires.Sub(time.Now()) < 6*24*time.Hour || expires.Sub(time.Now()) > 8*24*time.Hour {
		t.Errorf("test link = %+v", made)
	}
	var text *string
	conn, err := h.server.ClickHouse.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(context.Background(), `
		SELECT rendered_text FROM messages FINAL WHERE tenant_id = ? AND status != 'rejected'`,
		acct.TenantID).Scan(&text); err != nil || text == nil ||
		*text != "Sale today: "+testLinkBase+"/l/"+made.Code {
		t.Errorf("logged text = %v (%v), want the real link in the body", text, err)
	}

	if res := send(withLink, nil); res.Code != 422 || errorMessage(t, res) != needsLink {
		t.Errorf("no link = %d %s, want 422 %q", res.Code, res.Body, needsLink)
	}
	if res := send(without, map[string]any{"link": link, "body": "Sale today in store."}); res.Code != 422 ||
		errorMessage(t, res) != unusedLink {
		t.Errorf("unused link = %d %s, want 422 %q", res.Code, res.Body, unusedLink)
	}
}

// A14: the same body both ways through the estimate.
func TestTheEstimateCarriesTheLink(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	h.server.LinkBaseURL = testLinkBase
	acct := h.newAccount("owner")
	list := createList(t, h, acct.Token, "Link "+fmt.Sprint(rand.Int()))
	importRows(t, h, acct.Token, list.Id.String(), []map[string]any{
		{"msisdn": number()}, {"msisdn": number()},
	}, "link-"+fmt.Sprint(rand.Int()))
	template := seedSlotTemplate(t, h, acct, strings.Repeat("a", 160)+"{{link}}")
	body := map[string]any{"listId": list.Id.String(), "country": "IN", "channel": "SMS", "templateId": template}

	without := estimateFor(t, h, acct.Token, body)
	if without.Recipients != 0 || without.VariableSkippedByName["link"] != 2 {
		t.Errorf("without a link = %+v, want both skipped by the name link", without)
	}
	body["link"] = map[string]any{"destination": "https://shop.example.org/"}
	res := h.do(http.MethodPost, "/v1/campaigns/estimate", acct.Token, body)
	var with struct {
		Recipients, SegmentsPerMessageMin, SegmentsPerMessageMax int
	}
	_ = json.Unmarshal(res.Body, &with)
	if res.Code != 200 || with.Recipients != 2 || with.SegmentsPerMessageMin != 2 || with.SegmentsPerMessageMax != 2 {
		t.Errorf("with a link = %d %s, want 2 recipients at exactly 2 segments", res.Code, res.Body)
	}
}
