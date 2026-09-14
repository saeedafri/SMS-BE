package api

import (
	"context"
	"encoding/json"
	"time"

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
		recorded, err := s.deliverWebhook(ctx, identity, hook, eventType, payload, 1)
		if err != nil {
			s.Logger.Warn("webhook delivery not recorded",
				"event", eventType, "endpoint", hook.ID, "error", err)
			continue
		}
		if recorded.Outcome == "failed" && hook.SealedSecret != nil {
			go s.retryWebhook(identity, hook, eventType, payload)
		}
	}
}

// defaultWebhookRetryDelays space the retries after a failed first attempt.
// ponytail: retries live in this process and are lost on restart; move them to
// a queue table when a lost retry matters.
var defaultWebhookRetryDelays = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

// retryWebhook re-attempts a failed delivery on the retry schedule until one
// succeeds, recording every attempt in the delivery log.
func (s *Server) retryWebhook(identity store.Identity, hook store.WebhookEndpoint,
	eventType string, payload []byte) {

	delays := s.WebhookRetryDelays
	if delays == nil {
		delays = defaultWebhookRetryDelays
	}
	for i, delay := range delays {
		time.Sleep(delay)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		recorded, err := s.deliverWebhook(ctx, identity, hook, eventType, payload, i+2)
		cancel()
		if err == nil && recorded.Outcome == "succeeded" {
			return
		}
	}
}

// missingSecretNote is what the delivery log says for an endpoint whose secret
// was never stored.
const missingSecretNote = "Not sent: this endpoint was created before signing secrets were stored, " +
	"so its events cannot be signed with the secret you were shown. Recreate the endpoint."

// deliverWebhook signs payload with the endpoint's own secret, posts it, and
// records the attempt. An endpoint with no stored secret is recorded as failed
// and not contacted: signing with anything else produces a signature the
// customer cannot verify.
func (s *Server) deliverWebhook(ctx context.Context, identity store.Identity,
	hook store.WebhookEndpoint, eventType string, payload []byte, attempt int) (store.WebhookDelivery, error) {

	var delivery webhook.Result
	secret := ""
	if hook.SealedSecret != nil {
		var err error
		if secret, err = s.Secrets.Decrypt(*hook.SealedSecret); err != nil {
			s.Logger.Error("webhook secret unreadable", "endpoint", hook.ID, "error", err)
			secret = ""
		}
	}
	if secret == "" {
		note := missingSecretNote
		delivery = webhook.Result{Outcome: "failed", ResponseSnippet: &note, Payload: payload}
	} else {
		delivery = webhook.Deliver(ctx, hook.URL, eventType, payload, secret)
	}
	return store.RecordWebhookEvent(ctx, s.DB, identity, store.WebhookDelivery{
		EndpointID: hook.ID, EventType: eventType, Attempt: attempt,
		Outcome: delivery.Outcome, HTTPStatus: delivery.HTTPStatus,
		ResponseSnippet: delivery.ResponseSnippet, Payload: payload,
	})
}

// MessageSettled tells the tenant's webhooks a message reached a final state.
// Runs off the settle path: a slow customer endpoint must not hold receipts up.
func (s *Server) MessageSettled(_ context.Context, identity store.Identity, record store.MessageRecord) {
	event := "message.failed"
	if record.Status == "delivered" {
		event = "message.delivered"
	}
	data := map[string]any{
		"messageId": record.ID, "status": record.Status, "channel": record.Channel,
		"msisdn": record.Msisdn, "segments": record.Segments,
		"costMinor": record.CostMinor, "currency": record.Currency,
		"errorCode": record.ErrorCode, "errorClass": record.ErrorClass,
		"deliveredAt": record.DeliveredAt, "updatedAt": record.UpdatedAt,
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.emitWebhookEvent(ctx, identity, event, data)
	}()
}
