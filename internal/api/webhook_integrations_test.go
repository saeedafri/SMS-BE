package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The point of the feature: the customer's own CleverTap account gets the
// event in CleverTap's shape with the customer's key on it, still signed by
// Relay, and the delivery log keeps Relay's own payload.
func TestAnIntegrationEndpointReceivesTheVendorsFormat(t *testing.T) {
	h := newSendHarness(t)
	endpoint := startCustomerEndpoint(t)
	tenant := h.newAccount("owner")
	created := h.subscribeEndpoint(tenant, endpoint, "message.delivered")

	res := h.do(http.MethodPut, "/v1/developer/webhooks/"+created.Id.String()+"/integration", tenant.Token,
		map[string]any{"type": "clevertap", "headers": map[string]string{
			"X-CleverTap-Account-Id": "TEST-ACC", "X-CleverTap-Passcode": "s3cret-passcode"}})
	if res.Code != 200 || strings.Contains(string(res.Body), "s3cret-passcode") {
		t.Fatalf("put = %d %s (and no header value may come back)", res.Code, res.Body)
	}
	got := h.do(http.MethodGet, "/v1/developer/webhooks/"+created.Id.String()+"/integration", tenant.Token, nil)
	if !strings.Contains(string(got.Body), `"clevertap"`) || !strings.Contains(string(got.Body), "X-CleverTap-Passcode") ||
		strings.Contains(string(got.Body), "s3cret-passcode") {
		t.Errorf("get = %s", got.Body)
	}

	h.deliverOneSMS(tenant, "9876543210")
	waitFor(t, "the CleverTap-shaped event", func() bool { return len(endpoint.received("message.delivered")) == 1 })
	hook := endpoint.received("message.delivered")[0]
	var body struct {
		D []struct {
			Identity string
			EvtName  string
			Type     string
		}
	}
	if err := json.Unmarshal(hook.Body, &body); err != nil || len(body.D) != 1 ||
		body.D[0].Type != "event" || body.D[0].EvtName != "Relay Message Delivered" ||
		!strings.HasSuffix(body.D[0].Identity, "9876543210") {
		t.Fatalf("body = %s", hook.Body)
	}
	if hook.Header.Get("X-CleverTap-Passcode") != "s3cret-passcode" || hook.Header.Get("X-CleverTap-Account-Id") != "TEST-ACC" {
		t.Errorf("the customer's headers did not arrive: %v", hook.Header)
	}
	if hook.Signature == "" {
		t.Errorf("an integration delivery lost its Relay signature")
	}

	// Back to a plain webhook: Relay's own envelope again.
	if res := h.do(http.MethodPut, "/v1/developer/webhooks/"+created.Id.String()+"/integration", tenant.Token,
		map[string]any{"type": "default"}); res.Code != 200 {
		t.Fatalf("clear = %d %s", res.Code, res.Body)
	}
	// A second real send would seed a second sender with the same header; the
	// test-event route delivers through the same path.
	if res := h.do(http.MethodPost, "/v1/developer/webhooks/"+created.Id.String()+"/test-event", tenant.Token,
		map[string]any{"eventType": "message.delivered"}); res.Code >= 300 {
		t.Fatalf("test event = %d %s", res.Code, res.Body)
	}
	waitFor(t, "the plain event", func() bool { return len(endpoint.received("message.delivered")) == 2 })
	if !strings.Contains(string(endpoint.received("message.delivered")[1].Body), `"event":"message.delivered"`) {
		t.Errorf("cleared endpoint still gets the vendor shape: %s", endpoint.received("message.delivered")[1].Body)
	}
}

func TestIntegrationSettingsAreValidated(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	endpoint := startCustomerEndpoint(t)
	tenant := h.newAccount("owner")
	created := h.subscribeEndpoint(tenant, endpoint, "message.delivered")
	path := "/v1/developer/webhooks/" + created.Id.String() + "/integration"
	ok := map[string]string{"Authorization": "Bearer abc"}
	tooMany := map[string]string{}
	for i := 0; i < 11; i++ {
		tooMany["X-H"+string(rune('a'+i))] = "v"
	}
	for name, body := range map[string]map[string]any{
		"unknown type":       {"type": "salesforce", "headers": ok},
		"no headers":         {"type": "webengage"},
		"empty headers":      {"type": "webengage", "headers": map[string]string{}},
		"reserved header":    {"type": "webengage", "headers": map[string]string{"X-Relay-Signature": "forged"}},
		"content-type":       {"type": "webengage", "headers": map[string]string{"Content-Type": "text/plain"}},
		"bad name":           {"type": "webengage", "headers": map[string]string{"Bad Name": "v"}},
		"newline injection":  {"type": "webengage", "headers": map[string]string{"Authorization": "a\r\nX-Evil: 1"}},
		"empty value":        {"type": "webengage", "headers": map[string]string{"Authorization": ""}},
		"too many":           {"type": "webengage", "headers": tooMany},
		"headers on default": {"type": "default", "headers": ok},
		"unknown field":      {"type": "webengage", "headers": ok, "url": "https://evil.example"},
	} {
		if res := h.do(http.MethodPut, path, tenant.Token, body); res.Code != 422 {
			t.Errorf("%s = %d %s, want 422", name, res.Code, res.Body)
		}
	}
	member := h.newAccount("member")
	if res := h.do(http.MethodPut, path, member.Token, map[string]any{"type": "webengage", "headers": ok}); res.Code != 404 && res.Code != 403 {
		t.Errorf("another tenant's member = %d, want 404/403", res.Code)
	}
	if res := h.do(http.MethodGet, path, "", nil); res.Code != 401 {
		t.Errorf("anonymous = %d, want 401", res.Code)
	}
}
