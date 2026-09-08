package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// EVERY route that declares a limit refuses one outside 1..200.
//
// Driven from the contract rather than from a list, because a hand-written list
// of twenty-four routes is a list someone forgets to add the twenty-fifth to.
// This walks `spec.paths`, finds every operation with a `limit` parameter, and
// asserts the server enforces the bounds that parameter declares — so a new
// paged endpoint is covered the day it is declared, without anyone remembering.
//
// The rule it enforces exists because this API had THREE answers to limit=201:
// most routes discarded the value and fell back to their own default,
// /v1/contacts clamped to 200, and /v1/operator/user-activity honoured it. A
// caller could write a correct paging walk against one endpoint and have the
// identical code silently truncate against another — which is exactly what
// happened: a whole-collection reader asked /v1/campaigns for 500, got 20 rows
// beside a total of 75, and took two of the frontend's screens down.
func TestEveryPagedRouteRefusesALimitOutsideItsDeclaredBounds(t *testing.T) {
	// The send harness, because two of these routes read the message log and
	// a 500 from a missing store would mask the answer this is asking for.
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
		if operation == nil {
			continue
		}
		limit := declaredLimit(operation.Parameters)
		if limit == nil {
			continue
		}
		checked++
		t.Run(path, func(t *testing.T) {
			// The contract's own numbers, not repeated here: raising the
			// maximum in the contract moves this test with it.
			if limit.Min == nil || limit.Max == nil {
				t.Fatalf("declares a limit with no minimum/maximum to enforce")
			}
			min, max := int(*limit.Min), int(*limit.Max)
			token := acct.Token
			if strings.HasPrefix(path, "/v1/operator") {
				token = operator
			}
			url := h.concreteURL(path, acct) + requiredQuery(operation.Parameters)

			// Above the maximum and below the minimum are both refused, and
			// the boundary value itself is not — an off-by-one in the guard
			// would otherwise pass the first two assertions.
			for _, probe := range []struct {
				limit int
				want  int
			}{
				{max + 1, http.StatusUnprocessableEntity},
				{min - 1, http.StatusUnprocessableEntity},
				{max, http.StatusOK},
				{min, http.StatusOK},
			} {
				res := h.do(http.MethodGet,
					fmt.Sprintf("%s%slimit=%d", url, separator(url), probe.limit), token, nil)
				if res.Code != probe.want {
					t.Errorf("limit=%d answered %d, want %d — %s",
						probe.limit, res.Code, probe.want, res.Body)
				}
			}

			// And the refusal says which parameter was wrong. A 422 alone
			// cannot be told apart from the page rule these routes also carry.
			res := h.do(http.MethodGet,
				fmt.Sprintf("%s%slimit=%d", url, separator(url), max+1), token, nil)
			if !strings.Contains(string(res.Body), "Limit must be between") {
				t.Errorf("refused for the wrong reason: %s", res.Body)
			}
		})
	}
	if checked < 24 {
		t.Errorf("only %d routes declare a limit — the contract carries 24, so "+
			"this test is walking less than it thinks", checked)
	}
}

type limitBounds struct{ Min, Max *float64 }

func declaredLimit(params openapi3.Parameters) *limitBounds {
	for _, ref := range params {
		if ref.Value == nil || ref.Value.Name != "limit" || ref.Value.Schema == nil {
			continue
		}
		return &limitBounds{Min: ref.Value.Schema.Value.Min, Max: ref.Value.Schema.Value.Max}
	}
	return nil
}

// requiredQuery supplies the parameters a route refuses to run without, so a
// missing `environment` cannot masquerade as a limit refusal — which it did on
// the first run of this test, on two routes, with the right status code.
func requiredQuery(params openapi3.Parameters) string {
	out := ""
	for _, ref := range params {
		p := ref.Value
		if p == nil || !p.Required || p.In != "query" {
			continue
		}
		value := "live"
		if p.Schema != nil && p.Schema.Value != nil && len(p.Schema.Value.Enum) > 0 {
			value = fmt.Sprint(p.Schema.Value.Enum[0])
		}
		out += fmt.Sprintf("?%s=%s", p.Name, value)
	}
	return out
}

func separator(url string) string {
	if strings.Contains(url, "?") {
		return "&"
	}
	return "?"
}

// concreteURL fills a templated path with ids that really exist on this tenant.
//
// Real ones rather than a placeholder, because the boundary probes expect a
// 200: an invented id answers 404 and would let an off-by-one in the guard
// through on exactly the four routes that take a path parameter. The 422 probes
// would pass either way — every handler refuses a bad limit before it looks
// anything up — which is precisely why they cannot be the only assertion.
func (h *harness) concreteURL(path string, acct account) string {
	h.t.Helper()
	if !strings.Contains(path, "{") {
		return path
	}
	switch {
	case strings.HasPrefix(path, "/v1/campaigns/"):
		if h.sampleCampaign == "" {
			_, h.sampleCampaign = h.seedOptedOutCampaign(acct, "limit bounds")
		}
		return strings.Replace(path, "{id}", h.sampleCampaign, 1)
	case strings.HasPrefix(path, "/v1/developer/webhooks/"):
		if h.sampleWebhook == "" {
			h.sampleWebhook = h.seedWebhookReturningID(acct, "live")
		}
		return strings.Replace(path, "{id}", h.sampleWebhook, 1)
	case strings.HasPrefix(path, "/v1/verify/services/"):
		if h.sampleVerifyService == "" {
			h.sampleVerifyService = h.seedVerifyService(acct)
		}
		return strings.Replace(path, "{id}", h.sampleVerifyService, 1)
	}
	h.t.Fatalf("no id to substitute into %s — add one rather than skipping the route", path)
	return ""
}

func (h *harness) seedWebhookReturningID(acct account, environment string) string {
	h.t.Helper()
	var id string
	if err := h.admin.QueryRow(h.t.Context(), `
		INSERT INTO webhook_endpoints (tenant_id, environment, url, subscribed_events,
		                               signing_secret_prefix, signing_secret_hash, status)
		VALUES ($1, $2, $3, ARRAY['message.delivered'], $4, $5, 'enabled')
		RETURNING id`, acct.TenantID, environment,
		"https://example.test/bounds/"+uuid.NewString()[:8],
		"whsec_"+uuid.NewString()[:8], uuid.NewString()).Scan(&id); err != nil {
		h.t.Fatalf("seed webhook: %v", err)
	}
	return id
}

func (h *harness) seedVerifyService(acct account) string {
	h.t.Helper()
	var id string
	if err := h.admin.QueryRow(h.t.Context(), `
		INSERT INTO verify_services (tenant_id, name, channels, fallback_order,
		                             code_length, code_ttl_seconds, max_attempts,
		                             max_per_phone, window_seconds, cooldown_seconds,
		                             region_allowlist)
		VALUES ($1, $2, '[]'::jsonb, ARRAY['SMS'], 6, 300, 5, 5, 3600, 60,
		        ARRAY['IN']) RETURNING id`,
		acct.TenantID, "bounds "+uuid.NewString()[:8]).Scan(&id); err != nil {
		h.t.Fatalf("seed verify service: %v", err)
	}
	return id
}
