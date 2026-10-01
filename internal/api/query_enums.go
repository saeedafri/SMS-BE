package api

import (
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
// The contract closes the vocabulary of status, range, channel, country and
// the rest, but the generated binder does not check an enum, so
// `?status=__nope__` answered 200 with an empty page and `?range=bogus` quietly
// used the default. A caller who mistyped a filter saw "no results" and drew
// the wrong conclusion. Read from the embedded spec rather than listed here, so
// a new enum is enforced the day it is declared.
type enumRoute struct {
	pattern *regexp.Regexp
	params  map[string][]string
}

var (
	enumRoutesOnce sync.Once
	enumRoutes     map[string][]enumRoute
)

func loadEnumRoutes() {
	enumRoutes = map[string][]enumRoute{}
	spec, err := gen.GetSwagger()
	if err != nil {
		return
	}
	for path, item := range spec.Paths.Map() {
		for method, operation := range item.Operations() {
			params := map[string][]string{}
			for _, ref := range operation.Parameters {
				if ref.Value == nil || ref.Value.In != "query" || ref.Value.Schema == nil {
					continue
				}
				schema := ref.Value.Schema.Value
				if schema != nil && schema.Type != nil && schema.Type.Is("array") && schema.Items != nil {
					schema = schema.Items.Value
				}
				if values := enumValues(schema); len(values) > 0 {
					params[ref.Value.Name] = values
				}
			}
			if len(params) == 0 {
				continue
			}
			enumRoutes[method] = append(enumRoutes[method],
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

// rejectBadEnums answers 422 for a query value the contract does not allow.
// An empty value is left to the handler: several screens send `status=` to
// mean "all".
func rejectBadEnums(next http.Handler) http.Handler {
	enumRoutesOnce.Do(loadEnumRoutes)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if len(query) > 0 {
			for _, route := range enumRoutes[r.Method] {
				if !route.pattern.MatchString(r.URL.Path) {
					continue
				}
				for name, allowed := range route.params {
					for _, raw := range query[name] {
						for _, value := range strings.Split(raw, ",") {
							if value != "" && !slices.Contains(allowed, value) {
								writeError(w, http.StatusUnprocessableEntity, codeValidation,
									fmt.Sprintf("%s must be one of: %s.", name, strings.Join(allowed, ", ")))
								return
							}
						}
					}
				}
				break
			}
		}
		next.ServeHTTP(w, r)
	})
}
