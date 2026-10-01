package api

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
	"github.com/saeedafri/sms-be/internal/webhook"
)

// Native webhook integrations: an endpoint that is a customer's WebEngage,
// MoEngage or CleverTap account receives events in that product's format.
// Mounted directly until the contract declares them; the endpoint itself is
// still created through the ordinary webhooks routes.
func (s *Server) mountWebhookIntegrationRoutes(r chi.Router) {
	r.Get("/v1/developer/webhooks/{id}/integration", s.getWebhookIntegration)
	r.Put("/v1/developer/webhooks/{id}/integration", s.putWebhookIntegration)
}

var headerName = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

const maxIntegrationHeaders = 10

// webhookIntegrationBody never carries a header value: they are credentials.
func webhookIntegrationBody(kind string, names []string) map[string]any {
	sort.Strings(names)
	if names == nil {
		names = []string{}
	}
	return map[string]any{"type": kind, "headerNames": names,
		"supported": append([]string{"default"}, webhook.Integrations...)}
}

func (s *Server) integrationEndpoint(w http.ResponseWriter, r *http.Request) (store.Identity, uuid.UUID, bool) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Missing or invalid bearer token")
		return identity, uuid.Nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, "id must be a uuid.")
		return identity, uuid.Nil, false
	}
	hooks, err := store.ListWebhooks(r.Context(), s.DB, identity, nil)
	if err != nil {
		s.Logger.Error("list webhooks", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return identity, uuid.Nil, false
	}
	for _, hook := range hooks {
		if hook.ID == id {
			return identity, id, true
		}
	}
	writeError(w, http.StatusNotFound, codeNotFound, "No such webhook endpoint.")
	return identity, uuid.Nil, false
}

func (s *Server) getWebhookIntegration(w http.ResponseWriter, r *http.Request) {
	identity, id, ok := s.integrationEndpoint(w, r)
	if !ok {
		return
	}
	integration, found, err := store.GetWebhookIntegration(r.Context(), s.DB, identity, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	if !found {
		writeJSON(w, http.StatusOK, webhookIntegrationBody("default", nil))
		return
	}
	writeJSON(w, http.StatusOK, webhookIntegrationBody(integration.Type, s.headerNames(integration)))
}

func (s *Server) headerNames(integration store.WebhookIntegration) []string {
	headers := s.integrationHeaders(integration)
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	return names
}

func (s *Server) integrationHeaders(integration store.WebhookIntegration) map[string]string {
	if integration.SealedHeaders == nil || s.Secrets == nil {
		return nil
	}
	plain, err := s.Secrets.Decrypt(*integration.SealedHeaders)
	if err != nil {
		return nil
	}
	var headers map[string]string
	_ = json.Unmarshal([]byte(plain), &headers)
	return headers
}

func (s *Server) putWebhookIntegration(w http.ResponseWriter, r *http.Request) {
	identity, id, ok := s.integrationEndpoint(w, r)
	if !ok {
		return
	}
	if !canManageSettings(identity.Role) {
		writeError(w, http.StatusForbidden, codeForbidden, "Member role cannot change webhook integrations.")
		return
	}
	var body struct {
		Type    string            `json:"type"`
		Headers map[string]string `json:"headers"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	if body.Type == "default" || body.Type == "" {
		if len(body.Headers) > 0 {
			writeError(w, http.StatusUnprocessableEntity, codeValidation,
				"A plain Relay webhook takes no custom headers.")
			return
		}
		if err := store.ClearWebhookIntegration(r.Context(), s.DB, identity, id); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
			return
		}
		writeJSON(w, http.StatusOK, webhookIntegrationBody("default", nil))
		return
	}
	if !slices.Contains(webhook.Integrations, body.Type) {
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			"type must be one of: default, "+strings.Join(webhook.Integrations, ", ")+".")
		return
	}
	if problem := checkIntegrationHeaders(body.Headers); problem != "" {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, problem)
		return
	}
	raw, _ := json.Marshal(body.Headers)
	sealed, err := s.Secrets.Encrypt(string(raw))
	if err != nil {
		s.Logger.Error("seal integration headers", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	if err := store.SetWebhookIntegration(r.Context(), s.DB, identity,
		store.WebhookIntegration{EndpointID: id, Type: body.Type, SealedHeaders: &sealed}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "an unexpected error occurred")
		return
	}
	names := make([]string, 0, len(body.Headers))
	for name := range body.Headers {
		names = append(names, name)
	}
	writeJSON(w, http.StatusOK, webhookIntegrationBody(body.Type, names))
}

func checkIntegrationHeaders(headers map[string]string) string {
	if len(headers) == 0 {
		return "headers must carry at least your vendor API key."
	}
	if len(headers) > maxIntegrationHeaders {
		return "headers holds at most 10 entries."
	}
	for name, value := range headers {
		switch {
		case !headerName.MatchString(name):
			return name + " is not a valid header name."
		case webhook.ReservedHeader(name):
			return name + " is set by Relay and cannot be overridden."
		case value == "" || len(value) > 2000 || strings.ContainsAny(value, "\r\n"):
			return "the value of " + name + " must be 1-2000 characters on a single line."
		}
	}
	return ""
}

// vendorRequest is what deliverWebhook sends for an endpoint with an
// integration: the transformed body and the customer's headers. A payload that
// cannot be transformed is reported rather than sent in the wrong shape.
func (s *Server) vendorRequest(ctx context.Context, identity store.Identity,
	hook store.WebhookEndpoint, payload []byte) (body []byte, headers map[string]string, note string) {

	integration, found, err := store.GetWebhookIntegration(ctx, s.DB, identity, hook.ID)
	if err != nil {
		return nil, nil, "Not sent: the endpoint's integration could not be read."
	}
	if !found {
		return payload, nil, ""
	}
	body, err = webhook.Transform(integration.Type, payload, time.Now())
	if err != nil {
		return nil, nil, "Not sent: " + err.Error() + "."
	}
	return body, s.integrationHeaders(integration), ""
}
