package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestATestSendGoesThroughTheRealGateToEachNumberOnce(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)

	res := h.do(http.MethodPost, "/v1/messages/test", tenant.Token, map[string]any{
		"senderId": sender, "templateId": template, "body": "Your order has shipped.",
		"recipients": []string{"9810000501", "+91 98100 00502", "9810000501"}})
	var out struct {
		Results []struct {
			Recipient, Status, FailureCode string
			MessageID                      *string
		}
	}
	if err := json.Unmarshal(res.Body, &out); err != nil || res.Code != http.StatusAccepted {
		t.Fatalf("test send = %d %s", res.Code, res.Body)
	}
	if len(out.Results) != 2 {
		t.Fatalf("results = %s, want one per distinct number", res.Body)
	}
	for _, r := range out.Results {
		if r.MessageID == nil || r.Status == "rejected" || r.FailureCode != "" {
			t.Errorf("result %+v, want a sent message", r)
		}
	}
	list := h.do(http.MethodGet, "/v1/messages?limit=10", tenant.Token, nil)
	var page struct{ Total int }
	_ = json.Unmarshal(list.Body, &page)
	if page.Total != 2 {
		t.Errorf("message log has %d messages, want the 2 test sends", page.Total)
	}
}

func TestATestSendThatTheGateRefusesSaysWhy(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	// No wallet funded: every send is refused for balance.
	res := h.do(http.MethodPost, "/v1/messages/test", tenant.Token, map[string]any{
		"senderId": sender, "templateId": template, "body": "hi",
		"recipients": []string{"9810000503"}})
	var out struct{ Results []struct{ Status, FailureCode string } }
	_ = json.Unmarshal(res.Body, &out)
	if res.Code != 202 || len(out.Results) != 1 || out.Results[0].Status != "rejected" ||
		out.Results[0].FailureCode != "insufficient_balance" {
		t.Errorf("unfunded test send = %d %s", res.Code, res.Body)
	}
}

func TestTestSendRefusesWhatIsNotATest(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	six := []string{"9810000601", "9810000602", "9810000603", "9810000604", "9810000605", "9810000606"}
	for name, body := range map[string]map[string]any{
		"six recipients": {"senderId": sender, "body": "hi", "recipients": six},
		"none":           {"senderId": sender, "body": "hi", "recipients": []string{}},
		"bad number":     {"senderId": sender, "body": "hi", "recipients": []string{"abc"}},
		"no body":        {"senderId": sender, "recipients": []string{"9810000601"}},
		"no sender":      {"body": "hi", "recipients": []string{"9810000601"}},
		"unknown sender": {"senderId": uuid.NewString(), "body": "hi", "recipients": []string{"9810000601"}},
		"unknown field":  {"senderId": sender, "body": "hi", "recipients": []string{"9810000601"}, "audience": "all"},
	} {
		if res := h.do(http.MethodPost, "/v1/messages/test", tenant.Token, body); res.Code != 422 {
			t.Errorf("%s = %d %s, want 422", name, res.Code, res.Body)
		}
	}
	viewer := h.newAccount("viewer")
	if res := h.do(http.MethodPost, "/v1/messages/test", viewer.Token, map[string]any{}); res.Code != 403 {
		t.Errorf("viewer = %d, want 403", res.Code)
	}
	if res := h.do(http.MethodPost, "/v1/messages/test", "", map[string]any{}); res.Code != 401 {
		t.Errorf("anonymous = %d, want 401", res.Code)
	}
}
