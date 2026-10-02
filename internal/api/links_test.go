package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type linkResult struct {
	Code     string `json:"code"`
	ShortURL string `json:"shortUrl"`
}

func (h *harness) follow(method, code, agent string) *httptest.ResponseRecorder {
	// Production has no migration-role pool in the API process; the redirect has
	// to work without one, and the first version did not.
	h.server.AdminDB = nil
	req := httptest.NewRequest(method, "/l/"+code, nil)
	req.Host = "relay.example.test"
	if agent != "" {
		req.Header.Set("User-Agent", agent)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

const phoneAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 Safari/604.1"

// The whole path a recipient takes: create, tap, get redirected, and the tap is
// in the logs; a link-preview fetch and a HEAD are not counted as a person.
func TestALinkRedirectsAndItsClicksAreCountedHonestly(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	owner := h.newAccount("owner")

	res := h.do(http.MethodPost, "/v1/links", owner.Token, map[string]any{
		"destination": "https://shop.example.org/offer?a=1", "recipient": "+91 98100 00301",
		"label": "diwali", "appendClickId": true})
	if res.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", res.Code, res.Body)
	}
	var link linkResult
	_ = json.Unmarshal(res.Body, &link)
	if len(link.Code) != 8 || !strings.HasSuffix(link.ShortURL, "/l/"+link.Code) {
		t.Fatalf("link = %+v", link)
	}

	rec := h.follow(http.MethodGet, link.Code, phoneAgent)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusFound || !strings.HasPrefix(loc, "https://shop.example.org/offer?") ||
		!strings.Contains(loc, "a=1") || !strings.Contains(loc, "click_id="+link.Code) {
		t.Fatalf("redirect = %d %q", rec.Code, loc)
	}
	h.follow(http.MethodHead, link.Code, phoneAgent)
	h.follow(http.MethodGet, link.Code, "WhatsApp/2.23.20 A")

	var stats struct {
		Links, Clicks, HumanClicks, BotClicks, UniqueClickers int
		ByDevice                                              map[string]int
	}
	res = h.do(http.MethodGet, "/v1/links/stats", owner.Token, nil)
	_ = json.Unmarshal(res.Body, &stats)
	if stats.Clicks != 2 || stats.HumanClicks != 1 || stats.BotClicks != 1 ||
		stats.UniqueClickers != 1 || stats.ByDevice["mobile"] != 1 {
		t.Errorf("stats = %s, want 2 clicks of which 1 human (HEAD uncounted)", res.Body)
	}

	var clicks struct {
		Clicks []struct {
			Device, Os, Recipient string
			IsBot                 bool
		}
		Total int
	}
	res = h.do(http.MethodGet, "/v1/links/clicks", owner.Token, nil)
	_ = json.Unmarshal(res.Body, &clicks)
	if clicks.Total != 1 || clicks.Clicks[0].Os != "ios" || clicks.Clicks[0].Recipient != "+919810000301" {
		t.Errorf("default click log = %s, want the one human click", res.Body)
	}
	res = h.do(http.MethodGet, "/v1/links/clicks?includeBots=true", owner.Token, nil)
	_ = json.Unmarshal(res.Body, &clicks)
	if clicks.Total != 2 {
		t.Errorf("with bots = %s, want 2", res.Body)
	}
	var list struct {
		Links []struct{ Clicks, HumanClicks int }
	}
	res = h.do(http.MethodGet, "/v1/links", owner.Token, nil)
	_ = json.Unmarshal(res.Body, &list)
	if len(list.Links) != 1 || list.Links[0].Clicks != 2 || list.Links[0].HumanClicks != 1 {
		t.Errorf("list = %s", res.Body)
	}
}

func TestUnknownAndExpiredLinksAnswerCleanly(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	owner := h.newAccount("owner")
	if rec := h.follow(http.MethodGet, "nope1234", phoneAgent); rec.Code != http.StatusNotFound {
		t.Errorf("unknown code = %d, want 404", rec.Code)
	}
	res := h.do(http.MethodPost, "/v1/links", owner.Token, map[string]any{
		"destination": "https://shop.example.org/x"})
	var link linkResult
	_ = json.Unmarshal(res.Body, &link)
	if _, err := h.admin.Exec(t.Context(),
		`UPDATE short_links SET expires_at = now() - interval '1 minute' WHERE code = $1`, link.Code); err != nil {
		t.Fatal(err)
	}
	if rec := h.follow(http.MethodGet, link.Code, phoneAgent); rec.Code != http.StatusGone {
		t.Errorf("expired = %d, want 410", rec.Code)
	}
}

func TestLinkDestinationsAreVetted(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	owner := h.newAccount("owner")
	for name, body := range map[string]map[string]any{
		"points at relay":   {"destination": "https://example.com/loop"},
		"javascript scheme": {"destination": "javascript:alert(1)"},
		"data scheme":       {"destination": "data:text/html,hi"},
		"relative":          {"destination": "/just/a/path"},
		"empty":             {"destination": ""},
		"credentials":       {"destination": "https://user:pw@example.com/"},
		"too long":          {"destination": "https://shop.example.org/" + strings.Repeat("a", 2100)},
		"public shortener":  {"destination": "https://bit.ly/abc"},
		"bad recipient":     {"destination": "https://shop.example.org/", "recipient": "abc"},
		"past expiry":       {"destination": "https://shop.example.org/", "expiresAt": "2020-01-01T00:00:00Z"},
		"long label":        {"destination": "https://shop.example.org/", "label": strings.Repeat("a", 121)},
		"unknown field":     {"destination": "https://shop.example.org/", "color": "red"},
	} {
		if res := h.do(http.MethodPost, "/v1/links", owner.Token, body); res.Code != 422 {
			t.Errorf("%s = %d %s, want 422", name, res.Code, res.Body)
		}
	}
	if res := h.do(http.MethodPost, "/v1/links", "", map[string]any{"destination": "https://shop.example.org/"}); res.Code != 401 {
		t.Errorf("anonymous create = %d, want 401", res.Code)
	}
}

func TestBulkLinksGiveEachRecipientTheirOwnCodeAndDeDuplicate(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	owner := h.newAccount("owner")
	res := h.do(http.MethodPost, "/v1/links/bulk", owner.Token, map[string]any{
		"destination": "https://shop.example.org/p", "appendClickId": true,
		"recipients": []string{"+919810000401", "+919810000402", "+91 98100 00401"}})
	var out struct{ Links []linkResult }
	_ = json.Unmarshal(res.Body, &out)
	if res.Code != 201 || len(out.Links) != 2 || out.Links[0].Code == out.Links[1].Code {
		t.Fatalf("bulk = %d %s, want 2 distinct links", res.Code, res.Body)
	}
	if res := h.do(http.MethodPost, "/v1/links/bulk", owner.Token, map[string]any{
		"destination": "https://shop.example.org/p", "recipients": []string{}}); res.Code != 422 {
		t.Errorf("empty bulk = %d, want 422", res.Code)
	}
	tooMany := make([]string, 1001)
	for i := range tooMany {
		tooMany[i] = "+9198100" + string(rune('0'+i/1000%10)) + string(rune('0'+i/100%10)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10))
	}
	if res := h.do(http.MethodPost, "/v1/links/bulk", owner.Token, map[string]any{
		"destination": "https://shop.example.org/p", "recipients": tooMany}); res.Code != 422 {
		t.Errorf("1001 recipients = %d, want 422", res.Code)
	}
}

// One tenant never sees another's links or clicks.
func TestLinksAreIsolatedBetweenTenants(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	one, two := h.newAccount("owner"), h.newAccount("owner")
	res := h.do(http.MethodPost, "/v1/links", one.Token, map[string]any{"destination": "https://shop.example.org/z"})
	var link linkResult
	_ = json.Unmarshal(res.Body, &link)
	h.follow(http.MethodGet, link.Code, phoneAgent)
	for _, path := range []string{"/v1/links", "/v1/links/clicks?includeBots=true"} {
		res := h.do(http.MethodGet, path, two.Token, nil)
		if !strings.Contains(string(res.Body), `"total":0`) {
			t.Errorf("tenant two on %s sees %s", path, res.Body)
		}
	}
}
