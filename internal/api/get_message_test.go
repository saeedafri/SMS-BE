package api_test

import (
	"net/http"
	"testing"
)

// After a send returns an id, the next thing any integration does is ask what
// happened to it. Narrowing the list to find one row was the only way, which is
// a poor substitute and gets worse as the log grows.
func TestASentMessageCanBeReadBackById(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)

	sent := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": sender, "templateId": template,
		"to": "9876500701", "body": "Readable by id?"})
	var accepted struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	sent.decode(t, &accepted)
	if accepted.Status != "sent" {
		t.Fatalf("send status = %q\n%s", accepted.Status, sent.Body)
	}

	read := h.do(http.MethodGet, "/v1/messages/"+accepted.ID, tenant.Token, nil)
	if read.Code != http.StatusOK {
		t.Fatalf("read by id = %d\n%s", read.Code, read.Body)
	}
	var entry struct {
		ID       string  `json:"id"`
		Status   string  `json:"status"`
		Msisdn   string  `json:"msisdn"`
		Segments int     `json:"segments"`
		Channel  string  `json:"channel"`
		SentAt   *string `json:"sentAt"`
	}
	read.decode(t, &entry)
	if entry.ID != accepted.ID {
		t.Fatalf("id = %q, want %q", entry.ID, accepted.ID)
	}
	if entry.Status != "sent" {
		t.Fatalf("status = %q, want sent", entry.Status)
	}
	if entry.Channel != "SMS" {
		t.Fatalf("channel = %q", entry.Channel)
	}
	if entry.Segments != 1 {
		t.Fatalf("segments = %d", entry.Segments)
	}
}

// A refused message is still readable — that is the whole reason a refusal
// carries an id — and it carries the reason it was refused.
func TestARefusedMessageIsReadableByIdWithItsReason(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	h.fundWallet(tenant)

	refused := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": sender, "to": "9876500702", "body": "No template."})
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	refused.decode(t, &out)
	if out.Status != "rejected" {
		t.Fatalf("status = %q, want rejected", out.Status)
	}

	read := h.do(http.MethodGet, "/v1/messages/"+out.ID, tenant.Token, nil)
	if read.Code != http.StatusOK {
		t.Fatalf("read a refused message = %d\n%s", read.Code, read.Body)
	}
	var entry struct {
		Status    string  `json:"status"`
		ErrorCode *string `json:"errorCode"`
	}
	read.decode(t, &entry)
	// The LOG's vocabulary has no `rejected`, so a refusal reads `failed` here
	// while the send response said `rejected`. Asserted so the difference stays
	// deliberate.
	if entry.Status != "failed" {
		t.Fatalf("log status = %q, want failed", entry.Status)
	}
	if entry.ErrorCode == nil || *entry.ErrorCode != "registered_template_required" {
		t.Fatalf("errorCode = %v", entry.ErrorCode)
	}
}

func TestReadingAMessageThatIsNotYoursIs404(t *testing.T) {
	h := newSendHarness(t)
	mine := h.newAccount("owner")
	theirs := h.newAccount("owner")
	sender := h.approvedSender(theirs)
	template := h.wildcardTemplate(theirs, sender)
	h.fundWallet(theirs)

	sent := h.do(http.MethodPost, "/v1/messages", theirs.Token, map[string]any{
		"senderId": sender, "templateId": template,
		"to": "9876500703", "body": "Theirs."})
	var accepted struct {
		ID string `json:"id"`
	}
	sent.decode(t, &accepted)

	read := h.do(http.MethodGet, "/v1/messages/"+accepted.ID, mine.Token, nil)
	if read.Code != http.StatusNotFound {
		t.Fatalf("another tenant's message = %d, want 404 — the same answer as "+
			"an id that never existed\n%s", read.Code, read.Body)
	}

	missing := h.do(http.MethodGet,
		"/v1/messages/00000000-0000-0000-0000-000000000000", mine.Token, nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("a missing id = %d, want 404", missing.Code)
	}
}

// The same scope as the list, and enforced.
func TestReadingOneMessageNeedsTheReadMessagesScope(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)
	readKey := h.apiKey(tenant, []string{"send:sms", "read:messages"})
	sendKey := h.apiKey(tenant, []string{"send:sms"})

	sent := h.do(http.MethodPost, "/v1/messages", readKey, map[string]any{
		"senderId": sender, "templateId": template,
		"to": "9876500704", "body": "Poll me."})
	var accepted struct {
		ID string `json:"id"`
	}
	sent.decode(t, &accepted)

	withScope := h.do(http.MethodGet, "/v1/messages/"+accepted.ID, readKey, nil)
	if withScope.Code != http.StatusOK {
		t.Fatalf("read:messages key = %d, want 200\n%s", withScope.Code, withScope.Body)
	}
	withoutScope := h.do(http.MethodGet, "/v1/messages/"+accepted.ID, sendKey, nil)
	if withoutScope.Code != http.StatusForbidden {
		t.Fatalf("send-only key = %d, want 403\n%s", withoutScope.Code, withoutScope.Body)
	}
}
