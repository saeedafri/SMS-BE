package api_test

import (
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/api"
	"github.com/saeedafri/sms-be/internal/domain/billing"
)

// Enabling a grey route must take more than pressing the button.
//
// A grey route reaches handsets without being registered with the operator
// behind it. It delivers until the carrier notices, and then messages are
// filtered with no report and the customer's sender id is blocked — under DLT
// the penalty lands on their principal entity, not on us. The console offered
// the toggle as if it were any other, and two grey routes were found carrying
// production traffic on 2026-08-21 with registered alternatives sitting in the
// same corridor. Nobody chose that; it was just the easiest button to press.
func TestEnablingAGreyRouteIsRefusedUnlessTheDeploymentAllowsIt(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	grey := h.seedRoute("grey", 90)
	registered := h.seedRoute("registered", 91)

	refused := h.do(http.MethodPost, "/v1/operator/routes/"+grey+"/enable", operator, nil)
	if refused.Code != http.StatusUnprocessableEntity {
		t.Fatalf("enabling a grey route = %d, want 422\n%s", refused.Code, refused.Body)
	}
	if status := h.routeStatus(grey); status != "disabled" {
		t.Fatalf("the grey route is %q after a refused enable", status)
	}

	// The guard has to be about the standing, not about route enabling. A
	// registered route in the same corridor still enables normally.
	allowed := h.do(http.MethodPost, "/v1/operator/routes/"+registered+"/enable", operator, nil)
	if allowed.Code != http.StatusOK {
		t.Fatalf("enabling a registered route = %d, want 200\n%s", allowed.Code, allowed.Body)
	}
}

// The escape hatch has to work, or a deployment that genuinely needs a grey
// route has no way to say so and someone edits the database by hand instead.
func TestAGreyRouteEnablesWhenTheDeploymentSaysSo(t *testing.T) {
	h := newHarness(t)
	h.router = api.NewRouter(&api.Server{
		DB: h.pool, OperatorDB: h.operatorPool, AdminDB: h.admin,
		EnableDevEndpoints: true, Gateway: billing.ManualGateway{},
		AllowGreyRoutes: true,
		Logger:          slog.New(slog.NewJSONHandler(h.logs, nil)),
	})
	operator := h.operatorToken()
	grey := h.seedRoute("grey", 90)

	enabled := h.do(http.MethodPost, "/v1/operator/routes/"+grey+"/enable", operator, nil)
	if enabled.Code != http.StatusOK {
		t.Fatalf("enable with ALLOW_GREY_ROUTES = %d, want 200\n%s", enabled.Code, enabled.Body)
	}
	if status := h.routeStatus(grey); status != "active" {
		t.Fatalf("route is %q after an allowed enable, want active", status)
	}
}

// Routes are global rather than tenant-scoped, so the test seeds and removes its
// own rather than depending on the demo fixture being present.
func (h *harness) seedRoute(standing string, priority int) string {
	h.t.Helper()
	id := uuid.New()
	if _, err := h.admin.Exec(context.Background(), `
		INSERT INTO routes (id, country, channel, carrier, label, priority,
		                    compliance_standing, cost_per_segment_minor, currency, status)
		VALUES ($1, 'IN', 'SMS', 'JIO', $2, $4, $3, 9, 'INR', 'disabled')`,
		id, "Guard fixture "+standing+" "+id.String(), standing, priority); err != nil {
		h.t.Fatalf("seed route: %v", err)
	}
	h.t.Cleanup(func() {
		_, _ = h.admin.Exec(context.Background(), `DELETE FROM routes WHERE id = $1`, id)
	})
	return id.String()
}

func (h *harness) routeStatus(id string) string {
	h.t.Helper()
	var status string
	if err := h.admin.QueryRow(context.Background(),
		`SELECT status FROM routes WHERE id = $1`, id).Scan(&status); err != nil {
		h.t.Fatalf("read route status: %v", err)
	}
	return status
}

// Until this shipped, a corridor could only be changed by editing the table by
// hand: the console listed routes, reordered them and toggled them, and had no
// way to add the path an operator had just signed a carrier contract for.
func TestAnOperatorCanAddAndRemoveARoute(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()

	created := h.do(http.MethodPost, "/v1/operator/routes", operator, map[string]any{
		"country": "AE", "channel": "SMS", "carrier": "DU",
		"label": "du via Aggregator Z", "complianceStanding": "registered",
		"costPerSegmentMinor": 4, "currency": "AED",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create route = %d, want 201\n%s", created.Code, created.Body)
	}
	var route struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Priority int    `json:"priority"`
	}
	created.decode(t, &route)
	t.Cleanup(func() {
		_, _ = h.admin.Exec(context.Background(), `DELETE FROM routes WHERE id = $1`, route.ID)
	})

	// Disabled, not live. A route that carried traffic the moment it was typed
	// would put real messages on a carrier connection nobody had tested.
	if route.Status != "disabled" {
		t.Errorf("a new route is %q, want disabled", route.Status)
	}

	// Active routes are not tidied away by accident.
	if err := h.enableRoute(route.ID); err != nil {
		t.Fatalf("enable: %v", err)
	}
	refused := h.do(http.MethodDelete, "/v1/operator/routes/"+route.ID, operator, nil)
	if refused.Code != http.StatusUnprocessableEntity {
		t.Fatalf("deleting an active route = %d, want 422\n%s", refused.Code, refused.Body)
	}

	if disabled := h.do(http.MethodPost, "/v1/operator/routes/"+route.ID+"/disable",
		operator, nil); disabled.Code != http.StatusOK {
		t.Fatalf("disable = %d\n%s", disabled.Code, disabled.Body)
	}
	removed := h.do(http.MethodDelete, "/v1/operator/routes/"+route.ID, operator, nil)
	if removed.Code != http.StatusNoContent {
		t.Fatalf("delete route = %d, want 204\n%s", removed.Code, removed.Body)
	}
	if h.routeExists(route.ID) {
		t.Fatal("the route is still there after a 204")
	}
}

// An out-of-enum value must be refused at the boundary. oapi-codegen renders
// the contract's enums as plain Go string aliases, so without this the value
// reaches the CHECK constraint and a typo becomes a 500.
func TestCreatingARouteRefusesValuesTheContractDoesNotDefine(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	valid := map[string]any{
		"country": "AE", "channel": "SMS", "carrier": "DU", "label": "Bad input",
		"complianceStanding": "registered", "costPerSegmentMinor": 4, "currency": "AED",
	}
	for field, bad := range map[string]any{
		"country": "ZZ", "channel": "PIGEON", "carrier": "NOTACARRIER",
		"complianceStanding": "dodgy", "currency": "XXX", "label": "  ",
	} {
		body := map[string]any{}
		for k, v := range valid {
			body[k] = v
		}
		body[field] = bad
		res := h.do(http.MethodPost, "/v1/operator/routes", operator, body)
		if res.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %v gave %d, want 422\n%s", field, bad, res.Code, res.Body)
		}
	}
}

// Priorities in a carrier's group are an order the console renders. A hole in
// it reads as a route somebody cannot see.
func TestRemovingARouteClosesTheGapItLeaves(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()

	ids := make([]string, 0, 3)
	for _, label := range []string{"First", "Second", "Third"} {
		res := h.do(http.MethodPost, "/v1/operator/routes", operator, map[string]any{
			"country": "AE", "channel": "VOICE", "carrier": "DU", "label": label,
			"complianceStanding": "registered", "costPerSegmentMinor": 7, "currency": "AED",
		})
		if res.Code != http.StatusCreated {
			t.Fatalf("create %s = %d\n%s", label, res.Code, res.Body)
		}
		var route struct {
			ID string `json:"id"`
		}
		res.decode(t, &route)
		ids = append(ids, route.ID)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = h.admin.Exec(context.Background(), `DELETE FROM routes WHERE id = $1`, id)
		}
	})

	if removed := h.do(http.MethodDelete, "/v1/operator/routes/"+ids[0], operator, nil); removed.Code != http.StatusNoContent {
		t.Fatalf("delete = %d\n%s", removed.Code, removed.Body)
	}
	for index, id := range ids[1:] {
		want := index + 1
		if got := h.routePriority(id); got != want {
			t.Errorf("route %d is priority %d after the delete, want %d — the order has a hole in it",
				index+2, got, want)
		}
	}
}

func (h *harness) enableRoute(id string) error {
	h.t.Helper()
	_, err := h.admin.Exec(context.Background(),
		`UPDATE routes SET status = 'active' WHERE id = $1`, id)
	return err
}

func (h *harness) routeExists(id string) bool {
	h.t.Helper()
	var exists bool
	if err := h.admin.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM routes WHERE id = $1)`, id).Scan(&exists); err != nil {
		h.t.Fatalf("route exists: %v", err)
	}
	return exists
}

func (h *harness) routePriority(id string) int {
	h.t.Helper()
	var priority int
	if err := h.admin.QueryRow(context.Background(),
		`SELECT priority FROM routes WHERE id = $1`, id).Scan(&priority); err != nil {
		h.t.Fatalf("route priority: %v", err)
	}
	return priority
}

// The rate card's cost reference has to be a number, or an operator prices
// against nothing.
//
// It came back null on all fourteen rates, which read as missing carrier data.
// The data was there the whole time: the filter compared route status against
// "enabled", a word routes have not held since migration 00029 renamed it to
// "active", so it excluded every route in the table.
func TestTheRateCardReportsACostReferenceFromActiveRoutes(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	id := h.seedRoute("registered", 92)
	if err := h.enableRoute(id); err != nil {
		t.Fatalf("enable: %v", err)
	}

	card := h.do(http.MethodGet, "/v1/operator/rates", operator, nil)
	if card.Code != http.StatusOK {
		t.Fatalf("rate card = %d\n%s", card.Code, card.Body)
	}
	var page struct {
		Defaults []struct {
			Country            string `json:"country"`
			Channel            string `json:"channel"`
			CostReferenceMinor *int   `json:"costReferenceMinor"`
		} `json:"defaults"`
	}
	card.decode(t, &page)

	found := false
	for _, rate := range page.Defaults {
		if rate.Country != "IN" || rate.Channel != "SMS" {
			continue
		}
		found = true
		if rate.CostReferenceMinor == nil {
			t.Fatal("IN/SMS has an active route and still reports no cost reference — " +
				"the operator is pricing against nothing")
		}
	}
	if !found {
		t.Skip("no IN/SMS default rate in this database")
	}
}
