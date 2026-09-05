package api_test

import (
	"net/http"
	"testing"
)

// A webhook subscribed to nothing is worse than a refused one: it presents as
// enabled, mints a signing secret, and is permanently silent. The customer's
// integration never fires and there is nothing on the screen or in the response
// saying why.
//
// subscribedEvents is required in the contract, but an omitted array decodes to
// an empty one rather than to an error — so the requiredness has to be checked
// in the handler, exactly where `environment` already was.
func TestCreatingAWebhookSubscribedToNothingIsRefused(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	for _, body := range []map[string]any{
		{"url": "https://example.test/hook", "environment": "test"},
		{"url": "https://example.test/hook", "environment": "test", "subscribedEvents": []string{}},
	} {
		response := h.do(http.MethodPost, "/v1/developer/webhooks", acct.Token, body)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("create with %v = %d, want 422 — it creates a permanently "+
				"silent endpoint that presents as enabled\n%s",
				body, response.Code, response.Body)
		}
	}
}

func TestCreatingAWebhookWithEventsStillWorks(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	response := h.do(http.MethodPost, "/v1/developer/webhooks", acct.Token, map[string]any{
		"url": "https://example.test/hook", "environment": "test",
		"subscribedEvents": []string{"message.delivered"}})
	if response.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201\n%s", response.Code, response.Body)
	}
	var created struct {
		ID               string   `json:"id"`
		SubscribedEvents []string `json:"subscribedEvents"`
		Status           string   `json:"status"`
	}
	response.decode(t, &created)
	if len(created.SubscribedEvents) != 1 || created.SubscribedEvents[0] != "message.delivered" {
		t.Fatalf("subscribedEvents = %v", created.SubscribedEvents)
	}
}

// A webhook can be silenced by editing it just as easily as by creating it
// wrong, and update did not check either rule the create path checks.
func TestAWebhookCannotBeSilencedOrCorruptedByAnUpdate(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	created := h.do(http.MethodPost, "/v1/developer/webhooks", acct.Token, map[string]any{
		"url": "https://example.test/hook", "environment": "test",
		"subscribedEvents": []string{"message.delivered"}})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d\n%s", created.Code, created.Body)
	}
	var hook struct {
		ID string `json:"id"`
	}
	created.decode(t, &hook)

	emptied := h.do(http.MethodPatch, "/v1/developer/webhooks/"+hook.ID, acct.Token,
		map[string]any{"subscribedEvents": []string{}})
	if emptied.Code != http.StatusUnprocessableEntity {
		t.Fatalf("emptying the subscription = %d, want 422\n%s", emptied.Code, emptied.Body)
	}

	typo := h.do(http.MethodPatch, "/v1/developer/webhooks/"+hook.ID, acct.Token,
		map[string]any{"subscribedEvents": []string{"message.delivered", "message.teleported"}})
	if typo.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown event name on update = %d, want 422 — create refuses "+
			"the same value, so it was stored on one verb and refused on the other\n%s",
			typo.Code, typo.Body)
	}

	// And a legitimate change still lands.
	good := h.do(http.MethodPatch, "/v1/developer/webhooks/"+hook.ID, acct.Token,
		map[string]any{"subscribedEvents": []string{"message.failed"}})
	if good.Code != http.StatusOK {
		t.Fatalf("a valid update = %d\n%s", good.Code, good.Body)
	}
}
