package api_test

import (
	"strings"
	"testing"
)

type opDLR struct {
	Stat        string  `json:"stat"`
	ErrorCode   *string `json:"errorCode"`
	SubmittedAt *string `json:"submittedAt"`
	DoneAt      *string `json:"doneAt"`
	ReceivedAt  *string `json:"receivedAt"`
	Raw         *string `json:"raw"`
}

type opTextMessage struct {
	ID           string  `json:"id"`
	To           string  `json:"to"`
	Status       string  `json:"status"`
	TemplateID   *string `json:"templateId"`
	TemplateName *string `json:"templateName"`
	RenderedText *string `json:"renderedText"`
	DLR          *opDLR  `json:"dlr"`
}

// Handoff 2026-09-29 A1, A4, A5, A6, A10: what the handset was sent, and what
// the carrier said about it, on a real campaign against the sandbox.
func TestOperatorSeesTheTextSentAndTheCarriersReceipt(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acme := h.newAccount("owner")
	h.fundWallet(acme)
	ops := h.operatorToken()

	delivered, absent := "+919877520010", "+919877520001"
	campaignID := h.launchCampaign(acme, ops, "Receipt check", []string{delivered, absent})

	var page struct {
		Messages []opTextMessage `json:"messages"`
	}
	// Before the carrier reports: sent, and no receipt yet (A6).
	h.operatorGet(ops, "/v1/operator/messages?campaignId="+campaignID, &page)
	for _, m := range page.Messages {
		if m.DLR != nil {
			t.Errorf("%s: dlr %+v before any receipt, want null", m.To, m.DLR)
		}
	}

	h.drainSandbox()
	h.operatorGet(ops, "/v1/operator/messages?campaignId="+campaignID, &page)
	if len(page.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(page.Messages))
	}
	for _, m := range page.Messages {
		if m.RenderedText == nil || *m.RenderedText == "" || strings.Contains(*m.RenderedText, "{{") {
			t.Errorf("%s: renderedText = %v, want the filled text", m.To, m.RenderedText)
		}
		if m.TemplateID != nil && (m.TemplateName == nil || *m.TemplateName == "") {
			t.Errorf("%s: templateName missing for template %s", m.To, *m.TemplateID)
		}
		if m.DLR == nil {
			t.Errorf("%s: %s with no dlr", m.To, m.Status)
			continue
		}
		switch {
		case strings.HasSuffix(m.To, "010") && m.DLR.Stat != "DELIVRD":
			t.Errorf("delivered message dlr.stat = %q, want DELIVRD", m.DLR.Stat)
		case strings.HasSuffix(m.To, "001") && m.DLR.Stat != "UNDELIV":
			t.Errorf("absent subscriber dlr.stat = %q, want UNDELIV", m.DLR.Stat)
		}
		if m.DLR.ReceivedAt == nil {
			t.Errorf("%s: dlr.receivedAt missing", m.To)
		}
	}

	// A10: the campaign by id, and nothing for an unknown id.
	var one struct {
		Total     int          `json:"total"`
		Campaigns []opCampaign `json:"campaigns"`
	}
	h.operatorGet(ops, "/v1/operator/campaigns?campaignId="+campaignID, &one)
	if one.Total != 1 || len(one.Campaigns) != 1 || one.Campaigns[0].ID != campaignID {
		t.Errorf("campaignId filter = %+v, want exactly the campaign", one)
	}
	h.operatorGet(ops, "/v1/operator/campaigns?campaignId=00000000-0000-4000-8000-000000000000", &one)
	if one.Total != 0 || len(one.Campaigns) != 0 {
		t.Errorf("unknown campaignId = %+v, want an empty page", one)
	}
}
