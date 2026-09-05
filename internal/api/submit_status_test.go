package api_test

import (
	"net/http"
	"testing"
)

// A refusal at submit and a failure in delivery are the two moments a customer
// tells apart — "you would not take it" versus "you took it and it did not
// arrive" — and they lead to different fixes.
//
// They were reported under two spellings: the early refusals said `rejected`
// and the gate said `failed`, so a client could not branch on "refused" at all.
// Submit-time refusals are now always `rejected`.
func TestEverySubmitTimeRefusalIsReportedAsRejected(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.registeredTemplate(tenant, sender, "Hi {{first_name}}, your order shipped.")
	h.fundWallet(tenant)

	cases := []struct {
		name    string
		payload map[string]any
		code    string
	}{
		{"no template where the regime requires one",
			map[string]any{"senderId": sender, "to": "9876500501", "body": "Hello."},
			"registered_template_required"},
		{"body that is not the template",
			map[string]any{"senderId": sender, "templateId": template,
				"to": "9876500502", "body": "Nothing like the template."},
			"template_body_mismatch"},
		{"content the country bans",
			map[string]any{"senderId": sender, "templateId": template,
				"to": "9876500503", "body": "Claim now http://bit.ly/abc"},
			"content_not_allowed"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := h.do(http.MethodPost, "/v1/messages", tenant.Token, testCase.payload)
			if response.Code != http.StatusAccepted {
				t.Fatalf("send = %d, want 202\n%s", response.Code, response.Body)
			}
			var out struct {
				Status    string  `json:"status"`
				ErrorCode *string `json:"errorCode"`
				CostMinor int64   `json:"costMinor"`
			}
			response.decode(t, &out)
			if out.Status != "rejected" {
				t.Fatalf("status = %q, want rejected — a submit-time refusal and a "+
					"delivery failure must not share a spelling", out.Status)
			}
			if out.ErrorCode == nil || *out.ErrorCode != testCase.code {
				t.Fatalf("errorCode = %v, want %s", out.ErrorCode, testCase.code)
			}
			if out.CostMinor != 0 {
				t.Fatalf("costMinor = %d on a refusal", out.CostMinor)
			}
		})
	}
}

// A suppressed recipient is the gate refusing, which used to be the one that
// said "failed".
func TestASuppressedRecipientIsRejectedNotFailed(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)

	suppressed := "+919876500504"
	added := h.do(http.MethodPost, "/v1/suppressions", tenant.Token,
		map[string]any{"msisdns": []string{suppressed}, "reason": "manual"})
	if added.Code != http.StatusCreated && added.Code != http.StatusOK {
		t.Fatalf("suppress = %d\n%s", added.Code, added.Body)
	}

	response := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": sender, "templateId": template, "to": suppressed, "body": "Hello."})
	var out struct {
		Status    string  `json:"status"`
		ErrorCode *string `json:"errorCode"`
	}
	response.decode(t, &out)
	if out.Status != "rejected" {
		t.Fatalf("status = %q (%v), want rejected", out.Status, out.ErrorCode)
	}
}

// The other half of the distinction: an accepted send still reports `sent`, and
// the message log — whose enum has no `rejected` — still reads `failed` for a
// refused message, because MessageStatus cannot spell it.
func TestASendResultAndTheLogDescribeAMessageTheSameWay(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)

	sent := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": sender, "templateId": template, "to": "9876500505", "body": "Real send."})
	var accepted struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	sent.decode(t, &accepted)
	if accepted.Status != "sent" {
		t.Fatalf("status = %q, want sent", accepted.Status)
	}

	refused := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": sender, "to": "9876500506", "body": "No template."})
	var rejected struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	refused.decode(t, &rejected)
	if rejected.Status != "rejected" {
		t.Fatalf("status = %q, want rejected", rejected.Status)
	}

	// The same message must read `rejected` in the log too. It read `failed`
	// until MessageStatus gained the value, which meant "we would not take it"
	// and "we took it and it did not arrive" were the same word to anyone
	// reading the log.
	page := h.do(http.MethodGet, "/v1/messages?limit=50", tenant.Token, nil)
	var log struct {
		Messages []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"messages"`
	}
	page.decode(t, &log)
	for _, row := range log.Messages {
		if row.ID == rejected.ID && row.Status != "rejected" {
			t.Fatalf("the log reports %q for a refused message, want rejected — "+
				"the log and the send result must not describe one message two ways",
				row.Status)
		}
	}
}
