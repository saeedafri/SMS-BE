package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestDripSettingsCanBeSetReadAndCleared(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	id := h.seedNamedCampaign(tenant, "Drip me", "scheduled")
	path := "/v1/campaigns/" + id + "/drip"

	res := h.do(http.MethodPut, path, tenant.Token, map[string]any{"batchSize": 100, "intervalMinutes": 15})
	var out struct {
		Dripping        bool
		BatchSize       *int
		IntervalMinutes *int
	}
	_ = json.Unmarshal(res.Body, &out)
	if res.Code != 200 || !out.Dripping || *out.BatchSize != 100 || *out.IntervalMinutes != 15 {
		t.Fatalf("set = %d %s", res.Code, res.Body)
	}
	res = h.do(http.MethodGet, path, tenant.Token, nil)
	_ = json.Unmarshal(res.Body, &out)
	if res.Code != 200 || !out.Dripping {
		t.Errorf("get = %d %s", res.Code, res.Body)
	}
	res = h.do(http.MethodPut, path, tenant.Token, map[string]any{})
	_ = json.Unmarshal(res.Body, &out)
	if res.Code != 200 || out.Dripping {
		t.Errorf("clear = %d %s", res.Code, res.Body)
	}
}

func TestDripSettingsAreRefusedWhenWrongOrTooLate(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	scheduled := h.seedNamedCampaign(tenant, "Fresh", "scheduled")
	started := h.seedNamedCampaign(tenant, "Started", "sent")
	for name, body := range map[string]map[string]any{
		"batch only":      {"batchSize": 10},
		"interval only":   {"intervalMinutes": 10},
		"zero batch":      {"batchSize": 0, "intervalMinutes": 10},
		"huge batch":      {"batchSize": 100001, "intervalMinutes": 10},
		"zero interval":   {"batchSize": 10, "intervalMinutes": 0},
		"over a day":      {"batchSize": 10, "intervalMinutes": 1441},
		"string batch":    {"batchSize": "ten", "intervalMinutes": 10},
		"unknown field":   {"batchSize": 10, "intervalMinutes": 10, "pace": "slow"},
	} {
		if res := h.do(http.MethodPut, "/v1/campaigns/"+scheduled+"/drip", tenant.Token, body); res.Code != 422 {
			t.Errorf("%s = %d %s, want 422", name, res.Code, res.Body)
		}
	}
	if res := h.do(http.MethodPut, "/v1/campaigns/"+started+"/drip", tenant.Token,
		map[string]any{"batchSize": 10, "intervalMinutes": 10}); res.Code != http.StatusConflict {
		t.Errorf("a sent campaign = %d %s, want 409", res.Code, res.Body)
	}
	if res := h.do(http.MethodPut, "/v1/campaigns/"+uuid.NewString()+"/drip", tenant.Token,
		map[string]any{"batchSize": 10, "intervalMinutes": 10}); res.Code != 404 {
		t.Errorf("unknown campaign = %d, want 404", res.Code)
	}
	if res := h.do(http.MethodPut, "/v1/campaigns/nope/drip", tenant.Token, map[string]any{}); res.Code != 422 {
		t.Errorf("bad id = %d, want 422", res.Code)
	}
	other := h.newAccount("owner")
	if res := h.do(http.MethodPut, "/v1/campaigns/"+scheduled+"/drip", other.Token,
		map[string]any{"batchSize": 10, "intervalMinutes": 10}); res.Code != 404 {
		t.Errorf("another tenant's campaign = %d, want 404", res.Code)
	}
}

// The create call reads the drip fields off a body whose generated type does
// not declare them, and refuses half a pair before anything is created.
func TestCreatingACampaignValidatesItsDripFields(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	base := func(extra map[string]any) map[string]any {
		body := map[string]any{"name": "x", "channel": "SMS", "country": "IN",
			"senderId": uuid.NewString(), "templateId": uuid.NewString()}
		for k, v := range extra {
			body[k] = v
		}
		return body
	}
	for name, extra := range map[string]map[string]any{
		"batch only":   {"dripBatchSize": 10},
		"bad interval": {"dripBatchSize": 10, "dripIntervalMinutes": 5000},
		"wrong type":   {"dripBatchSize": "ten", "dripIntervalMinutes": 10},
	} {
		res := h.do(http.MethodPost, "/v1/campaigns", tenant.Token, base(extra))
		if res.Code != 422 || !contains(string(res.Body), "drip") {
			t.Errorf("%s = %d %s, want a 422 about drip", name, res.Code, res.Body)
		}
	}
}
