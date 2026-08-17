package api_test

import (
	"net/http"
	"testing"
)

// A member must not reach any mutating route in an area the product gates.
//
// This exists because every one of these routes was authenticated and nothing
// more, and that was not caught by any test or review — it was found by probing
// the deployed server as each role and comparing the results against what the
// frontend allows. A member could mint a live API key with messages:write and
// be handed the secret, and could point a webhook at an external URL that would
// then receive every delivery event for the tenant.
//
// The frontend hides all of this from a member. That is not a defence: it stops
// a button being drawn, not a request being sent. The contract already declared
// a 403 on each of these, so the guard was always intended.
//
// Written as a TABLE of every route in the gated areas rather than a test for
// the one that was exploited, because the failure here was systemic — the guard
// was missing from all of them at once, so pinning a single route would leave
// the same hole open on its eleven neighbours.
func TestMemberIsRefusedMutatingRoutesInGatedAreas(t *testing.T) {
	h := newHarness(t)
	member := h.newAccount("member")

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		// Developer — credentials. The escalation vectors: an API key
		// authenticates on its own afterwards, so it outlives the role check
		// that should have stopped it being issued.
		{"create api key", http.MethodPost, "/v1/developer/api-keys",
			map[string]any{"name": "k", "environment": "live", "scopes": []string{"messages:write"}}},
		{"create webhook", http.MethodPost, "/v1/developer/webhooks",
			map[string]any{"url": "https://example.test/hook", "environment": "live",
				"events": []string{"message.delivered"}}},
		{"add ip allowlist entry", http.MethodPost, "/v1/developer/ip-allowlist",
			map[string]any{"cidr": "203.0.113.0/24", "label": "office"}},

		// Settings — tenant-wide configuration.
		{"update alerts", http.MethodPatch, "/v1/alerts",
			map[string]any{"lowBalanceEnabled": true, "lowBalanceThresholdMinor": 1000}},

		// Verify — service configuration lives behind the developer area.
		{"create verify service", http.MethodPost, "/v1/verify/services",
			map[string]any{"name": "svc", "channel": "SMS"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := h.do(tc.method, tc.path, member.Token, tc.body)
			if res.Code != http.StatusForbidden {
				t.Fatalf("%s %s as member = %d, want 403\n%s",
					tc.method, tc.path, res.Code, string(res.Body))
			}
		})
	}
}

// The control for the test above.
//
// Without it, a handler that returned 403 to EVERYONE — or one that had been
// deleted outright — would satisfy every assertion up there and look like a
// pass. Asserting only that the refused role is refused proves the route is
// closed, not that it is closed to the right people.
func TestOwnerIsNotRefusedTheSameRoutes(t *testing.T) {
	h := newHarness(t)
	owner := h.newAccount("owner")

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"create api key", http.MethodPost, "/v1/developer/api-keys",
			map[string]any{"name": "k", "environment": "live", "scopes": []string{"messages:write"}}},
		{"create webhook", http.MethodPost, "/v1/developer/webhooks",
			map[string]any{"url": "https://example.test/hook", "environment": "live",
				"events": []string{"message.delivered"}}},
		{"update alerts", http.MethodPatch, "/v1/alerts",
			map[string]any{"lowBalanceEnabled": true, "lowBalanceThresholdMinor": 1000}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := h.do(tc.method, tc.path, owner.Token, tc.body)
			// Any non-403 is a pass. A 422 would mean authorisation succeeded
			// and only the body was rejected, which is still proof the owner is
			// not being refused for their ROLE.
			if res.Code == http.StatusForbidden {
				t.Fatalf("%s %s as owner = 403; the guard is refusing the wrong people\n%s",
					tc.method, tc.path, string(res.Body))
			}
		})
	}
}
