package api_test

import (
	"net/http"
	"testing"
)

// The stream must refuse an unauthenticated caller before it starts streaming.
//
// An SSE endpoint that authenticates lazily is worse than one that does not
// authenticate at all: the connection opens, the browser reports success, and
// the hole only shows up when somebody reads the events.
//
// The harness wires no Redis, so a 503 here is the other correct answer — the
// point of this test is that neither case leaves the connection hanging open,
// which is what an unauthenticated stream would look like from the client.
func TestEventStreamRefusesAnonymousCallers(t *testing.T) {
	h := newHarness(t)
	res := h.do(http.MethodGet, "/v1/events", "", nil)
	if res.Code != http.StatusUnauthorized && res.Code != http.StatusServiceUnavailable {
		t.Fatalf("anonymous /v1/events = %d, want 401 (or 503 without Redis)\n%s",
			res.Code, res.Body)
	}
}

// An authenticated caller must not be refused for their identity.
//
// Without this the test above passes just as happily against an endpoint that
// refuses everyone, which proves nothing about who may listen.
func TestEventStreamDoesNotRefuseAnAuthenticatedTenant(t *testing.T) {
	h := newHarness(t)
	account := h.newAccount("owner")
	res := h.do(http.MethodGet, "/v1/events", account.Token, nil)
	if res.Code == http.StatusUnauthorized {
		t.Fatalf("an authenticated tenant was refused the stream\n%s", res.Body)
	}
}
