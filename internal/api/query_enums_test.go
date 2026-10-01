package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// EVERY route that declares an enum on a query parameter refuses a value
// outside it, and accepts each value inside it. Driven from the contract, like
// the limit test beside it, so a new enum is covered the day it is declared.
func TestEveryDeclaredQueryEnumRefusesAValueOutsideIt(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	operator := h.operatorToken()
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatalf("load embedded spec: %v", err)
	}

	checked := 0
	for path, item := range spec.Paths.Map() {
		operation := item.Get
		// Routes with an id in the path need a seeded row to substitute; the
		// middleware matches them by the same pattern, so the ones without
		// carry the proof.
		if operation == nil || strings.Contains(path, "{") {
			continue
		}
		token := acct.Token
		if strings.HasPrefix(path, "/v1/operator") {
			token = operator
		}
		base := h.concreteURL(path, acct) + requiredQuery(operation.Parameters)
		for _, ref := range operation.Parameters {
			param := ref.Value
			if param == nil || param.In != "query" || param.Schema == nil || param.Schema.Value == nil {
				continue
			}
			schema := param.Schema.Value
			if schema.Type != nil && schema.Type.Is("array") && schema.Items != nil {
				schema = schema.Items.Value
			}
			if len(schema.Enum) == 0 {
				continue
			}
			checked++
			bad := fmt.Sprintf("%s%s%s=__nope__", base, separator(base), param.Name)
			res := h.do(http.MethodGet, bad, token, nil)
			if res.Code != http.StatusUnprocessableEntity || !strings.Contains(string(res.Body), param.Name+" must be one of") {
				t.Errorf("GET %s %s=__nope__ answered %d %s, want 422 naming the parameter",
					path, param.Name, res.Code, res.Body)
			}
			// A parameter the route requires is already in base.
			if good, ok := schema.Enum[0].(string); ok && !strings.Contains(base, param.Name+"=") {
				url := fmt.Sprintf("%s%s%s=%s", base, separator(base), param.Name, good)
				if res := h.do(http.MethodGet, url, token, nil); res.Code == http.StatusUnprocessableEntity {
					t.Errorf("GET %s %s=%s refused a value the contract allows: %s",
						path, param.Name, good, res.Body)
				}
			}
		}
	}
	if checked < 25 {
		t.Errorf("only %d enum parameters checked; the contract carries more than that", checked)
	}
}

// A comma list is checked item by item, and an empty value still means "all".
func TestAListOfEnumValuesIsCheckedItemByItem(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	operator := h.operatorToken()
	if res := h.do(http.MethodGet, "/v1/operator/campaigns?status=queued,sending", operator, nil); res.Code != 200 {
		t.Errorf("valid list = %d %s, want 200", res.Code, res.Body)
	}
	if res := h.do(http.MethodGet, "/v1/operator/campaigns?status=queued,nope", operator, nil); res.Code != 422 {
		t.Errorf("list with a bad item = %d, want 422", res.Code)
	}
	if res := h.do(http.MethodGet, "/v1/operator/campaigns?status=", operator, nil); res.Code != 200 {
		t.Errorf("empty status = %d, want 200 (all)", res.Code)
	}
}
