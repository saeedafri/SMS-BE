package api_test

import (
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"
)

// PATCH /v1/operator/routes/{id}: the cost, and nothing else.
//
// Before this, correcting a price meant deleting the route and adding it again,
// which put it last in the corridor and disabled — an outage performed by hand
// on a live path. BACKEND_REQUEST_route-update.md §1.

type routeBodyShape struct {
	ID                  string  `json:"id"`
	Label               string  `json:"label"`
	Status              string  `json:"status"`
	Priority            int     `json:"priority"`
	Carrier             string  `json:"carrier"`
	Country             string  `json:"country"`
	Channel             string  `json:"channel"`
	ComplianceStanding  string  `json:"complianceStanding"`
	Currency            string  `json:"currency"`
	CostPerSegmentMinor int     `json:"costPerSegmentMinor"`
	ConnectionID        *string `json:"connectionId"`
}

// seedRouteForUpdate creates one route and returns it as the API renders it.
func seedRouteForUpdate(t *testing.T, h *harness, operator string) routeBodyShape {
	t.Helper()
	res := createRoute(t, h, operator, routeBody(fmt.Sprintf("Videocon transactional %d", rand.Int()), nil))
	if res.Code != http.StatusCreated {
		t.Fatalf("seed route = %d, want 201\n%s", res.Code, res.Body)
	}
	var route routeBodyShape
	res.decode(t, &route)
	return route
}

func TestARoutesCostIsChangedInPlaceAndNothingElseMoves(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	before := seedRouteForUpdate(t, h, operator)

	res := h.do(http.MethodPatch, "/v1/operator/routes/"+before.ID, operator,
		map[string]any{"costPerSegmentMinor": 7})
	if res.Code != http.StatusOK {
		t.Fatalf("patch = %d, want 200\n%s", res.Code, res.Body)
	}
	var after routeBodyShape
	res.decode(t, &after)
	if after.CostPerSegmentMinor != 7 {
		t.Errorf("cost = %d, want 7", after.CostPerSegmentMinor)
	}
	// Everything else byte-identical: a reprice that moved the route would send
	// live traffic somewhere else while the console still read as before.
	before.CostPerSegmentMinor = 7
	if after != before {
		t.Errorf("the reprice moved something else:\n before %+v\n after  %+v", before, after)
	}

	// Zero is a value, not "unset".
	if zero := h.do(http.MethodPatch, "/v1/operator/routes/"+before.ID, operator,
		map[string]any{"costPerSegmentMinor": 0}); zero.Code != http.StatusOK {
		t.Errorf("cost 0 = %d, want 200\n%s", zero.Code, zero.Body)
	}

	// The list reflects it, and the change is in the audit log.
	list := h.do(http.MethodGet, "/v1/operator/routes?country=IN&channel=SMS", operator, nil)
	if !strings.Contains(string(list.Body), before.Label) {
		t.Fatalf("the route is missing from the list: %s", list.Body)
	}
	audit := h.do(http.MethodGet, "/v1/operator/audit-log?range=90d&limit=50&action=route.update", operator, nil)
	if audit.Code != http.StatusOK || !strings.Contains(string(audit.Body), before.Label) {
		t.Errorf("no route.update audit row naming %q: %d %s", before.Label, audit.Code, audit.Body)
	}
}

func TestACostThatIsNotAWholeNonNegativeNumberIsRefused(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	route := seedRouteForUpdate(t, h, operator)

	for name, body := range map[string]any{
		"negative":     map[string]any{"costPerSegmentMinor": -1},
		"not a whole":  map[string]any{"costPerSegmentMinor": 1.5},
		"empty object": map[string]any{},
		"no body":      nil,
	} {
		res := h.do(http.MethodPatch, "/v1/operator/routes/"+route.ID, operator, body)
		if res.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d %s, want 422", name, res.Code, res.Body)
		}
	}
	assertRouteUnchanged(t, h, operator, route)
}

// additionalProperties: false is documentation until the path is registered in
// rejectUnknownFields — encoding/json drops unknown keys in silence, so a body
// naming a carrier would answer 200 and change nothing, which reads to an
// operator as a successful edit.
func TestARouteUpdateCarryingAnyOtherFieldIsRefused(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	route := seedRouteForUpdate(t, h, operator)

	for name, body := range map[string]map[string]any{
		"cost plus a label": {"costPerSegmentMinor": 7, "label": "x"},
		"another field":     {"carrier": "AIRTEL"},
		"a typo":            {"costPerSegment": 7},
	} {
		res := h.do(http.MethodPatch, "/v1/operator/routes/"+route.ID, operator, body)
		if res.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d %s, want 422", name, res.Code, res.Body)
		}
	}
	assertRouteUnchanged(t, h, operator, route)
}

func TestRepricingNeedsAnOperatorAndARouteThatExists(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	route := seedRouteForUpdate(t, h, operator)
	cost := map[string]any{"costPerSegmentMinor": 9}

	for name, tc := range map[string]struct {
		id, token string
		want      int
	}{
		"unknown id":   {"6f1d1f6a-0000-4000-8000-000000000000", operator, http.StatusNotFound},
		"malformed id": {"not-a-uuid", operator, http.StatusNotFound},
		"no token":     {route.ID, "", http.StatusUnauthorized},
		"tenant token": {route.ID, h.newAccount("owner").Token, http.StatusUnauthorized},
	} {
		if res := h.do(http.MethodPatch, "/v1/operator/routes/"+tc.id, tc.token, cost); res.Code != tc.want {
			t.Errorf("%s = %d %s, want %d", name, res.Code, res.Body, tc.want)
		}
	}
	assertRouteUnchanged(t, h, operator, route)
}

// assertRouteUnchanged re-reads the route and fails if any field moved.
func assertRouteUnchanged(t *testing.T, h *harness, operator string, want routeBodyShape) {
	t.Helper()
	var routes struct {
		Routes []routeBodyShape `json:"routes"`
	}
	res := h.do(http.MethodGet, "/v1/operator/routes", operator, nil)
	res.decode(t, &routes)
	for _, got := range routes.Routes {
		if got.ID != want.ID {
			continue
		}
		if got != want {
			t.Errorf("a refused request changed the route:\n before %+v\n after  %+v", want, got)
		}
		return
	}
	t.Errorf("route %s is gone from the list", want.ID)

}
