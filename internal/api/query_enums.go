package api

import (
	"bytes"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// Query-parameter enums, enforced from the contract.
//
// The contract closes the vocabulary of status, range, channel, country and the
// rest, but the generated binder does not check an enum, so `?status=__nope__`
// answered 200 with an empty page and `?range=bogus` quietly used the default.
// A caller who mistyped a filter saw "no results" and drew the wrong conclusion.
// Read from the embedded spec rather than listed here, so a new enum is
// enforced the day it is declared.
//
// A handler's own refusal wins. Many handlers already reject a bad value with
// specific wording the frontend and existing tests depend on, so a request
// carrying a value outside the enum is run through the handler with its
// response held back: if the handler refused it, that answer goes out
// unchanged; if it would have answered 2xx, the caller gets a 422 instead. Only
// GETs are held, they have no side effects, and only when a bad value is
// actually present, so every ordinary request pays nothing.
type enumRoute struct {
	pattern *regexp.Regexp
	params  map[string][]string
}

var (
	enumRoutesOnce sync.Once
	enumRoutes     []enumRoute
)

// enumExtras are values the server accepts that the contract's enum has not
// caught up with. rcs_agent is a real approvals queue (see operator.go) that
// ApprovalType does not list; refusing it would break the console.
var enumExtras = map[string][]string{
	"/v1/operator/approvals type": {"rcs_agent"},
}

func loadEnumRoutes() {
	spec, err := gen.GetSwagger()
	if err != nil {
		return
	}
	for path, item := range spec.Paths.Map() {
		if item.Get == nil {
			continue
		}
		params := map[string][]string{}
		for _, ref := range item.Get.Parameters {
			if ref.Value == nil || ref.Value.In != "query" || ref.Value.Schema == nil {
				continue
			}
			schema := ref.Value.Schema.Value
			if schema != nil && schema.Type != nil && schema.Type.Is("array") && schema.Items != nil {
				schema = schema.Items.Value
			}
			if values := enumValues(schema); len(values) > 0 {
				params[ref.Value.Name] = append(values, enumExtras[path+" "+ref.Value.Name]...)
			}
		}
		if len(params) > 0 {
			enumRoutes = append(enumRoutes,
				enumRoute{pattern: regexp.MustCompile(fixPlaceholders(path)), params: params})
		}
	}
}

// fixPlaceholders builds the matcher for one spec path.
func fixPlaceholders(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, "{") {
			parts[i] = "[^/]+"
		} else {
			parts[i] = regexp.QuoteMeta(part)
		}
	}
	return "^" + strings.Join(parts, "/") + "$"
}

func enumValues(schema *openapi3.Schema) []string {
	if schema == nil || len(schema.Enum) == 0 {
		return nil
	}
	values := make([]string, 0, len(schema.Enum))
	for _, value := range schema.Enum {
		if text, ok := value.(string); ok {
			values = append(values, text)
		}
	}
	return values
}

// badEnum names the first query parameter carrying a value the contract does not
// allow, and what it does allow. An empty value is left alone: several screens
// send `status=` to mean "all".
func badEnum(r *http.Request) (name string, allowed []string, found bool) {
	query := r.URL.Query()
	for _, route := range enumRoutes {
		if !route.pattern.MatchString(r.URL.Path) {
			continue
		}
		for param, values := range route.params {
			for _, raw := range query[param] {
				for _, value := range strings.Split(raw, ",") {
					if value != "" && !slices.Contains(values, value) {
						return param, values, true
					}
				}
			}
		}
		return "", nil, false
	}
	return "", nil, false
}

func rejectBadEnums(next http.Handler) http.Handler {
	enumRoutesOnce.Do(loadEnumRoutes)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RawQuery == "" {
			next.ServeHTTP(w, r)
			return
		}
		name, allowed, bad := badEnum(r)
		if !bad {
			next.ServeHTTP(w, r)
			return
		}
		held := &heldResponse{header: http.Header{}}
		next.ServeHTTP(held, r)
		if held.status >= 400 {
			held.flushTo(w)
			return
		}
		writeError(w, http.StatusUnprocessableEntity, codeValidation,
			fmt.Sprintf("%s must be one of: %s.", name, strings.Join(allowed, ", ")))
	})
}

// heldResponse keeps a handler's answer so the middleware can decide whether it
// goes out.
type heldResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (h *heldResponse) Header() http.Header { return h.header }

func (h *heldResponse) WriteHeader(status int) {
	if h.status == 0 {
		h.status = status
	}
}

func (h *heldResponse) Write(data []byte) (int, error) {
	if h.status == 0 {
		h.status = http.StatusOK
	}
	return h.body.Write(data)
}

func (h *heldResponse) flushTo(w http.ResponseWriter) {
	for name, values := range h.header {
		w.Header()[name] = values
	}
	w.WriteHeader(h.status)
	_, _ = w.Write(h.body.Bytes())
}
