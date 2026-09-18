package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saeedafri/sms-be/internal/api"
)

// doFrom sends a request whose TCP peer is remoteAddr, the way nginx on the
// same host (127.0.0.1) or a stranger on the internet would arrive.
func (h *harness) doFrom(remoteAddr, method, path, token string, body any,
	headers map[string]string) response {
	h.t.Helper()
	var req *http.Request
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal request: %v", err)
		}
		req = httptest.NewRequest(method, path, newBytesReader(encoded))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.RemoteAddr = remoteAddr
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return response{Code: rec.Code, Body: rec.Body.Bytes(), Header: rec.Header()}
}

// keyAllowlistedTo seeds a read key whose test environment is restricted to cidr.
func (h *harness) keyAllowlistedTo(cidr string) string {
	h.t.Helper()
	tenant := h.newAccount("owner")
	secret := h.apiKey(tenant, []string{"read:messages"})
	res := h.do(http.MethodPost, "/v1/developer/ip-allowlist", tenant.Token,
		map[string]any{"environment": "test", "cidr": cidr})
	if res.Code != http.StatusCreated {
		h.t.Fatalf("add %s = %d\n%s", cidr, res.Code, res.Body)
	}
	return secret
}

// No header a caller types may choose the address an allowlist checks. The
// harness's own peer is 192.0.2.1, which is not a proxy, so every header here is
// text the caller wrote.
func TestAHeaderCannotChooseTheCallersAddress(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	secret := h.keyAllowlistedTo("203.0.113.0/24")

	for _, header := range []string{"True-Client-IP", "X-Real-IP", "X-Forwarded-For"} {
		t.Run(header, func(t *testing.T) {
			res := h.doFrom("192.0.2.1:1234", http.MethodGet, "/v1/messages", secret, nil,
				map[string]string{header: "203.0.113.9"})
			if res.Code != http.StatusForbidden {
				t.Fatalf("%s: 203.0.113.9 from 192.0.2.1 = %d, want 403\n%s",
					header, res.Code, res.Body)
			}
		})
	}
}

// nginx on the same host is the one peer whose X-Real-IP is the caller. Its
// True-Client-IP is still the caller's own text, passed through untouched.
func TestTheLocalProxysRealIPIsTheCaller(t *testing.T) {
	h := newSendHarness(t)
	secret := h.keyAllowlistedTo("203.0.113.0/24")
	loopback, err := api.ParseIPAllowlist("127.0.0.1/32,::1/128")
	if err != nil {
		t.Fatal(err)
	}
	h.server.TrustedProxies = loopback.Networks()
	h.rebuildRouter()

	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"real ip inside the list", map[string]string{"X-Real-IP": "203.0.113.9"}, http.StatusOK},
		{"real ip outside the list", map[string]string{"X-Real-IP": "198.51.100.1"}, http.StatusForbidden},
		{"true-client-ip cannot override the proxy", map[string]string{
			"True-Client-IP": "203.0.113.9", "X-Real-IP": "198.51.100.1"}, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := h.doFrom("127.0.0.1:40000", http.MethodGet, "/v1/messages", secret, nil, tc.headers)
			if res.Code != tc.want {
				t.Fatalf("from 127.0.0.1 with %v = %d, want %d\n%s", tc.headers, res.Code, tc.want, res.Body)
			}
		})
	}
}

// The operator console's network gate is the pre-auth door to the most valuable
// surface on the deployment. A header must not open it. It answers 404, not 403,
// by design: a 403 confirms a console exists at this address.
func TestTheOperatorConsoleIgnoresASpoofedAddress(t *testing.T) {
	h := newHarness(t)
	allowlist, err := api.ParseIPAllowlist("203.0.113.0/24")
	if err != nil {
		t.Fatal(err)
	}
	h.server.OperatorAllowlist = allowlist
	h.rebuildRouter()

	res := h.doFrom("192.0.2.1:1234", http.MethodPost, "/v1/operator/login", "",
		map[string]any{"email": harnessOperatorEmail, "password": harnessOperatorPassword},
		map[string]string{"True-Client-IP": "203.0.113.9"})
	if res.Code != http.StatusNotFound {
		t.Fatalf("operator login from 192.0.2.1 claiming 203.0.113.9 = %d, want the allowlist's 404\n%s",
			res.Code, res.Body)
	}
}

// Carrier callbacks settle money and decide templates. The token is right here,
// so a 404 can only be the allowlist refusing the real peer.
func TestACarrierWebhookIgnoresASpoofedAddress(t *testing.T) {
	h := newCarrierHarness(t, &stubRegistrar{vendor: "airtel"})
	allowlist, err := api.ParseIPAllowlist("203.0.113.0/24")
	if err != nil {
		t.Fatal(err)
	}
	h.server.CarrierWebhookAllowlist = allowlist
	h.rebuildRouter()

	res := h.doFrom("192.0.2.1:1234", http.MethodPost, "/v1/carrier-webhooks/rcs/airtel/"+webhookToken, "",
		map[string]any{"messageId": "someone-elses-template", "eventType": "DELIVERED"},
		map[string]string{"True-Client-IP": "203.0.113.9"})
	if res.Code != http.StatusNotFound {
		t.Fatalf("callback from 192.0.2.1 claiming 203.0.113.9 = %d, want the allowlist's 404\n%s",
			res.Code, res.Body)
	}
}
