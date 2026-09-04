package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// rejectUnknownFields refuses a JSON body carrying properties the schema does
// not declare, for the routes that declare additionalProperties: false.
//
// encoding/json ignores unknown keys, so a contract that says
// additionalProperties: false is documentation and nothing more unless something
// enforces it. This is middleware rather than a check inside each handler
// because the failure mode being guarded against is precisely a handler that
// forgot — a create path that spread the body while its update sibling assigned
// fields explicitly. One place, applied to both, cannot drift.
//
// The body is buffered and restored, so the generated binder decodes exactly
// what the client sent.
func rejectUnknownFields(allowed map[string][]string) func(http.Handler) http.Handler {
	sets := make(map[string]map[string]bool, len(allowed))
	for route, fields := range allowed {
		set := make(map[string]bool, len(fields))
		for _, field := range fields {
			set[field] = true
		}
		sets[route] = set
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			set, guarded := sets[routeKey(r)]
			if !guarded || r.Body == nil {
				next.ServeHTTP(w, r)
				return
			}
			raw, err := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if err != nil {
				writeError(w, http.StatusUnprocessableEntity, codeValidation,
					"The request body could not be read.")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))

			if len(bytes.TrimSpace(raw)) > 0 {
				var body map[string]json.RawMessage
				if err := json.Unmarshal(raw, &body); err == nil {
					// Which keys the caller actually sent, so a handler can tell
					// "registrationId: null" — clear it — from omitting the field,
					// which means leave it alone. Both decode to a nil pointer, so
					// the typed struct cannot distinguish them on its own.
					present := make(map[string]bool, len(body))
					for key := range body {
						present[key] = true
					}
					r = r.WithContext(context.WithValue(r.Context(), bodyKeysContextKey{}, present))
					var unknown []string
					for key := range body {
						if !set[key] {
							unknown = append(unknown, key)
						}
					}
					if len(unknown) > 0 {
						sort.Strings(unknown)
						writeError(w, http.StatusUnprocessableEntity, codeValidation,
							fmt.Sprintf("Unknown field(s): %s.", strings.Join(unknown, ", ")))
						return
					}
				}
				// A body that is not a JSON object falls through: the generated
				// binder reports that far better than this can.
			}
			next.ServeHTTP(w, r)
		})
	}
}

// routeKey identifies a request by method and path shape, with the trailing id
// collapsed so /v1/operator/connections/{id} matches whatever uuid arrives.
// routeKey turns a request into the contract route it matched, so the allowlist
// can be written the way the contract writes paths.
//
// This middleware runs before chi has resolved a route pattern, so the pattern
// is reconstructed by collapsing id-shaped segments back to {id}. It used to
// compare literal paths with one prefix special-case for connections, which
// worked only because that route's sibling had no id in it — every later entry
// written as {id} silently matched nothing, and the guard passed by never
// firing. That is how a throttle body carrying `status` got through.
func routeKey(r *http.Request) string {
	segments := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
	for i, segment := range segments {
		if _, err := uuid.Parse(segment); err == nil {
			segments[i] = "{id}"
		}
	}
	return r.Method + " " + strings.Join(segments, "/")
}

// connectionBodyFields are the properties ConnectionCreate and ConnectionUpdate
// declare. Kept as one list because the two schemas differ only in which are
// required, and a field allowed on create but rejected on update would be the
// asymmetry this guard exists to prevent.
var connectionBodyFields = []string{
	"label", "carrier", "environment", "host", "port", "systemId", "systemType",
	"bindType", "password", "maxTps", "windowSize", "enquireLinkSeconds",
	"reconnectBackoffSeconds",
}

type bodyKeysContextKey struct{}

// bodyMentions reports whether the request body carried this key at all,
// whatever its value. Only populated for routes in the allowlist above, which
// is where the distinction is needed.
func bodyMentions(ctx context.Context, field string) bool {
	keys, ok := ctx.Value(bodyKeysContextKey{}).(map[string]bool)
	return ok && keys[field]
}
