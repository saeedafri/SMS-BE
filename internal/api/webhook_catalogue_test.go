package api_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/gen/api"
)

// allWebhookEvents is the catalogue in lifecycle order, exactly as the contract
// declares it.
var allWebhookEvents = []string{
	"message.inbound", "message.queued", "message.sent", "message.delivered",
	"message.read", "message.failed",
	"campaign.completed", "campaign.failed",
	"sender.approved", "sender.rejected",
	"template.approved", "template.rejected",
	"wallet.low_balance", "wallet.depleted",
}

// Every one of the fourteen can be subscribed to, in one endpoint.
//
// The UI offers all of them, so an event the backend silently rejected — or
// accepted and never recognised — would be a checkbox that does nothing.
func TestEveryEventInTheCatalogueCanBeSubscribedTo(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPost, "/v1/developer/webhooks", acct.Token, map[string]any{
		"url": "https://example.test/hook", "environment": "test",
		"subscribedEvents": allWebhookEvents,
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("subscribing to all 14 = %d, want 201; body = %s", res.Code, res.Body)
	}
	var created gen.WebhookEndpointCreated
	res.decode(t, &created)
	if len(created.SubscribedEvents) != len(allWebhookEvents) {
		t.Fatalf("stored %d events, want %d", len(created.SubscribedEvents), len(allWebhookEvents))
	}
	// The `approved` spelling is deliberate: the customer-facing word is now
	// "Verified", but renaming the enum value would break stored subscriptions.
	stored := map[string]bool{}
	for _, event := range created.SubscribedEvents {
		stored[string(event)] = true
	}
	for _, want := range []string{"sender.approved", "template.approved", "message.inbound"} {
		if !stored[want] {
			t.Errorf("%q did not survive the round trip", want)
		}
	}
}

// A typo'd event is a subscription that silently never fires, which from the
// customer's side is indistinguishable from a broken integration.
func TestAnUnknownWebhookEventIsRefused(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPost, "/v1/developer/webhooks", acct.Token, map[string]any{
		"url": "https://example.test/hook", "environment": "test",
		"subscribedEvents": []string{"message.delivered", "message.delivred"},
	})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a misspelled event = %d, want 422; body = %s", res.Code, res.Body)
	}
}

// An inbound message fans out to the endpoints subscribed to it, and to no
// others.
//
// Both halves matter. Without the first, every reply and every STOP is
// invisible to anything outside our own dashboard. Without the second, the
// subscription list is a setting that lies.
//
// Asserted on the event log rather than on a live receiver: delivery to an
// unreachable endpoint still records the attempt, which is the behaviour a
// customer debugging a missed event depends on.
func TestAnInboundMessageIsDeliveredOnlyToSubscribers(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	subscribe := func(events []string) uuid.UUID {
		t.Helper()
		res := h.do(http.MethodPost, "/v1/developer/webhooks", acct.Token, map[string]any{
			"url":              "https://" + uuid.NewString()[:8] + ".invalid/hook",
			"environment":      "test",
			"subscribedEvents": events,
		})
		if res.Code != http.StatusCreated {
			t.Fatalf("create webhook = %d: %s", res.Code, res.Body)
		}
		var created gen.WebhookEndpointCreated
		res.decode(t, &created)
		return created.Id
	}
	wantsInbound := subscribe([]string{"message.inbound", "message.failed"})
	wantsDelivered := subscribe([]string{"message.delivered"})

	contact := newContact(t, h, acct.TenantID, "+91982000"+uuid.NewString()[:4])
	inbound := h.do(http.MethodPost, "/v1/dev/receive-inbound-message", acct.Token,
		map[string]any{"contactId": contact.String(), "channel": "SMS", "body": "STOP"})
	if inbound.Code != http.StatusCreated {
		t.Fatalf("inbound = %d: %s", inbound.Code, inbound.Body)
	}

	eventsFor := func(id uuid.UUID) []gen.WebhookEvent {
		t.Helper()
		res := h.do(http.MethodGet, "/v1/developer/webhooks/"+id.String()+"/events",
			acct.Token, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("read events = %d: %s", res.Code, res.Body)
		}
		var page gen.WebhookEventPage
		res.decode(t, &page)
		return page.Events
	}

	subscribed := eventsFor(wantsInbound)
	if len(subscribed) == 0 {
		t.Fatal("an inbound message produced no event on a subscribed endpoint — " +
			"every reply and every STOP stays invisible outside our own dashboard")
	}
	if string(subscribed[0].EventType) != "message.inbound" {
		t.Errorf("event type = %q, want message.inbound", subscribed[0].EventType)
	}
	// The payload is not on the contract's WebhookEvent, so it is read from the
	// log directly — it is what the customer's endpoint actually received.
	var payload string
	if err := h.admin.QueryRow(t.Context(),
		`SELECT payload::text FROM webhook_events WHERE id = $1`,
		subscribed[0].Id).Scan(&payload); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if !contains(payload, "STOP") {
		t.Errorf("payload does not carry the text that arrived: %s", payload)
	}
	if !contains(payload, "messageId") {
		t.Errorf("payload carries no message id, so a reply cannot reference it: %s", payload)
	}
	if !contains(payload, "keywordMatched") {
		t.Errorf("payload does not say the STOP was recognised as an opt-out: %s", payload)
	}

	if others := eventsFor(wantsDelivered); len(others) != 0 {
		t.Errorf("an endpoint subscribed only to message.delivered received %d event(s)",
			len(others))
	}
}

// newContact inserts one contact directly. The inbound hook needs a contact id
// and the import endpoint is a heavier path than this test is about.
func newContact(t *testing.T, h *harness, tenantID uuid.UUID, msisdn string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := h.admin.QueryRow(t.Context(), `
		INSERT INTO contacts (tenant_id, msisdn, country, fields, consent)
		VALUES ($1, $2, 'IN', '{}'::jsonb, $3::jsonb) RETURNING id`,
		tenantID, msisdn, `{"sms":true}`).Scan(&id); err != nil {
		t.Fatalf("insert contact: %v", err)
	}
	return id
}
