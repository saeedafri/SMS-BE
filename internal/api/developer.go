package api

import (
	"context"
	"errors"
	"fmt"

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
		Scopes:      toContractScopes(key.Scopes), KeyPrefix: key.KeyPrefix,
		Status: gen.ApiKeyStatus(key.Status), LastUsedAt: key.LastUsedAt,
		CreatedAt: key.CreatedAt,
	}
}

func (s *Server) ListApiKeys(ctx context.Context, request gen.ListApiKeysRequestObject) (gen.ListApiKeysResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	keys, err := store.ListAPIKeys(ctx, s.DB, identity, string(request.Params.Environment))
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
	// PRIVILEGE ESCALATION. Every mutating route in this file was authenticated
	// and nothing more, so a MEMBER — the lowest role, whom the frontend hides
	// the entire Developer section from — could mint a live API key with
	// messages:write and be handed the secret. Verified against the deployed
	// server, not inferred: sam@acme.test (role=member) got 201 and a working
	// sk_live_ key, and separately created a webhook pointing at an external URL
	// that would have received every delivery event for the tenant.
	//
	// The frontend gate was the only thing in the way, and a frontend gate is
	// not an authorization boundary — it stops a button being drawn, not a
	// request being made. The contract already declared a 403 on all of these,
	// so the guard was intended and simply never written.
	//
	// An API key also outlives the check: once issued it authenticates on its
	// own, so a member who obtained one keeps that reach regardless of role.
	if !canManageSettings(identity.Role) {
		return gen.CreateApiKey403JSONResponse(
			errorBody(codeForbidden, "Member role cannot create API keys.")), nil
	}
	if request.Body.Name == "" {
		return gen.CreateApiKey422JSONResponse(errorBody(codeValidation,
			"A key name is required.")), nil
	}
	if !oneOf(string(request.Body.Environment), validEnvironments) {
		return gen.CreateApiKey422JSONResponse(errorBody(codeValidation,
			enumMessage("Environment", validEnvironments))), nil
	}
	// A scope outside the catalogue was accepted, stored verbatim and echoed
	// back on every later read. "messages:write" is not a scope — it does not
	// appear in the list this same service publishes two paths away — and a key
	// holding it is a key whose printed permissions mean nothing, because
	// nothing will ever match it.
	if bad, ok := knownScopes(request.Body.Scopes); !ok {
		return gen.CreateApiKey422JSONResponse(errorBody(codeValidation,
			fmt.Sprintf("%q is not a scope. Scopes must be one of: %s.",
				bad, scopeVocabulary()))), nil
	}
	key, err := store.CreateAPIKey(ctx, s.DB, identity, request.Body.Name,
		string(request.Body.Environment), toStoredScopes(request.Body.Scopes))
	if err != nil {
		return nil, err
	}
	s.recordActivity(ctx, identity, store.ActivityAPIKeyCreate,
		fmt.Sprintf("Created %s key %q", key.Environment, key.Name))
	return gen.CreateApiKey201JSONResponse(gen.ApiKeyCreated{
		Id: key.ID, Name: key.Name, Environment: gen.Environment(key.Environment),
		Scopes: toContractScopes(key.Scopes), KeyPrefix: key.KeyPrefix,
		Status: gen.ApiKeyStatus(key.Status), LastUsedAt: key.LastUsedAt,
		CreatedAt: key.CreatedAt, Secret: key.Secret,
	}), nil
}

func (s *Server) RotateApiKey(ctx context.Context, request gen.RotateApiKeyRequestObject) (gen.RotateApiKeyResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return gen.RotateApiKey403JSONResponse(
			errorBody(codeForbidden, "Member role cannot rotate API keys.")), nil
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
	s.recordActivity(ctx, identity, store.ActivityAPIKeyRotate,
		fmt.Sprintf("Rotated %s key %q", key.Environment, key.Name))
	return gen.RotateApiKey200JSONResponse(gen.ApiKeyCreated{
		Id: key.ID, Name: key.Name, Environment: gen.Environment(key.Environment),
		Scopes: toContractScopes(key.Scopes), KeyPrefix: key.KeyPrefix,
		Status: gen.ApiKeyStatus(key.Status), LastUsedAt: key.LastUsedAt,
		CreatedAt: key.CreatedAt, Secret: key.Secret,
	}), nil
}

func (s *Server) RevokeApiKey(ctx context.Context, request gen.RevokeApiKeyRequestObject) (gen.RevokeApiKeyResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return gen.RevokeApiKey403JSONResponse(
			errorBody(codeForbidden, "Member role cannot revoke API keys.")), nil
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
	s.recordActivity(ctx, identity, store.ActivityAPIKeyRevoke, "Revoked an API key")
	return gen.RevokeApiKey204Response{}, nil
}

// ListApiScopes is a static catalogue rather than a table: scopes are defined
// by what this code implements, so storing them would let the list drift from
// what the API actually enforces.
func (s *Server) ListApiScopes(ctx context.Context, _ gen.ListApiScopesRequestObject) (gen.ListApiScopesResponseObject, error) {
	if _, ok := identityFrom(ctx); !ok {
		return nil, errUnauthenticated
	}
	// Keys and labels are the frontend's registry verbatim
	// (../SMS-UI/src/lib/registries/scopes.ts).
	//
	// The contract does not enumerate scope strings, so both sides invented
	// their own vocabulary and they did not match: we offered "messages.send"
	// where the dashboard's key-creation form renders "send:sms". The form
	// listed our labels but its own copy, tests and stored keys all speak the
	// other set, so a key created through the UI carried scopes the API would
	// not recognise. One vocabulary, and it is theirs, because a scope string
	// is part of the public API surface customers paste into their own code.
	return gen.ListApiScopes200JSONResponse(apiScopeCatalogue), nil
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

func (s *Server) ListWebhookEndpoints(ctx context.Context, request gen.ListWebhookEndpointsRequestObject) (gen.ListWebhookEndpointsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	environment := string(request.Params.Environment)
	hooks, err := store.ListWebhooks(ctx, s.DB, identity, &environment)
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
	hooks, err := store.ListWebhooks(ctx, s.DB, identity, nil)
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
	if !canManageSettings(identity.Role) {
		return gen.CreateWebhookEndpoint403JSONResponse(
			errorBody(codeForbidden, "Member role cannot create webhook endpoints.")), nil
	}
	// An endpoint we cannot reach over TLS is an endpoint that leaks message
	// content and lets anyone forge delivery reports, so plain http is refused
	// rather than accepted with a warning nobody reads.
	if err := webhook.ValidateURL(request.Body.Url); err != nil {
		return gen.CreateWebhookEndpoint422JSONResponse(errorBody(codeValidation, err.Error())), nil
	}
	// A webhook subscribed to nothing is worse than a rejected one. It presents
	// as enabled, mints a signing secret, and is permanently silent — and the
	// customer has nothing on the screen or in the response telling them why
	// their integration never fires.
	//
	// The field is required in the contract, but an omitted array decodes to an
	// empty one rather than to an error, so the requiredness has to be checked
	// here. Environment, on this same handler, was already validated; this is
	// the sibling field that was not.
	if len(request.Body.SubscribedEvents) == 0 {
		return gen.CreateWebhookEndpoint422JSONResponse(errorBody(codeValidation,
			"At least one subscribed event is required. "+
				enumMessage("SubscribedEvents", validWebhookEvents))), nil
	}
	// A typo'd event name is a subscription that silently never fires, which
	// looks identical to a broken integration from the customer's side.
	events := make([]string, 0, len(request.Body.SubscribedEvents))
	for _, event := range request.Body.SubscribedEvents {
		if !oneOf(string(event), validWebhookEvents) {
			return gen.CreateWebhookEndpoint422JSONResponse(errorBody(codeValidation,
				enumMessage("SubscribedEvents", validWebhookEvents))), nil
		}
		events = append(events, string(event))
	}
	if !oneOf(string(request.Body.Environment), validEnvironments) {
		return gen.CreateWebhookEndpoint422JSONResponse(errorBody(codeValidation,
			enumMessage("Environment", validEnvironments))), nil
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
	if !canManageSettings(identity.Role) {
		return gen.UpdateWebhookEndpoint403JSONResponse(
			errorBody(codeForbidden, "Member role cannot change webhook endpoints.")), nil
	}
	// The same two rules create applies, because a webhook can be silenced by
	// editing it just as easily as by creating it wrong. The pointer is what
	// separates "not supplied" — leave the subscription alone — from "supplied
	// as empty", which is a request to make this endpoint permanently silent.
	var events []string
	if request.Body.SubscribedEvents != nil {
		if len(*request.Body.SubscribedEvents) == 0 {
			return gen.UpdateWebhookEndpoint422JSONResponse(errorBody(codeValidation,
				"At least one subscribed event is required. "+
					enumMessage("SubscribedEvents", validWebhookEvents))), nil
		}
		events = make([]string, 0, len(*request.Body.SubscribedEvents))
		for _, event := range *request.Body.SubscribedEvents {
			// Create rejected a typo'd event name and this did not, so the same
			// unknown value was refused on one verb and stored on the other.
			if !oneOf(string(event), validWebhookEvents) {
				return gen.UpdateWebhookEndpoint422JSONResponse(errorBody(codeValidation,
					enumMessage("SubscribedEvents", validWebhookEvents))), nil
			}
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
	if !canManageSettings(identity.Role) {
		return gen.DeleteWebhookEndpoint403JSONResponse(
			errorBody(codeForbidden, "Member role cannot delete webhook endpoints.")), nil
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
	page, ok2 := pageNumber(request.Params.Page)
	if !ok2 {
		return gen.ListWebhookEvents422JSONResponse(
			errorBody(codeValidation, pageTooLow)), nil
	}
	limit := 50
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}
	hookID, ok3 := parsePathID(request.Id)
	if !ok3 {
		return gen.ListWebhookEvents404JSONResponse(errorBody("not_found", "No such endpoint.")), nil
	}
	events, total, err := store.ListWebhookEvents(ctx, s.DB, identity, hookID, page, limit)
	if err != nil {
		return nil, err
	}
	out := make([]gen.WebhookEvent, 0, len(events))
	for _, event := range events {
		out = append(out, toWebhookEvent(event))
	}
	return gen.ListWebhookEvents200JSONResponse(
		gen.WebhookEventPage{Events: out, Total: total}), nil
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
	hooks, err := store.ListWebhooks(ctx, s.DB, identity, nil)
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
	hooks, err := store.ListWebhooks(ctx, s.DB, identity, nil)
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

func (s *Server) ListIpAllowlist(ctx context.Context, request gen.ListIpAllowlistRequestObject) (gen.ListIpAllowlistResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	entries, err := store.ListIPAllowlist(ctx, s.DB, identity, string(request.Params.Environment))
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
	if !canManageSettings(identity.Role) {
		return gen.AddIpAllowlistEntry403JSONResponse(
			errorBody(codeForbidden, "Member role cannot change the IP allowlist.")), nil
	}
	// A malformed CIDR would silently allow nothing, locking the tenant out of
	// their own API with no indication why.
	if err := webhook.ValidateCIDR(request.Body.Cidr); err != nil {
		return gen.AddIpAllowlistEntry422JSONResponse(errorBody(codeValidation, err.Error())), nil
	}
	if !oneOf(string(request.Body.Environment), validEnvironments) {
		return gen.AddIpAllowlistEntry422JSONResponse(errorBody(codeValidation,
			enumMessage("Environment", validEnvironments))), nil
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

// RemoveIpAllowlistEntry deletes one allowed source range.
//
// Deleting the last entry is allowed and means "no IP restriction" — an empty
// allowlist is not a lockout. The alternative, refusing to remove the final
// entry, would strand a tenant who added the wrong range from an address they
// no longer have.
func (s *Server) RemoveIpAllowlistEntry(ctx context.Context, request gen.RemoveIpAllowlistEntryRequestObject) (gen.RemoveIpAllowlistEntryResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !canManageSettings(identity.Role) {
		return gen.RemoveIpAllowlistEntry403JSONResponse(
			errorBody(codeForbidden, "Member role cannot change the IP allowlist.")), nil
	}
	entryID, valid := parsePathID(request.Id)
	if !valid {
		return gen.RemoveIpAllowlistEntry404JSONResponse(
			errorBody(codeNotFound, "No such allowlist entry.")), nil
	}
	err := store.DeleteIPAllowEntry(ctx, s.DB, identity, entryID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.RemoveIpAllowlistEntry404JSONResponse(
			errorBody(codeNotFound, "No such allowlist entry.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.RemoveIpAllowlistEntry204Response{}, nil
}
