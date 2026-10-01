package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestFrequencyCapsRoundTripAndClear(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	owner := h.newAccount("owner")

	if res := h.do(http.MethodGet, "/v1/frequency-caps", owner.Token, nil); res.Code != 200 ||
		string(res.Body) != "{\"caps\":[]}\n" {
		t.Fatalf("fresh tenant = %d %s, want an empty list", res.Code, res.Body)
	}

	body := map[string]any{"dailyLimit": 2, "monthlyLimit": 10,
		"excludedNumbers": []string{"+91 98100 00201", "+919810000201", "+919810000200"}}
	res := h.do(http.MethodPut, "/v1/frequency-caps/SMS", owner.Token, body)
	if res.Code != 200 {
		t.Fatalf("put = %d %s", res.Code, res.Body)
	}
	var saved struct {
		Channel         string   `json:"channel"`
		DailyLimit      *int     `json:"dailyLimit"`
		WeeklyLimit     *int     `json:"weeklyLimit"`
		MonthlyLimit    *int     `json:"monthlyLimit"`
		ExcludedNumbers []string `json:"excludedNumbers"`
	}
	if err := json.Unmarshal(res.Body, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Channel != "SMS" || *saved.DailyLimit != 2 || saved.WeeklyLimit != nil ||
		*saved.MonthlyLimit != 10 || len(saved.ExcludedNumbers) != 2 ||
		saved.ExcludedNumbers[0] != "+919810000200" {
		t.Errorf("saved = %+v, want normalised, de-duplicated, sorted exclusions", saved)
	}
	var list struct{ Caps []map[string]any }
	res = h.do(http.MethodGet, "/v1/frequency-caps", owner.Token, nil)
	_ = json.Unmarshal(res.Body, &list)
	if len(list.Caps) != 1 || list.Caps[0]["channel"] != "SMS" {
		t.Errorf("list = %s, want the one SMS cap", res.Body)
	}

	// Clearing every limit and every exclusion is removing the cap.
	if res := h.do(http.MethodPut, "/v1/frequency-caps/SMS", owner.Token,
		map[string]any{}); res.Code != 200 {
		t.Fatalf("clear = %d %s", res.Code, res.Body)
	}
	res = h.do(http.MethodGet, "/v1/frequency-caps", owner.Token, nil)
	if string(res.Body) != "{\"caps\":[]}\n" {
		t.Errorf("after clearing = %s, want an empty list", res.Body)
	}
}

func TestFrequencyCapsAreRefusedBadInput(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	owner := h.newAccount("owner")
	put := func(channel string, body any) response {
		return h.do(http.MethodPut, "/v1/frequency-caps/"+channel, owner.Token, body)
	}
	for name, res := range map[string]response{
		"zero limit":         put("SMS", map[string]any{"dailyLimit": 0}),
		"negative limit":     put("SMS", map[string]any{"weeklyLimit": -3}),
		"huge limit":         put("SMS", map[string]any{"dailyLimit": 100001}),
		"daily over weekly":  put("SMS", map[string]any{"dailyLimit": 5, "weeklyLimit": 3}),
		"weekly over month":  put("SMS", map[string]any{"weeklyLimit": 9, "monthlyLimit": 4}),
		"email channel":      put("EMAIL", map[string]any{"dailyLimit": 1}),
		"unknown channel":    put("TELEPATHY", map[string]any{"dailyLimit": 1}),
		"channel mismatch":   put("SMS", map[string]any{"channel": "RCS", "dailyLimit": 1}),
		"unknown field":      put("SMS", map[string]any{"dailyLimit": 1, "hourlyLimit": 1}),
		"bad excluded":       put("SMS", map[string]any{"excludedNumbers": []string{"abc"}}),
		"string for a limit": put("SMS", map[string]any{"dailyLimit": "two"}),
	} {
		if res.Code != http.StatusUnprocessableEntity || res.errorCode(t) != "validation_failed" {
			t.Errorf("%s = %d %s, want 422 validation_failed", name, res.Code, res.Body)
		}
	}
	tooMany := make([]string, 1001)
	for i := range tooMany {
		tooMany[i] = "+9198100" + string(rune('0'+i/100%10)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10)) + "00"
	}
	if res := put("SMS", map[string]any{"excludedNumbers": tooMany}); res.Code != 422 {
		t.Errorf("1001 exclusions = %d, want 422", res.Code)
	}
}

func TestOnlyAnAdminMayChangeFrequencyCapsButAMemberMayRead(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	member := h.newAccount("member")
	if res := h.do(http.MethodPut, "/v1/frequency-caps/SMS", member.Token,
		map[string]any{"dailyLimit": 1}); res.Code != http.StatusForbidden {
		t.Errorf("member put = %d %s, want 403", res.Code, res.Body)
	}
	if res := h.do(http.MethodGet, "/v1/frequency-caps", member.Token, nil); res.Code != 200 {
		t.Errorf("member get = %d, want 200", res.Code)
	}
	if res := h.do(http.MethodGet, "/v1/frequency-caps", "", nil); res.Code != http.StatusUnauthorized {
		t.Errorf("anonymous get = %d, want 401", res.Code)
	}
	if res := h.do(http.MethodPut, "/v1/frequency-caps/SMS", "", map[string]any{}); res.Code != http.StatusUnauthorized {
		t.Errorf("anonymous put = %d, want 401", res.Code)
	}
}

// One tenant's cap is invisible to another.
func TestFrequencyCapsAreIsolatedBetweenTenants(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	one, two := h.newAccount("owner"), h.newAccount("owner")
	h.do(http.MethodPut, "/v1/frequency-caps/SMS", one.Token, map[string]any{"dailyLimit": 3})
	res := h.do(http.MethodGet, "/v1/frequency-caps", two.Token, nil)
	if string(res.Body) != "{\"caps\":[]}\n" {
		t.Errorf("tenant two sees %s", res.Body)
	}
}
