package api

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
	"github.com/saeedafri/sms-be/internal/webhook"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// parsePathID turns a path parameter into a uuid. The contract types these as
// plain strings, so a malformed one must be rejected here rather than reaching
// a query that would fail with a database syntax error.
func parsePathID(value string) (uuid.UUID, bool) {
	parsed, err := uuid.Parse(value)
	return parsed, err == nil
}

func toAPIKey(key store.APIKey) gen.ApiKey {
	return gen.ApiKey{
		Id: key.ID, Name: key.Name,
		Environment: gen.Environment(key.Environment),
		Scopes:      key.Scopes, KeyPrefix: key.KeyPrefix,
		Status: gen.ApiKeyStatus(key.Status), LastUsedAt: key.LastUsedAt,
		CreatedAt: key.CreatedAt,
	}
}

func (s *Server) ListApiKeys(ctx context.Context, _ gen.ListApiKeysRequestObject) (gen.ListApiKeysResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	keys, err := store.ListAPIKeys(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.ApiKey, 0, len(keys))
	for _, key := range keys {
		out = append(out, toAPIKey(key))
	}
	return gen.ListApiKeys200JSONResponse(out), nil
}

// CreateApiKey mints a key. The secret is in this response and nowhere else,
// ever again — only its hash is stored, so a lost key must be rotated rather
// than recovered.
func (s *Server) CreateApiKey(ctx context.Context, request gen.CreateApiKeyRequestObject) (gen.CreateApiKeyResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if request.Body.Name == "" {
		return gen.CreateApiKey422JSONResponse(errorBody(codeValidation,
			"A key name is required.")), nil
	}
	key, err := store.CreateAPIKey(ctx, s.DB, identity, request.Body.Name,
		string(request.Body.Environment), request.Body.Scopes)
	if err != nil {
		return nil, err
	}
	return gen.CreateApiKey201JSONResponse(gen.ApiKeyCreated{
		Id: key.ID, Name: key.Name, Environment: gen.Environment(key.Environment),
		Scopes: key.Scopes, KeyPrefix: key.KeyPrefix,
		Status: gen.ApiKeyStatus(key.Status), LastUsedAt: key.LastUsedAt,
		CreatedAt: key.CreatedAt, Secret: key.Secret,
	}), nil
}

func (s *Server) RotateApiKey(ctx context.Context, request gen.RotateApiKeyRequestObject) (gen.RotateApiKeyResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	keyID, ok2 := parsePathID(request.Id)
	if !ok2 {
		return gen.RotateApiKey404JSONResponse(errorBody("not_found", "No such API key.")), nil
	}
	key, err := store.RotateAPIKey(ctx, s.DB, identity, keyID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.RotateApiKey404JSONResponse(errorBody("not_found", "No such API key.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.RotateApiKey200JSONResponse(gen.ApiKeyCreated{
		Id: key.ID, Name: key.Name, Environment: gen.Environment(key.Environment),
		Scopes: key.Scopes, KeyPrefix: key.KeyPrefix,
		Status: gen.ApiKeyStatus(key.Status), LastUsedAt: key.LastUsedAt,
		CreatedAt: key.CreatedAt, Secret: key.Secret,
	}), nil
}

func (s *Server) RevokeApiKey(ctx context.Context, request gen.RevokeApiKeyRequestObject) (gen.RevokeApiKeyResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	keyID, ok2 := parsePathID(request.Id)
	if !ok2 {
		return gen.RevokeApiKey404JSONResponse(errorBody("not_found", "No such API key.")), nil
	}
	err := store.RevokeAPIKey(ctx, s.DB, identity, keyID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.RevokeApiKey404JSONResponse(errorBody("not_found", "No such API key.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.RevokeApiKey204Response{}, nil
}

// ListApiScopes is a static catalogue rather than a table: scopes are defined
// by what this code implements, so storing them would let the list drift from
// what the API actually enforces.
func (s *Server) ListApiScopes(ctx context.Context, _ gen.ListApiScopesRequestObject) (gen.ListApiScopesResponseObject, error) {
	if _, ok := identityFrom(ctx); !ok {
		return nil, errUnauthenticated
	}
	return gen.ListApiScopes200JSONResponse([]gen.ApiScope{
		{Key: "messages.send", Label: "Send messages", Category: "Messaging"},
		{Key: "messages.read", Label: "Read message logs", Category: "Messaging"},
		{Key: "campaigns.write", Label: "Create and launch campaigns", Category: "Messaging"},
		{Key: "contacts.write", Label: "Manage contacts and lists", Category: "Audience"},
		{Key: "contacts.read", Label: "Read contacts and lists", Category: "Audience"},
		{Key: "senders.read", Label: "Read sender IDs and registrations", Category: "Compliance"},
		{Key: "templates.write", Label: "Manage templates", Category: "Compliance"},
		{Key: "wallet.read", Label: "Read balance and ledger", Category: "Billing"},
		{Key: "analytics.read", Label: "Read analytics", Category: "Reporting"},
	}), nil
}

// GetRateLimit reports the tenant's API budget. Live traffic gets the larger
// allowance; test keys are deliberately throttled so a runaway integration
// test cannot consume the production budget.
func (s *Server) GetRateLimit(ctx context.Context, request gen.GetRateLimitRequestObject) (gen.GetRateLimitResponseObject, error) {
	if _, ok := identityFrom(ctx); !ok {
		return nil, errUnauthenticated
	}
	environment := string(request.Params.Environment)
	if environment == "" {
		environment = "live"
	}
	tier := gen.RateLimitTier{
		Environment: gen.Environment(environment),
		TierName:    "standard", RequestsPerSecond: 100, Burst: 200,
	}
	if environment == "test" {
		tier.TierName, tier.RequestsPerSecond, tier.Burst = "test", 10, 20
	}
	return gen.GetRateLimit200JSONResponse(tier), nil
}

func toWebhook(hook store.WebhookEndpoint) gen.WebhookEndpoint {
	events := make([]gen.WebhookEventType, 0, len(hook.SubscribedEvents))
	for _, event := range hook.SubscribedEvents {
		events = append(events, gen.WebhookEventType(event))
	}
	return gen.WebhookEndpoint{
		Id: hook.ID, Environment: gen.Environment(hook.Environment),
		Url: hook.URL, SubscribedEvents: events,
		SigningSecretPrefix: hook.SigningSecretPrefix,
		Status:              gen.WebhookStatus(hook.Status),
	}
}

func (s *Server) ListWebhookEndpoints(ctx context.Context, _ gen.ListWebhookEndpointsRequestObject) (gen.ListWebhookEndpointsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	hooks, err := store.ListWebhooks(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.WebhookEndpoint, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, toWebhook(hook))
	}
	return gen.ListWebhookEndpoints200JSONResponse(out), nil
}

func (s *Server) GetWebhookEndpoint(ctx context.Context, request gen.GetWebhookEndpointRequestObject) (gen.GetWebhookEndpointResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	hooks, err := store.ListWebhooks(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	for _, hook := range hooks {
		if hook.ID.String() == request.Id {
			return gen.GetWebhookEndpoint200JSONResponse(toWebhook(hook)), nil
		}
	}
	return gen.GetWebhookEndpoint404JSONResponse(errorBody("not_found", "No such endpoint.")), nil
}

func (s *Server) CreateWebhookEndpoint(ctx context.Context, request gen.CreateWebhookEndpointRequestObject) (gen.CreateWebhookEndpointResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	// An endpoint we cannot reach over TLS is an endpoint that leaks message
	// content and lets anyone forge delivery reports, so plain http is refused
	// rather than accepted with a warning nobody reads.
	if err := webhook.ValidateURL(request.Body.Url); err != nil {
		return gen.CreateWebhookEndpoint422JSONResponse(errorBody(codeValidation, err.Error())), nil
	}
	events := make([]string, 0, len(request.Body.SubscribedEvents))
	for _, event := range request.Body.SubscribedEvents {
		events = append(events, string(event))
	}
	hook, err := store.CreateWebhook(ctx, s.DB, identity,
		string(request.Body.Environment), request.Body.Url, events)
	if err != nil {
		return nil, err
	}
	created := toWebhook(hook)
	return gen.CreateWebhookEndpoint201JSONResponse(gen.WebhookEndpointCreated{
		Id: created.Id, Environment: created.Environment, Url: created.Url,
		SubscribedEvents: created.SubscribedEvents, Status: created.Status,
		SigningSecretPrefix: created.SigningSecretPrefix,
		SigningSecret:       hook.SigningSecret,
	}), nil
}

func (s *Server) UpdateWebhookEndpoint(ctx context.Context, request gen.UpdateWebhookEndpointRequestObject) (gen.UpdateWebhookEndpointResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	var events []string
	if request.Body.SubscribedEvents != nil {
		events = make([]string, 0, len(*request.Body.SubscribedEvents))
		for _, event := range *request.Body.SubscribedEvents {
			events = append(events, string(event))
		}
	}
	var status *string
	if request.Body.Status != nil {
		value := string(*request.Body.Status)
		status = &value
	}
	hookID, ok2 := parsePathID(request.Id)
	if !ok2 {
		return gen.UpdateWebhookEndpoint404JSONResponse(errorBody("not_found", "No such endpoint.")), nil
	}
	hook, err := store.UpdateWebhook(ctx, s.DB, identity, hookID, nil, events, status)
	if errors.Is(err, store.ErrNotFound) {
		return gen.UpdateWebhookEndpoint404JSONResponse(errorBody("not_found", "No such endpoint.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.UpdateWebhookEndpoint200JSONResponse(toWebhook(hook)), nil
}

func (s *Server) DeleteWebhookEndpoint(ctx context.Context, request gen.DeleteWebhookEndpointRequestObject) (gen.DeleteWebhookEndpointResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	hookID, ok2 := parsePathID(request.Id)
	if !ok2 {
		return gen.DeleteWebhookEndpoint404JSONResponse(errorBody("not_found", "No such endpoint.")), nil
	}
	err := store.DeleteWebhook(ctx, s.DB, identity, hookID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.DeleteWebhookEndpoint404JSONResponse(errorBody("not_found", "No such endpoint.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.DeleteWebhookEndpoint204Response{}, nil
}

func toWebhookEvent(event store.WebhookDelivery) gen.WebhookEvent {
	return gen.WebhookEvent{
		Id: event.ID, EndpointId: event.EndpointID,
		EventType: gen.WebhookEventType(event.EventType),
		Timestamp: event.OccurredAt, Attempt: event.Attempt,
		Outcome:    gen.WebhookEventOutcome(event.Outcome),
		HttpStatus: event.HTTPStatus, ResponseSnippet: event.ResponseSnippet,
	}
}

func (s *Server) ListWebhookEvents(ctx context.Context, request gen.ListWebhookEventsRequestObject) (gen.ListWebhookEventsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	limit := 50
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}
	hookID, ok2 := parsePathID(request.Id)
	if !ok2 {
		return gen.ListWebhookEvents404JSONResponse(errorBody("not_found", "No such endpoint.")), nil
	}
	events, err := store.ListWebhookEvents(ctx, s.DB, identity, hookID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]gen.WebhookEvent, 0, len(events))
	for _, event := range events {
		out = append(out, toWebhookEvent(event))
	}
	return gen.ListWebhookEvents200JSONResponse(gen.WebhookEventPage{Events: out}), nil
}

// SendWebhookTestEvent posts a real signed request to the endpoint.
//
// It is a genuine delivery, not a simulated one: the whole point of a test
// event is to prove the customer's endpoint accepts our signature and returns
// 2xx. Faking a success here would make the feature actively harmful.
func (s *Server) SendWebhookTestEvent(ctx context.Context, request gen.SendWebhookTestEventRequestObject) (gen.SendWebhookTestEventResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	hooks, err := store.ListWebhooks(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	for _, hook := range hooks {
		if hook.ID.String() != request.Id {
			continue
		}
		delivery := webhook.Deliver(ctx, hook.URL, "message.delivered",
			webhook.SamplePayload(), hook.SigningSecretPrefix)
		recorded, err := store.RecordWebhookEvent(ctx, s.DB, identity, store.WebhookDelivery{
			EndpointID: hook.ID, EventType: "message.delivered", Attempt: 1,
			Outcome: delivery.Outcome, HTTPStatus: delivery.HTTPStatus,
			ResponseSnippet: delivery.ResponseSnippet, Payload: delivery.Payload,
		})
		if err != nil {
			return nil, err
		}
		return gen.SendWebhookTestEvent201JSONResponse(toWebhookEvent(recorded)), nil
	}
	return gen.SendWebhookTestEvent404JSONResponse(errorBody("not_found", "No such endpoint.")), nil
}

// ResendWebhookEvent replays a stored payload as a new attempt.
//
// It records a new row rather than mutating the original: the delivery log is
// the evidence a customer uses to argue about what we sent and when, and
// overwriting the failed attempt would erase exactly the fact under dispute.
func (s *Server) ResendWebhookEvent(ctx context.Context, request gen.ResendWebhookEventRequestObject) (gen.ResendWebhookEventResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	eventID, ok2 := parsePathID(request.EventId)
	if !ok2 {
		return gen.ResendWebhookEvent404JSONResponse(errorBody("not_found", "No such event.")), nil
	}
	original, err := store.GetWebhookEvent(ctx, s.DB, identity, eventID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.ResendWebhookEvent404JSONResponse(errorBody("not_found", "No such event.")), nil
	}
	if err != nil {
		return nil, err
	}
	hooks, err := store.ListWebhooks(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	for _, hook := range hooks {
		if hook.ID != original.EndpointID {
			continue
		}
		delivery := webhook.Deliver(ctx, hook.URL, original.EventType,
			original.Payload, hook.SigningSecretPrefix)
		recorded, err := store.RecordWebhookEvent(ctx, s.DB, identity, store.WebhookDelivery{
			EndpointID: hook.ID, EventType: original.EventType,
			Attempt: original.Attempt + 1, Outcome: delivery.Outcome,
			HTTPStatus: delivery.HTTPStatus, ResponseSnippet: delivery.ResponseSnippet,
			Payload: original.Payload,
		})
		if err != nil {
			return nil, err
		}
		return gen.ResendWebhookEvent201JSONResponse(toWebhookEvent(recorded)), nil
	}
	return gen.ResendWebhookEvent404JSONResponse(errorBody("not_found", "No such endpoint.")), nil
}

func (s *Server) ListIpAllowlist(ctx context.Context, _ gen.ListIpAllowlistRequestObject) (gen.ListIpAllowlistResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	entries, err := store.ListIPAllowlist(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.IpAllowlistEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, gen.IpAllowlistEntry{
			Id: entry.ID, Environment: gen.Environment(entry.Environment),
			Cidr: entry.CIDR, Label: entry.Label, CreatedAt: entry.CreatedAt,
		})
	}
	return gen.ListIpAllowlist200JSONResponse(out), nil
}

func (s *Server) AddIpAllowlistEntry(ctx context.Context, request gen.AddIpAllowlistEntryRequestObject) (gen.AddIpAllowlistEntryResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	// A malformed CIDR would silently allow nothing, locking the tenant out of
	// their own API with no indication why.
	if err := webhook.ValidateCIDR(request.Body.Cidr); err != nil {
		return gen.AddIpAllowlistEntry422JSONResponse(errorBody(codeValidation, err.Error())), nil
	}
	entry, err := store.AddIPAllowEntry(ctx, s.DB, identity,
		string(request.Body.Environment), request.Body.Cidr, request.Body.Label)
	if err != nil {
		return nil, err
	}
	return gen.AddIpAllowlistEntry201JSONResponse(gen.IpAllowlistEntry{
		Id: entry.ID, Environment: gen.Environment(entry.Environment),
		Cidr: entry.CIDR, Label: entry.Label, CreatedAt: entry.CreatedAt,
	}), nil
}
