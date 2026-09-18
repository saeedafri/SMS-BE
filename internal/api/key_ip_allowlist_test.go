package api_test

import (
	"net/http"
	"testing"
)

// The IP allowlist on the developer screen restricts where a key works. A key
// used from outside it is refused; adding the caller's range lets it through,
// immediately rather than after a cache expires.
func TestAnApiKeyIsRefusedFromOutsideItsIpAllowlist(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	secret := h.apiKey(tenant, []string{"read:messages"})

	if res := h.do(http.MethodGet, "/v1/messages", secret, nil); res.Code != http.StatusOK {
		t.Fatalf("no allowlist = %d, want 200 — an empty allowlist restricts nothing\n%s",
			res.Code, res.Body)
	}

	add := func(cidr string) string {
		res := h.do(http.MethodPost, "/v1/developer/ip-allowlist", tenant.Token,
			map[string]any{"environment": "test", "cidr": cidr})
		if res.Code != http.StatusCreated {
			t.Fatalf("add %s = %d\n%s", cidr, res.Code, res.Body)
		}
		var entry struct {
			ID string `json:"id"`
		}
		res.decode(t, &entry)
		return entry.ID
	}
	add("203.0.113.0/24")
	if res := h.do(http.MethodGet, "/v1/messages", secret, nil); res.Code != http.StatusForbidden {
		t.Fatalf("key from an unlisted address = %d, want 403\n%s", res.Code, res.Body)
	}

	// httptest requests come from 192.0.2.1.
	listed := add("192.0.2.0/24")
	if res := h.do(http.MethodGet, "/v1/messages", secret, nil); res.Code != http.StatusOK {
		t.Fatalf("key from a listed address = %d, want 200\n%s", res.Code, res.Body)
	}

	h.do(http.MethodDelete, "/v1/developer/ip-allowlist/"+listed, tenant.Token, nil)
	if res := h.do(http.MethodGet, "/v1/messages", secret, nil); res.Code != http.StatusForbidden {
		t.Fatalf("after removing the caller's range = %d, want 403\n%s", res.Code, res.Body)
	}
}
