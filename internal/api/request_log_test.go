package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A refused request's log line says who made it and why it was refused, so a
// failure a customer reports can be explained from CloudWatch alone.
func TestARefusedRequestIsLoggedWithItsCallerAndReason(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tenant := h.newAccount("owner")

	res := h.do(http.MethodGet, "/v1/campaigns?page=0", tenant.Token, nil)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("page=0 = %d, want 422", res.Code)
	}

	for _, line := range strings.Split(h.logs.String(), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil || entry["route"] != "/v1/campaigns" {
			continue
		}
		if entry["error_code"] == nil || entry["error_message"] == nil {
			t.Errorf("no error_code/error_message on the refused request: %s", line)
		}
		if entry["tenant_id"] != tenant.TenantID.String() || entry["auth"] != "session" {
			t.Errorf("caller not on the line (want tenant %s, auth session): %s", tenant.TenantID, line)
		}
		if entry["query"] != "page=0" {
			t.Errorf("query = %v, want page=0", entry["query"])
		}
		return
	}
	t.Fatalf("no request line for /v1/campaigns in:\n%s", h.logs.String())
}
