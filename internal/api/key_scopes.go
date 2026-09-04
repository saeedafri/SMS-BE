package api

import (
	"fmt"
	"net/http"
	"strings"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// apiScopeCatalogue is the whole vocabulary of API key scopes.
//
// One list, read by three things: the catalogue endpoint that publishes it, the
// validation that refuses anything outside it, and the route table below that
// decides what each one authorises. They were not one list before, and the
// consequence was a service that published six scopes two paths away from an
// endpoint that accepted "messages:write" and stored it verbatim.
var apiScopeCatalogue = []gen.ApiScope{
	{Key: "send:sms", Label: "Send SMS", Category: "Send"},
	{Key: "send:rcs", Label: "Send RCS", Category: "Send"},
	{Key: "read:messages", Label: "Read message status", Category: "Read"},
	{Key: "read:analytics", Label: "Read analytics", Category: "Read"},
	{Key: "read:logs", Label: "Read message logs", Category: "Read"},
	{Key: "webhooks:manage", Label: "Manage webhooks", Category: "Manage"},
}

// knownScopes reports whether every scope is one we publish, and names the
// first that is not.
func knownScopes(scopes []string) (bad string, ok bool) {
	for _, scope := range scopes {
		found := false
		for _, known := range apiScopeCatalogue {
			if known.Key == scope {
				found = true
				break
			}
		}
		if !found {
			return scope, false
		}
	}
	return "", true
}

// scopeVocabulary renders the catalogue for an error message, in the style the
// enum validations elsewhere use.
func scopeVocabulary() string {
	keys := make([]string, 0, len(apiScopeCatalogue))
	for _, scope := range apiScopeCatalogue {
		keys = append(keys, scope.Key)
	}
	return strings.Join(keys, ", ")
}

// scopeCheckedByHandler marks a route an API key may call where the scope
// cannot be known from the path. POST /v1/messages is the only one: which of
// send:sms and send:rcs applies depends on the SENDER's channel, which is a
// database read the middleware has no business doing.
const scopeCheckedByHandler = ""

// keyRoutes is the whole policy: which routes accept an API key, and which
// scope each requires.
//
// It is a table rather than a check scattered across handlers on purpose. A key
// carries scopes instead of a role, and every handler that does not know that
// is a handler that would treat a send-only key as a full session. Keeping the
// decision in one place means the answer to "what can this key reach" is this
// list, and reviewing it is reviewing the whole surface.
//
// Absent from this table means an API key is not a credential there at all —
// the request proceeds unauthenticated and the handler answers 401. That is the
// deliberate default: the team roster, billing history and tenant settings are
// session-only, and they stay that way until someone adds a scope for them.
var keyRoutes = map[string]string{
	"POST /v1/messages": scopeCheckedByHandler,

	"GET /v1/messages": "read:messages",

	"GET /v1/campaigns":               "read:logs",
	"GET /v1/campaigns/{id}":          "read:logs",
	"GET /v1/campaigns/{id}/messages": "read:logs",

	"GET /v1/analytics":              "read:analytics",
	"GET /v1/analytics/reports":      "read:analytics",
	"GET /v1/analytics/reports/{id}": "read:analytics",

	"GET /v1/developer/webhooks":                          "webhooks:manage",
	"POST /v1/developer/webhooks":                         "webhooks:manage",
	"GET /v1/developer/webhooks/{id}":                     "webhooks:manage",
	"PATCH /v1/developer/webhooks/{id}":                   "webhooks:manage",
	"DELETE /v1/developer/webhooks/{id}":                  "webhooks:manage",
	"GET /v1/developer/webhooks/{id}/events":              "webhooks:manage",
	"POST /v1/developer/webhooks/{id}/test-event":         "webhooks:manage",
	"POST /v1/developer/webhooks/{id}/events/{id}/resend": "webhooks:manage",
}

// keyRouteScope returns the scope this request needs from an API key, and
// whether a key is accepted on it at all.
func keyRouteScope(r *http.Request) (scope string, accepted bool) {
	scope, accepted = keyRoutes[routeKey(r)]
	return scope, accepted
}

// forbiddenScope is the 403 a key gets when it authenticated correctly and
// simply does not hold what this route needs. Distinct from 401 on purpose:
// the credential is real, the permission is not, and a client that retries a
// 401 by re-authenticating would loop forever on a 403 dressed up as one.
func forbiddenScope(w http.ResponseWriter, scope string) {
	writeError(w, http.StatusForbidden, codeForbidden,
		fmt.Sprintf("This API key does not carry the %s scope.", scope))
}
