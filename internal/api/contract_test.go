package api_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// Every response this backend produces is validated against the frontend
// team's own openapi.json — not against our generated types, which would only
// prove we agree with ourselves.
//
// The spec is loaded from the generated embedded copy, so this fails the build
// the moment an implementation drifts from the contract, which is the whole
// reason we chose spec-first codegen.
func newContractValidator(t *testing.T) func(*testing.T, string, string, response) {
	t.Helper()

	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatalf("load embedded spec: %v", err)
	}
	// The spec declares a server URL the tests do not use; clearing it makes
	// the router match on path alone.
	spec.Servers = nil
	if err := spec.Validate(context.Background()); err != nil {
		t.Fatalf("the contract itself is invalid: %v", err)
	}
	router, err := gorillamux.NewRouter(spec)
	if err != nil {
		t.Fatalf("build router: %v", err)
	}

	return func(t *testing.T, method, path string, res response) {
		t.Helper()

		req := httptest.NewRequest(method, path, nil)
		route, pathParams, err := router.FindRoute(req)
		if err != nil {
			t.Fatalf("%s %s is not in the contract: %v", method, path, err)
		}

		// 204 and other bodiless responses have nothing to validate beyond the
		// status being one the contract declares.
		if _, declared := route.Operation.Responses.Map()[statusKey(res.Code)]; !declared {
			if _, hasDefault := route.Operation.Responses.Map()["default"]; !hasDefault {
				t.Fatalf("%s %s returned %d, which the contract does not declare",
					method, path, res.Code)
			}
		}
		if len(bytes.TrimSpace(res.Body)) == 0 {
			return
		}

		input := &openapi3filter.ResponseValidationInput{
			RequestValidationInput: &openapi3filter.RequestValidationInput{
				Request:    req,
				PathParams: pathParams,
				Route:      route,
				Options: &openapi3filter.Options{
					AuthenticationFunc: func(context.Context, *openapi3filter.AuthenticationInput) error {
						return nil
					},
				},
			},
			Status: res.Code,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   io.NopCloser(bytes.NewReader(res.Body)),
			Options: &openapi3filter.Options{
				IncludeResponseStatus: true,
			},
		}
		if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
			t.Fatalf("%s %s -> %d does not match the contract: %v\nbody: %s",
				method, path, res.Code, err, res.Body)
		}
	}
}

func statusKey(code int) string {
	return map[int]string{
		200: "200", 201: "201", 204: "204", 400: "400", 401: "401",
		403: "403", 404: "404", 409: "409", 410: "410", 422: "422", 429: "429",
	}[code]
}

// TestImplementedOperationsMatchTheContract drives every operation Stage 1
// implements, in both a success and a failure shape, and validates each
// response against openapi.json.
func TestImplementedOperationsMatchTheContract(t *testing.T) {
	h := newHarness(t)
	validate := newContractValidator(t)

	owner := h.newAccount("owner")
	member := h.newAccount("member")
	email := "contract-" + owner.TenantID.String()[:8] + "@example.test"
	h.trackTenant(email)

	cases := []struct {
		name   string
		method string
		path   string
		token  string
		body   any
	}{
		{"signup", http.MethodPost, "/v1/auth/signup", "", signupBody(email)},
		{"signup duplicate", http.MethodPost, "/v1/auth/signup", "", signupBody(email)},
		{"signup invalid", http.MethodPost, "/v1/auth/signup", "",
			map[string]any{"fullName": "", "email": "x@y.test", "password": "short",
				"orgName": "", "country": "IN"}},

		{"login ok", http.MethodPost, "/v1/auth/login", "",
			map[string]string{"email": owner.Email, "password": "test-password-123"}},
		{"login rejected", http.MethodPost, "/v1/auth/login", "",
			map[string]string{"email": owner.Email, "password": "wrong"}},

		{"me", http.MethodGet, "/v1/me", owner.Token, nil},
		{"me unauthenticated", http.MethodGet, "/v1/me", "", nil},
		{"update me", http.MethodPatch, "/v1/me", owner.Token, map[string]string{"name": "Renamed"}},
		{"update me forbidden", http.MethodPatch, "/v1/me", member.Token, map[string]string{"name": "X"}},
		{"update me invalid", http.MethodPatch, "/v1/me", owner.Token, map[string]string{"name": ""}},

		{"update tenant", http.MethodPatch, "/v1/tenant", owner.Token, map[string]string{"name": "Renamed Org"}},
		{"update tenant forbidden", http.MethodPatch, "/v1/tenant", member.Token, map[string]string{"name": "X"}},

		{"sessions", http.MethodGet, "/v1/sessions", owner.Token, nil},
		{"sessions unauthenticated", http.MethodGet, "/v1/sessions", "", nil},

		{"team", http.MethodGet, "/v1/team", owner.Token, nil},
		{"team forbidden", http.MethodGet, "/v1/team", member.Token, nil},
		{"invite", http.MethodPost, "/v1/team/invite", owner.Token,
			map[string]string{"email": "invited-" + email, "role": "member"}},
		{"invite duplicate", http.MethodPost, "/v1/team/invite", owner.Token,
			map[string]string{"email": owner.Email, "role": "member"}},

		{"wallet balances", http.MethodGet, "/v1/wallet/balances", owner.Token, nil},
		{"alerts", http.MethodGet, "/v1/alerts", owner.Token, nil},
		{"conversations", http.MethodGet, "/v1/conversations", owner.Token, nil},

		{"forgot password", http.MethodPost, "/v1/auth/password/forgot", "",
			map[string]string{"email": owner.Email}},
		{"reset invalid token", http.MethodPost, "/v1/auth/password/reset", "",
			map[string]string{"token": "nope", "password": "a-long-password"}},
		{"change password wrong current", http.MethodPatch, "/v1/auth/password", owner.Token,
			map[string]string{"currentPassword": "wrong", "newPassword": "a-long-password"}},

		{"verify email resend", http.MethodPost, "/v1/auth/verify-email/resend", owner.Token, nil},
		{"verify email bad token", http.MethodPost, "/v1/auth/verify-email/confirm", "",
			map[string]string{"token": "nope"}},

		{"mfa enroll", http.MethodPost, "/v1/auth/mfa/enroll", owner.Token, nil},
		{"mfa confirm wrong code", http.MethodPost, "/v1/auth/mfa/enroll/confirm", owner.Token,
			map[string]string{"code": "000000"}},
		{"mfa challenge expired", http.MethodPost, "/v1/auth/mfa/challenge", "",
			map[string]string{"challengeToken": "nope", "code": "000000", "method": "totp"}},

		{"logout", http.MethodPost, "/v1/auth/logout", owner.Token, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := h.do(tc.method, tc.path, tc.token, tc.body)
			validate(t, tc.method, tc.path, res)
		})
	}
}

// Anything still unimplemented must answer 501 with the contract's Error
// envelope. 501 is not in the contract's declared status set, which is
// correct — it means "not built yet", not a documented outcome — so this
// checks the envelope shape directly.
func TestUnimplementedOperationsStillUseTheErrorEnvelope(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	for _, path := range []string{"/v1/campaigns", "/v1/templates", "/v1/sender-ids"} {
		res := h.do(http.MethodGet, path, acct.Token, nil)
		if res.Code != http.StatusNotImplemented {
			t.Errorf("%s: status = %d, want 501", path, res.Code)
			continue
		}
		if code := res.errorCode(t); code != "not_implemented" {
			t.Errorf("%s: error.code = %q, want not_implemented", path, code)
		}
	}
}
