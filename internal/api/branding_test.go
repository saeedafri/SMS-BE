package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func (h *harness) publicBranding(host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/v1/branding/public?host="+host, nil)
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func uniqueDomain() string { return "login-" + uuid.NewString()[:8] + ".example.org" }

var brandBody = func(domain string) map[string]any {
	return map[string]any{"displayName": "Acme Messaging", "logoUrl": "https://cdn.example.org/logo.png",
		"primaryColor": "#1A73E8", "secondaryColor": "#202124", "supportEmail": "help@example.org",
		"customDomain": domain}
}

// A custom domain is stored at once and served only after the owner proves
// control of it in DNS; a squatter's claim never themes anybody's page.
func TestACustomDomainIsServedOnlyOnceItsDNSProofExists(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	domain := uniqueDomain()

	res := h.do(http.MethodPut, "/v1/branding", tenant.Token, brandBody(strings.ToUpper(domain)))
	var out struct {
		DomainVerified bool
		CustomDomain   string
		DNSRecord      struct{ Type, Name, Value string }
	}
	_ = json.Unmarshal(res.Body, &out)
	if res.Code != 200 || out.CustomDomain != domain || out.DomainVerified ||
		out.DNSRecord.Name != "_relay-verify."+domain || !strings.HasPrefix(out.DNSRecord.Value, "relay-verify=") {
		t.Fatalf("put = %d %s", res.Code, res.Body)
	}
	if rec := h.publicBranding(domain); rec.Code != 404 {
		t.Fatalf("an unverified domain was served: %d %s", rec.Code, rec.Body)
	}

	// Wrong record: still not served.
	h.server.TXT = func(context.Context, string) ([]string, error) { return []string{"relay-verify=nope"}, nil }
	res = h.do(http.MethodPost, "/v1/branding/verify-domain", tenant.Token, nil)
	if !strings.Contains(string(res.Body), `"domainVerified":false`) || h.publicBranding(domain).Code != 404 {
		t.Fatalf("a wrong record verified: %s", res.Body)
	}
	// The right record.
	h.server.TXT = func(_ context.Context, name string) ([]string, error) {
		if name != "_relay-verify."+domain {
			t.Errorf("looked up %q", name)
		}
		return []string{"v=spf1 -all", out.DNSRecord.Value}, nil
	}
	if res = h.do(http.MethodPost, "/v1/branding/verify-domain", tenant.Token, nil); !strings.Contains(string(res.Body), `"domainVerified":true`) {
		t.Fatalf("verify = %s", res.Body)
	}
	rec := h.publicBranding(domain)
	var shown map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &shown)
	if rec.Code != 200 || shown["displayName"] != "Acme Messaging" || shown["primaryColor"] != "#1A73E8" {
		t.Fatalf("public = %d %s", rec.Code, rec.Body)
	}
	if _, leaked := shown["domainToken"]; leaked || strings.Contains(rec.Body.String(), "relay-verify") {
		t.Errorf("the public lookup leaked the verification token: %s", rec.Body)
	}

	// Changing the domain drops the proof, which was for the old name.
	other := uniqueDomain()
	h.do(http.MethodPut, "/v1/branding", tenant.Token, brandBody(other))
	if h.publicBranding(domain).Code != 404 || h.publicBranding(other).Code != 404 {
		t.Errorf("a changed domain kept serving on proof for the old one")
	}
}

func TestOnlyOneAccountMayClaimADomain(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	one, two := h.newAccount("owner"), h.newAccount("owner")
	domain := uniqueDomain()
	if res := h.do(http.MethodPut, "/v1/branding", one.Token, brandBody(domain)); res.Code != 200 {
		t.Fatalf("first claim = %d", res.Code)
	}
	if res := h.do(http.MethodPut, "/v1/branding", two.Token, brandBody(domain)); res.Code != http.StatusConflict {
		t.Errorf("second claim = %d %s, want 409", res.Code, res.Body)
	}
	var seen struct{ Configured bool }
	_ = json.Unmarshal(h.do(http.MethodGet, "/v1/branding", two.Token, nil).Body, &seen)
	if seen.Configured {
		t.Errorf("a refused claim left settings behind")
	}
}

func TestBrandingRefusesWhatCouldBreakAPageOrAPerson(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	with := func(k string, v any) map[string]any {
		b := brandBody(uniqueDomain())
		b[k] = v
		return b
	}
	for name, body := range map[string]map[string]any{
		"http logo":         with("logoUrl", "http://cdn.example.org/l.png"),
		"javascript logo":   with("logoUrl", "javascript:alert(1)"),
		"data logo":         with("logoUrl", "data:image/svg+xml,<svg/>"),
		"credentials logo":  with("logoUrl", "https://u:p@cdn.example.org/l.png"),
		"css colour":        with("primaryColor", "red;background:url(x)"),
		"short colour":      with("primaryColor", "#fff"),
		"bad email":         with("supportEmail", "not-an-email"),
		"name and address":  with("supportEmail", "Help <help@example.org>"),
		"ip as domain":      with("customDomain", "10.0.0.1"),
		"bare domain":       with("customDomain", "localhost"),
		"domain with path":  with("customDomain", "a.example.org/x"),
		"domain with port":  with("customDomain", "a.example.org:8080"),
		"long name":         with("displayName", strings.Repeat("a", 81)),
		"unknown field":     with("tenantId", uuid.NewString()),
	} {
		if res := h.do(http.MethodPut, "/v1/branding", tenant.Token, body); res.Code != 422 {
			t.Errorf("%s = %d %s, want 422", name, res.Code, res.Body)
		}
	}
	for _, host := range []string{"", "nope", "a.example.org%0d%0ax", "1.2.3.4", "unknown-" + uuid.NewString()[:6] + ".example.org"} {
		if rec := h.publicBranding(host); rec.Code != 404 {
			t.Errorf("public host %q = %d, want 404", host, rec.Code)
		}
	}
	if res := h.do(http.MethodPost, "/v1/branding/verify-domain", tenant.Token, nil); res.Code != 409 {
		t.Errorf("verify with no domain = %d, want 409", res.Code)
	}
	member := h.newAccount("member")
	if res := h.do(http.MethodPut, "/v1/branding", member.Token, brandBody(uniqueDomain())); res.Code != 403 {
		t.Errorf("member = %d, want 403", res.Code)
	}
	if res := h.do(http.MethodGet, "/v1/branding", "", nil); res.Code != 401 {
		t.Errorf("anonymous = %d, want 401", res.Code)
	}
	if res := h.do(http.MethodDelete, "/v1/branding", tenant.Token, nil); res.Code != 204 {
		t.Errorf("delete = %d", res.Code)
	}
}
