package api

import (
	"context"
	"encoding/json"

	"github.com/saeedafri/sms-be/internal/store"
	"github.com/saeedafri/sms-be/internal/webhook"
)

// emitWebhookEvent delivers one event to every active endpoint subscribed to it
// and records each attempt in the event log.
//
// Delivery is best-effort and never fails the action that produced it: an
// inbound message that arrived is a fact, and a customer's endpoint being down
// must not turn it into a 500 for the carrier that delivered it. Every attempt
// is recorded either way, so a customer debugging a missed event can see that
// we tried and what came back.
//
// Endpoints that did not subscribe to this event are skipped rather than sent
// everything — a subscription list nothing honours is a setting that lies.
func (s *Server) emitWebhookEvent(ctx context.Context, identity store.Identity,
	eventType string, data any) {

	hooks, err := store.ListWebhooks(ctx, s.DB, identity, nil)
	if err != nil {
		s.Logger.Warn("webhook fan-out skipped — endpoints unreadable",
			"event", eventType, "error", err)
		return
	}
	payload, err := json.Marshal(map[string]any{"event": eventType, "data": data})
	if err != nil {
		s.Logger.Error("webhook payload could not be encoded",
			"event", eventType, "error", err)
		return
	}

	for _, hook := range hooks {
		// "enabled"/"disabled" are the stored values — there is no "active".
		// Comparing against the wrong word skipped every endpoint and the
		// fan-out silently delivered nothing.
		if hook.Status != "enabled" || !oneOf(eventType, hook.SubscribedEvents) {
			continue
		}
		delivery := webhook.Deliver(ctx, hook.URL, eventType, payload, hook.SigningSecretPrefix)
		if _, err := store.RecordWebhookEvent(ctx, s.DB, identity, store.WebhookDelivery{
			EndpointID: hook.ID, EventType: eventType, Attempt: 1,
			Outcome: delivery.Outcome, HTTPStatus: delivery.HTTPStatus,
			ResponseSnippet: delivery.ResponseSnippet, Payload: delivery.Payload,
		}); err != nil {
			s.Logger.Warn("webhook delivery not recorded",
				"event", eventType, "endpoint", hook.ID, "error", err)
		}
	}
}
