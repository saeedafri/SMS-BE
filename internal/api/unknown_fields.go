package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
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
func routeKey(r *http.Request) string {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if r.Method == http.MethodPatch && strings.HasPrefix(path, "/v1/operator/connections/") {
		return "PATCH /v1/operator/connections/{id}"
	}
	return r.Method + " " + path
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
