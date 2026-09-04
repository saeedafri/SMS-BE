package api_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/gen/api"
)

// throttle posts a raw body so the malformed cases can send things the typed
// request object could not express.
func throttle(t *testing.T, h *harness, operator string, tenantID uuid.UUID, body any) response {
	t.Helper()
	return h.do(http.MethodPost, "/v1/operator/tenants/"+tenantID.String()+"/throttle",
		operator, body)
}

// A throttled tenant carries the ceiling that was applied to it.
//
// The endpoint used to take no body at all, so "throttled" was a boolean with
// no number behind it: an operator could mark a tenant throttled but not say to
// what, which is the single most common reason to throttle anyone — honouring a
// carrier's contracted TPS.
func TestThrottleRecordsTheRateItApplied(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	acct := h.newAccount("owner")

	res := throttle(t, h, operator, acct.TenantID, map[string]any{"ratePerSecond": 40})
	if res.Code != http.StatusOK {
		t.Fatalf("throttle = %d, want 200; body = %s", res.Code, res.Body)
	}
	var detail gen.TenantDetail
	res.decode(t, &detail)

	if detail.Status != gen.TenantStatus("throttled") {
		t.Errorf("status = %q, want throttled", detail.Status)
	}
	if detail.ThrottledRatePerSecond == nil || *detail.ThrottledRatePerSecond != 40 {
		t.Fatalf("throttledRatePerSecond = %v, want 40", detail.ThrottledRatePerSecond)
	}
}

// The invariant, stated as the frontend states it:
//
//	throttledRatePerSecond is non-null if and only if status is throttled.
//
// Getting the transition IN right is easy. The mistake to avoid is the
// transitions OUT — every path that leaves throttled must clear the rate, or
// the console reports a live ceiling on a tenant that no longer has one. This
// walks both paths that exist rather than asserting one of them.
func TestEveryTransitionOutOfThrottledClearsTheRate(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()

	for _, exit := range []struct{ name, path, wantStatus string }{
		{"reinstate", "reinstate", "active"},
		{"suspend", "suspend", "suspended"},
	} {
		t.Run(exit.name, func(t *testing.T) {
			acct := h.newAccount("owner")
			if res := throttle(t, h, operator, acct.TenantID,
				map[string]any{"ratePerSecond": 25}); res.Code != http.StatusOK {
				t.Fatalf("throttle = %d: %s", res.Code, res.Body)
			}

			res := h.do(http.MethodPost,
				"/v1/operator/tenants/"+acct.TenantID.String()+"/"+exit.path, operator, nil)
			if res.Code != http.StatusOK {
				t.Fatalf("%s = %d: %s", exit.name, res.Code, res.Body)
			}
			var detail gen.TenantDetail
			res.decode(t, &detail)

			if string(detail.Status) != exit.wantStatus {
				t.Errorf("status = %q, want %q", detail.Status, exit.wantStatus)
			}
			if detail.ThrottledRatePerSecond != nil {
				t.Errorf("%s left a ceiling of %d behind on a tenant that is now %s",
					exit.name, *detail.ThrottledRatePerSecond, detail.Status)
			}
		})
	}
}

// A bad id is reported as a bad id.
//
// Validating the body first would answer 422 for a tenant that does not exist,
// and an operator chasing a typo'd id would be told their rate was wrong. The
// body here is invalid too, so the only way to pass is to check the id first.
func TestAnUnknownTenantIsA404EvenWithAnInvalidRate(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()

	res := throttle(t, h, operator, uuid.New(), map[string]any{"ratePerSecond": 0})
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", res.Code, res.Body)
	}
}

func TestAnUnusableRateIsRefused(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()

	for _, bad := range []struct {
		name string
		body any
	}{
		{"missing", map[string]any{}},
		{"zero", map[string]any{"ratePerSecond": 0}},
		{"negative", map[string]any{"ratePerSecond": -5}},
		{"fractional", map[string]any{"ratePerSecond": 2.5}},
		{"not a number", map[string]any{"ratePerSecond": "fast"}},
		{"null", map[string]any{"ratePerSecond": nil}},
	} {
		t.Run(bad.name, func(t *testing.T) {
			acct := h.newAccount("owner")
			res := throttle(t, h, operator, acct.TenantID, bad.body)
			if res.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s rate = %d, want 422; body = %s", bad.name, res.Code, res.Body)
			}
			// And nothing was applied.
			after := h.do(http.MethodGet, "/v1/operator/tenants/"+acct.TenantID.String(),
				operator, nil)
			var detail gen.TenantDetail
			after.decode(t, &detail)
			if detail.ThrottledRatePerSecond != nil {
				t.Errorf("a refused throttle still set a ceiling of %d",
					*detail.ThrottledRatePerSecond)
			}
		})
	}
}

// Re-throttling would silently overwrite the ceiling an earlier operator set,
// without either of them seeing the other's number.
func TestThrottlingAnAlreadyThrottledTenantIsRefused(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	acct := h.newAccount("owner")

	if res := throttle(t, h, operator, acct.TenantID,
		map[string]any{"ratePerSecond": 10}); res.Code != http.StatusOK {
		t.Fatalf("first throttle = %d: %s", res.Code, res.Body)
	}
	res := throttle(t, h, operator, acct.TenantID, map[string]any{"ratePerSecond": 99})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("second throttle = %d, want 422; body = %s", res.Code, res.Body)
	}

	// The original ceiling is intact — a refused change must not half-apply.
	after := h.do(http.MethodGet, "/v1/operator/tenants/"+acct.TenantID.String(), operator, nil)
	var detail gen.TenantDetail
	after.decode(t, &detail)
	if detail.ThrottledRatePerSecond == nil || *detail.ThrottledRatePerSecond != 10 {
		t.Fatalf("ceiling = %v after a refused re-throttle, want 10",
			detail.ThrottledRatePerSecond)
	}
}

// A rate limit on a suspended tenant is a number the console would display and
// nothing would honour.
func TestOnlyAnActiveTenantCanBeThrottled(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	acct := h.newAccount("owner")

	if res := h.do(http.MethodPost,
		"/v1/operator/tenants/"+acct.TenantID.String()+"/suspend", operator, nil,
	); res.Code != http.StatusOK {
		t.Fatalf("suspend = %d: %s", res.Code, res.Body)
	}
	res := throttle(t, h, operator, acct.TenantID, map[string]any{"ratePerSecond": 10})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("throttling a suspended tenant = %d, want 422; body = %s", res.Code, res.Body)
	}
}

// additionalProperties: false, actually enforced.
//
// The declaration is documentation without this: encoding/json drops unknown
// keys silently, so a body carrying `status` would be accepted and the operator
// would reasonably believe it had done something.
func TestAThrottleBodyCannotSmuggleAStatusChange(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	acct := h.newAccount("owner")

	res := throttle(t, h, operator, acct.TenantID,
		map[string]any{"ratePerSecond": 40, "status": "suspended"})
	if res.Code != http.StatusBadRequest && res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown field accepted: status = %d; body = %s", res.Code, res.Body)
	}

	after := h.do(http.MethodGet, "/v1/operator/tenants/"+acct.TenantID.String(), operator, nil)
	var detail gen.TenantDetail
	after.decode(t, &detail)
	if detail.Status == gen.TenantStatus("suspended") {
		t.Fatal("a throttle body changed the tenant's status to suspended")
	}
}

// "Throttled Acme" does not tell a later reader what ceiling was applied, which
// is the only thing they will want to know.
func TestTheAuditEntryNamesTheCeiling(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	acct := h.newAccount("owner")

	if res := throttle(t, h, operator, acct.TenantID,
		map[string]any{"ratePerSecond": 40, "reason": "carrier TPS cap"},
	); res.Code != http.StatusOK {
		t.Fatalf("throttle = %d: %s", res.Code, res.Body)
	}

	log := h.do(http.MethodGet,
		"/v1/operator/audit-log?tenantId="+acct.TenantID.String()+"&action=tenant.throttle",
		operator, nil)
	var page gen.AuditLogPage
	log.decode(t, &page)
	if len(page.Entries) == 0 {
		t.Fatal("the throttle wrote no audit entry")
	}
	detail := page.Entries[0].Detail
	if !contains(detail, "40") {
		t.Errorf("audit detail = %q, want it to name the rate that was applied", detail)
	}
	if !contains(detail, "carrier TPS cap") {
		t.Errorf("audit detail = %q, want it to carry the operator's reason", detail)
	}
}
