package api_test

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"
)

// Two refusals on creating a route, both from the frontend's 16 Sep list.

// createRoute posts one route and cleans it up, whatever the answer was.
func createRoute(t *testing.T, h *harness, operator string, body map[string]any) response {
	t.Helper()
	res := h.do(http.MethodPost, "/v1/operator/routes", operator, body)
	if res.Code == http.StatusCreated {
		var created struct {
			ID string `json:"id"`
		}
		res.decode(t, &created)
		t.Cleanup(func() {
			_, _ = h.admin.Exec(context.Background(), `DELETE FROM routes WHERE id = $1`, created.ID)
		})
	}
	return res
}

func routeBody(label string, extra map[string]any) map[string]any {
	body := map[string]any{
		"country": "IN", "channel": "SMS", "carrier": "VIDEOCON", "label": label,
		"complianceStanding": "registered", "costPerSegmentMinor": 12, "currency": "INR",
	}
	for key, value := range extra {
		body[key] = value
	}
	return body
}

// The label is how an operator tells two paths to the same carrier apart, so
// two rows carrying the same one in a corridor make the console ambiguous
// exactly where an operator is choosing which path to disable.
func TestASecondRouteWithTheSameLabelInACorridorIsRefused(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	label := fmt.Sprintf("Videocon direct %d", rand.Int())

	if first := createRoute(t, h, operator, routeBody(label, nil)); first.Code != http.StatusCreated {
		t.Fatalf("first route = %d, want 201\n%s", first.Code, first.Body)
	}

	second := createRoute(t, h, operator, routeBody(label, nil))
	if second.Code != http.StatusConflict || second.errorCode(t) != "conflict" {
		t.Fatalf("same label again = %d %s, want 409 conflict", second.Code, second.Body)
	}
	if !strings.Contains(string(second.Body), "label") {
		t.Errorf("the refusal does not name the label: %s", second.Body)
	}

	// Same label, different corridor: allowed. A corridor is one country and
	// channel, and "Videocon direct" on RCS is a different path entirely.
	elsewhere := createRoute(t, h, operator, routeBody(label, map[string]any{"channel": "RCS"}))
	if elsewhere.Code != http.StatusCreated {
		t.Errorf("the same label in another corridor = %d, want 201\n%s", elsewhere.Code, elsewhere.Body)
	}
}

// A connection id that names nothing used to reach the database and come back
// as a foreign key error, which the console can only show as "something went
// wrong".
func TestARouteNamingAConnectionThatDoesNotExistIsRefused(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()

	res := createRoute(t, h, operator, routeBody(fmt.Sprintf("Ghost bind %d", rand.Int()),
		map[string]any{"connectionId": "6f1d1f6a-0000-4000-8000-000000000000"}))
	if res.Code != http.StatusUnprocessableEntity || res.errorCode(t) != "validation_failed" {
		t.Fatalf("unknown connectionId = %d %s, want 422 validation_failed", res.Code, res.Body)
	}
	if !strings.Contains(string(res.Body), "connection") {
		t.Errorf("the refusal does not name the connection: %s", res.Body)
	}
}
