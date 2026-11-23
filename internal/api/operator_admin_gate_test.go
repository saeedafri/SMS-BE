package api_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// Signing in to the operator console is not permission to move money or to
// change a tenant's standing. A plain operator asks every such route and is
// refused with 403 before anything is read or written, and the tenant is left
// exactly as it was.
func TestPlainOperatorIsRefusedEveryMoneyAndStandingRoute(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tenant := h.newAccount("owner")
	plain := h.operatorTokenAs("harness-plain-ops@relay.internal", "operator")
	id := tenant.TenantID.String()
	other := uuid.NewString()

	routes := []struct{ method, path string }{
		{http.MethodPost, "/v1/operator/tenants/" + id + "/wallet/credit"},
		{http.MethodPost, "/v1/operator/tenants/" + id + "/payments"},
		{http.MethodPost, "/v1/operator/tenants/" + id + "/payments/" + other + "/void"},
		{http.MethodPut, "/v1/operator/tenants/" + id + "/credit-limit"},
		{http.MethodPost, "/v1/operator/tenants/" + id + "/suspend"},
		{http.MethodPost, "/v1/operator/tenants/" + id + "/reinstate"},
		{http.MethodPost, "/v1/operator/tenants/" + id + "/throttle"},
		{http.MethodPatch, "/v1/operator/rates/default"},
		{http.MethodPost, "/v1/operator/rates/overrides"},
		{http.MethodPatch, "/v1/operator/rates/overrides/" + other},
		{http.MethodDelete, "/v1/operator/rates/overrides/" + other},
	}
	for _, route := range routes {
		var body any = map[string]any{}
		if route.method == http.MethodDelete {
			body = nil
		}
		res := h.do(route.method, route.path, plain, body)
		if res.Code != http.StatusForbidden || res.errorCode(t) != "forbidden" {
			t.Errorf("%s %s as a plain operator = %d %s, want 403 forbidden",
				route.method, route.path, res.Code, res.Body)
		}
	}

	var detail struct {
		Status string `json:"status"`
	}
	res := h.do(http.MethodGet, "/v1/operator/tenants/"+id, h.operatorToken(), nil)
	res.decode(t, &detail)
	if detail.Status != "active" {
		t.Errorf("tenant status after refused calls = %q, want active", detail.Status)
	}
}
