package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func createConnection(t *testing.T, h *harness, operator string, overrides map[string]any) response {
	t.Helper()
	// system_id is unique per run: the table's (carrier, environment, host,
	// system_id) key is real, and a fixed fixture makes the SECOND run of any
	// spec fail with a 409 that has nothing to do with what it is testing.
	body := map[string]any{
		"label": "Airtel Test " + uuid.NewString()[:8], "carrier": "AIRTEL", "environment": "test",
		"host": "smpp.example", "port": 2775,
		"systemId": "textify-" + uuid.NewString(),
		"bindType": "transceiver", "password": "s3cret-bind-pw", "maxTps": 50,
	}
	for k, v := range overrides {
		body[k] = v
	}
	return h.do(http.MethodPost, "/v1/operator/connections", operator, body)
}

// The bind password is write-only, everywhere, always.
//
// These routes carry SMPP credentials for four carrier relationships — a
// materially worse thing to leak than an API key. There is no password property
// on the Connection response schema at all: not masked, not truncated, not
// present. The only thing a reader learns is when it was last set.
func TestABindPasswordIsNeverReturnedByAnyReadPath(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()

	label := "Airtel Test " + uuid.NewString()[:8]
	created := createConnection(t, h, operator, map[string]any{"label": label})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d\n%s", created.Code, created.Body)
	}
	if strings.Contains(string(created.Body), "s3cret-bind-pw") {
		t.Fatalf("the create response echoed the password: %s", created.Body)
	}
	if strings.Contains(string(created.Body), `"password"`) {
		t.Errorf("the create response carries a password field: %s", created.Body)
	}
	var connection struct {
		Id            string  `json:"id"`
		Status        string  `json:"status"`
		PasswordSetAt *string `json:"passwordSetAt"`
	}
	created.decode(t, &connection)

	// Created disabled regardless of anything in the request: a bind that went
	// live the moment it was typed would put traffic on an untested path.
	if connection.Status != "disabled" {
		t.Errorf("status = %q, want disabled", connection.Status)
	}
	if connection.PasswordSetAt == nil {
		t.Error("passwordSetAt is null after supplying a password")
	}

	for _, path := range []string{
		"/v1/operator/connections",
		"/v1/operator/connections/" + connection.Id,
	} {
		res := h.do(http.MethodGet, path, operator, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s = %d\n%s", path, res.Code, res.Body)
		}
		if strings.Contains(string(res.Body), "s3cret-bind-pw") ||
			strings.Contains(string(res.Body), `"password"`) {
			t.Errorf("GET %s leaked the password: %s", path, res.Body)
		}
	}

	// Nor may it reach the audit log's detail string.
	//
	// Scoped to this connection's own row: operator_audit_log is append-only, so
	// a row written by an earlier run cannot be cleaned up, and asserting across
	// every connection.create would make this spec report someone else's history.
	audit := h.do(http.MethodGet, "/v1/operator/audit-log?range=90d&action=connection.create",
		operator, nil)
	var log struct {
		Entries []struct {
			TargetLabel string `json:"targetLabel"`
			Detail      string `json:"detail"`
		} `json:"entries"`
	}
	audit.decode(t, &log)
	found := false
	for _, entry := range log.Entries {
		if entry.TargetLabel != label {
			continue
		}
		found = true
		if strings.Contains(entry.Detail, "s3cret-bind-pw") {
			t.Errorf("the audit detail carries the password: %s", entry.Detail)
		}
	}
	if !found {
		t.Error("the connection.create action was not audited")
	}
}

// A frontend gate stops a button being drawn, not a request being sent.
func TestACustomerTokenCannotReachAnyConnectionRoute(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	acct := h.newAccount("owner")

	created := createConnection(t, h, operator, map[string]any{"systemId": "rbac-" + uuid.NewString()})
	if created.Code != http.StatusCreated {
		t.Fatalf("seed connection = %d\n%s", created.Code, created.Body)
	}
	var connection struct {
		Id string `json:"id"`
	}
	created.decode(t, &connection)

	for _, route := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/operator/connections", nil},
		{http.MethodPost, "/v1/operator/connections", map[string]any{"label": "x"}},
		{http.MethodGet, "/v1/operator/connections/" + connection.Id, nil},
		{http.MethodPatch, "/v1/operator/connections/" + connection.Id, map[string]any{"label": "x"}},
		{http.MethodDelete, "/v1/operator/connections/" + connection.Id, nil},
		{http.MethodPost, "/v1/operator/connections/" + connection.Id + "/enable", nil},
		{http.MethodPost, "/v1/operator/connections/" + connection.Id + "/disable", nil},
		{http.MethodPost, "/v1/operator/connections/" + connection.Id + "/test", nil},
	} {
		res := h.do(route.method, route.path, acct.Token, route.body)
		if res.Code != http.StatusUnauthorized && res.Code != http.StatusForbidden {
			t.Errorf("%s %s with a customer token = %d, want 401/403\n%s",
				route.method, route.path, res.Code, res.Body)
		}
		if strings.Contains(string(res.Body), "s3cret") {
			t.Errorf("%s %s leaked a credential to a customer token", route.method, route.path)
		}
	}
}

// additionalProperties: false, on BOTH paths. The asymmetry — enforced on one,
// silently ignored on the other — is what shipped last time.
func TestAnUnknownFieldIsRejectedOnCreateAndOnUpdate(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()

	junk := createConnection(t, h, operator, map[string]any{
		"systemId": "junk-" + uuid.NewString(), "nope": "should-be-rejected",
	})
	if junk.Code != http.StatusUnprocessableEntity {
		t.Errorf("create with an unknown field = %d, want 422\n%s", junk.Code, junk.Body)
	}

	created := createConnection(t, h, operator, map[string]any{"systemId": "patch-" + uuid.NewString()})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d\n%s", created.Code, created.Body)
	}
	var connection struct {
		Id string `json:"id"`
	}
	created.decode(t, &connection)

	patch := h.do(http.MethodPatch, "/v1/operator/connections/"+connection.Id, operator,
		map[string]any{"label": "Renamed", "nope": "should-be-rejected"})
	if patch.Code != http.StatusUnprocessableEntity {
		t.Errorf("update with an unknown field = %d, want 422\n%s", patch.Code, patch.Body)
	}

	// A known field must still be accepted on the same path.
	ok := h.do(http.MethodPatch, "/v1/operator/connections/"+connection.Id, operator,
		map[string]any{"label": "Renamed"})
	if ok.Code != http.StatusOK {
		t.Fatalf("a valid update was refused: %d\n%s", ok.Code, ok.Body)
	}
}

// Enabling, disabling and deleting are each their own decision with their own
// refusals.
func TestConnectionLifecycleRefusals(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()

	created := createConnection(t, h, operator, map[string]any{"systemId": "lifecycle-" + uuid.NewString()})
	var connection struct {
		Id string `json:"id"`
	}
	created.decode(t, &connection)
	base := "/v1/operator/connections/" + connection.Id

	if res := h.do(http.MethodPost, base+"/enable", operator, nil); res.Code != http.StatusOK {
		t.Fatalf("enable = %d\n%s", res.Code, res.Body)
	}
	// Already active.
	if res := h.do(http.MethodPost, base+"/enable", operator, nil); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("enabling an active connection = %d, want 422", res.Code)
	}
	// Deleting an active bind must refuse — disable is a deliberate first step.
	if res := h.do(http.MethodDelete, base, operator, nil); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("deleting an active connection = %d, want 422\n%s", res.Code, res.Body)
	}
	if res := h.do(http.MethodPost, base+"/disable", operator, nil); res.Code != http.StatusOK {
		t.Fatalf("disable = %d\n%s", res.Code, res.Body)
	}
	if res := h.do(http.MethodDelete, base, operator, nil); res.Code != http.StatusNoContent {
		t.Errorf("delete = %d, want 204\n%s", res.Code, res.Body)
	}
	if res := h.do(http.MethodGet, base, operator, nil); res.Code != http.StatusNotFound {
		t.Errorf("the deleted connection is still readable: %d", res.Code)
	}
}

// Testing a bind reports reachability and must never change status. Proving a
// bind works and putting live traffic on it stay two separate decisions.
func TestTestingAConnectionNeverChangesItsStatus(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()

	created := createConnection(t, h, operator, map[string]any{
		"systemId": "probe-" + uuid.NewString(), "host": "127.0.0.1", "port": 9,
	})
	var connection struct {
		Id     string `json:"id"`
		Status string `json:"status"`
	}
	created.decode(t, &connection)

	res := h.do(http.MethodPost,
		fmt.Sprintf("/v1/operator/connections/%s/test", connection.Id), operator, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("test = %d\n%s", res.Code, res.Body)
	}

	after := h.do(http.MethodGet, "/v1/operator/connections/"+connection.Id, operator, nil)
	var reread struct {
		Status string `json:"status"`
		Health struct {
			Status string `json:"status"`
		} `json:"health"`
	}
	after.decode(t, &reread)
	if reread.Status != "disabled" {
		t.Errorf("testing changed status to %q — it must stay disabled", reread.Status)
	}
	if reread.Health.Status == "" {
		t.Error("health was not recorded")
	}
}
