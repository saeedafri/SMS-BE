package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The operator on a connection or a route is a name the operator console types,
// not a list this code knows. A new operator contract (Videocon) must not need a
// release before it can be configured.
func TestAnyWellFormedOperatorNameCanBeConfigured(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()

	created := createConnection(t, h, operator, map[string]any{"carrier": "VIDEOCON"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create VIDEOCON connection = %d, want 201\n%s", created.Code, created.Body)
	}
	var connection struct {
		ID      string `json:"id"`
		Carrier string `json:"carrier"`
	}
	created.decode(t, &connection)
	t.Cleanup(func() { h.do(http.MethodDelete, "/v1/operator/connections/"+connection.ID, operator, nil) })
	if connection.Carrier != "VIDEOCON" {
		t.Errorf("connection carrier = %q, want VIDEOCON", connection.Carrier)
	}

	updated := h.do(http.MethodPatch, "/v1/operator/connections/"+connection.ID, operator,
		map[string]any{"carrier": "TATA_TELE"})
	if updated.Code != http.StatusOK {
		t.Fatalf("update carrier to TATA_TELE = %d, want 200\n%s", updated.Code, updated.Body)
	}

	route := h.do(http.MethodPost, "/v1/operator/routes", operator, map[string]any{
		"country": "IN", "channel": "SMS", "carrier": "VIDEOCON",
		"label": "Videocon direct", "complianceStanding": "registered",
		"costPerSegmentMinor": 12, "currency": "INR",
	})
	if route.Code != http.StatusCreated {
		t.Fatalf("create VIDEOCON route = %d, want 201\n%s", route.Code, route.Body)
	}
	var created2 struct {
		ID string `json:"id"`
	}
	route.decode(t, &created2)
	t.Cleanup(func() {
		_, _ = h.admin.Exec(context.Background(), `DELETE FROM routes WHERE id = $1`, created2.ID)
	})
}

// Free, but not anything: the name is matched exactly against receipts and
// routes, so a lowercase or spaced variant would silently be a second operator.
func TestAMalformedOperatorNameIsRefused(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()

	for _, bad := range []string{"", "videocon", "VIDEO CON", "V", "1JIO", strings.Repeat("A", 21)} {
		res := createConnection(t, h, operator, map[string]any{"carrier": bad})
		if res.Code != http.StatusUnprocessableEntity || !strings.Contains(string(res.Body), "carrier") {
			t.Errorf("connection carrier %q = %d %s, want 422 naming carrier", bad, res.Code, res.Body)
		}
		route := h.do(http.MethodPost, "/v1/operator/routes", operator, map[string]any{
			"country": "IN", "channel": "SMS", "carrier": bad, "label": "Bad name",
			"complianceStanding": "registered", "costPerSegmentMinor": 12, "currency": "INR",
		})
		if route.Code == http.StatusCreated {
			// Only a broken check gets here; never leave its route behind for
			// other packages sharing this database to trip over.
			var created struct {
				ID string `json:"id"`
			}
			route.decode(t, &created)
			_, _ = h.admin.Exec(context.Background(), `DELETE FROM routes WHERE id = $1`, created.ID)
		}
		if route.Code != http.StatusUnprocessableEntity || !strings.Contains(string(route.Body), "carrier") {
			t.Errorf("route carrier %q = %d %s, want 422 naming carrier", bad, route.Code, route.Body)
		}
	}
}
