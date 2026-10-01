package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/saeedafri/sms-be/internal/store"
)

type chatbotView struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Active   bool     `json:"active"`
	Keywords []string `json:"keywords"`
}

// replyTemplate is an approved template with a fixed text and no slots, which
// is the only kind a keyword reply can use.
func (h *harness) replyTemplate(tenant account, senderID string) string {
	h.t.Helper()
	var id string
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO templates (tenant_id, sender_id, name, channel, country, body, status)
		VALUES ($1, $2, $3, 'SMS', 'IN', '20% off this week.', 'approved') RETURNING id`,
		tenant.TenantID, senderID, "Reply "+uuid.NewString()[:8]).Scan(&id); err != nil {
		h.t.Fatalf("seed reply template: %v", err)
	}
	return id
}

func (h *harness) makeChatbot(tenant account, sender string, extra map[string]any) response {
	body := map[string]any{"name": "Offers", "channel": "SMS", "senderId": sender,
		"keywords":   []string{"OFFER", "  Deals "},
		"templateId": h.replyTemplate(tenant, sender)}
	for k, v := range extra {
		body[k] = v
	}
	return h.do(http.MethodPost, "/v1/chatbots", tenant.Token, body)
}

func (h *harness) outboundBodies(tenant account, msisdn string) []string {
	rows, err := h.admin.Query(context.Background(), `
		SELECT m.body FROM conversation_messages m
		JOIN conversations c ON c.id = m.conversation_id
		JOIN contacts ct ON ct.id = c.contact_id
		WHERE m.tenant_id = $1 AND ct.msisdn = $2 AND m.direction = 'outbound'`, tenant.TenantID, msisdn)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var body string
		_ = rows.Scan(&body)
		out = append(out, body)
	}
	return out
}

// A customer writes a keyword and the tenant's flow answers it, once, however
// it is cased or spaced; a different word and a STOP get nothing.
func TestAKeywordIsAnsweredOnceAndNothingElseIs(t *testing.T) {
	h := newSendHarness(t)
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(h.logs.String())
		}
	})
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	h.fundWallet(tenant)
	res := h.makeChatbot(tenant, sender, nil)
	var flow chatbotView
	_ = json.Unmarshal(res.Body, &flow)
	if res.Code != http.StatusCreated || len(flow.Keywords) != 2 || flow.Keywords[0] != "deals" || flow.Keywords[1] != "offer" {
		t.Fatalf("create = %d %s, want normalised keywords", res.Code, res.Body)
	}
	ctx := context.Background()
	identity := store.Identity{TenantID: tenant.TenantID}
	phone := "+919810000701"

	h.server.FileReply(ctx, identity, phone, "IN", "SMS", "  oFfEr ")
	eventually(t, "the bot's reply in the thread", func() bool {
		return len(h.outboundBodies(tenant, phone)) == 1
	})
	if got := h.outboundBodies(tenant, phone)[0]; got != "20% off this week." {
		t.Errorf("reply = %q", got)
	}

	// The same person, same minute, same word: no second answer.
	h.server.FileReply(ctx, identity, phone, "IN", "SMS", "offer")
	// A word no flow answers, and a STOP.
	h.server.FileReply(ctx, identity, "+919810000702", "IN", "SMS", "hello there")
	h.server.FileReply(ctx, identity, "+919810000703", "IN", "SMS", "STOP")
	// A different channel's flow does not answer SMS and vice versa.
	h.server.FileReply(ctx, identity, "+919810000704", "IN", "RCS", "offer")

	eventually(t, "the other inbound messages to be filed", func() bool {
		var n int
		_ = h.admin.QueryRow(ctx, `SELECT count(*) FROM conversation_messages
			WHERE tenant_id = $1 AND direction = 'inbound'`, tenant.TenantID).Scan(&n)
		return n == 5
	})
	// Replies are sent behind the inbound; give a second one every chance to
	// show up before saying there was only one.
	time.Sleep(3 * time.Second)
	for _, p := range []string{phone} {
		if n := len(h.outboundBodies(tenant, p)); n != 1 {
			t.Errorf("%s has %d bot replies, want exactly 1", p, n)
		}
	}
	for _, p := range []string{"+919810000702", "+919810000703", "+919810000704"} {
		if n := len(h.outboundBodies(tenant, p)); n != 0 {
			t.Errorf("%s got %d bot replies, want none", p, n)
		}
	}
}

func TestAnInactiveChatbotIsSilentAndDeletingOneFreesItsKeywords(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	h.fundWallet(tenant)
	var flow chatbotView
	_ = json.Unmarshal(h.makeChatbot(tenant, sender, nil).Body, &flow)

	if res := h.do(http.MethodPatch, "/v1/chatbots/"+flow.ID, tenant.Token, map[string]any{"active": false}); res.Code != 200 {
		t.Fatalf("pause = %d %s", res.Code, res.Body)
	}
	h.server.FileReply(context.Background(), store.Identity{TenantID: tenant.TenantID},
		"+919810000711", "IN", "SMS", "offer")
	eventually(t, "the inbound filed", func() bool {
		var n int
		_ = h.admin.QueryRow(context.Background(), `SELECT count(*) FROM conversation_messages
			WHERE tenant_id = $1`, tenant.TenantID).Scan(&n)
		return n == 1
	})
	if n := len(h.outboundBodies(tenant, "+919810000711")); n != 0 {
		t.Errorf("a paused chatbot replied %d times", n)
	}
	if res := h.do(http.MethodDelete, "/v1/chatbots/"+flow.ID, tenant.Token, nil); res.Code != 204 {
		t.Fatalf("delete = %d", res.Code)
	}
	if res := h.makeChatbot(tenant, sender, nil); res.Code != 201 {
		t.Errorf("the keywords were not freed: %d %s", res.Code, res.Body)
	}
}

func TestChatbotsRefuseWhatWouldGoWrong(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	if res := h.makeChatbot(tenant, sender, nil); res.Code != 201 {
		t.Fatalf("setup = %d %s", res.Code, res.Body)
	}
	long := strings.Repeat("a", 41)
	for name, tc := range map[string]struct {
		extra map[string]any
		want  int
	}{
		"keyword taken":       {map[string]any{"name": "Dup", "keywords": []string{"OFFER"}}, 409},
		"STOP is reserved":    {map[string]any{"keywords": []string{"stop"}}, 422},
		"UNSUBSCRIBE":         {map[string]any{"keywords": []string{"Unsubscribe"}}, 422},
		"no keywords":         {map[string]any{"keywords": []string{}}, 422},
		"blank keyword":       {map[string]any{"keywords": []string{"   "}}, 422},
		"long keyword":        {map[string]any{"keywords": []string{long}}, 422},
		"too many":            {map[string]any{"keywords": []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10", "a11", "a12", "a13", "a14", "a15", "a16", "a17", "a18", "a19", "a20", "a21"}}, 422},
		"template with slots": {map[string]any{"keywords": []string{"x0"}, "templateId": h.wildcardTemplate(tenant, sender)}, 422},
		"no template in IN":   {map[string]any{"keywords": []string{"x1"}, "templateId": nil, "replyBody": "hi"}, 422},
		"no reply at all":     {map[string]any{"keywords": []string{"x1b"}, "templateId": nil, "replyBody": nil}, 422},
		"reply too long":      {map[string]any{"keywords": []string{"x2"}, "replyBody": strings.Repeat("a", 1601)}, 422},
		"unknown sender":      {map[string]any{"keywords": []string{"x3"}, "senderId": uuid.NewString()}, 422},
		"wrong channel":       {map[string]any{"keywords": []string{"x4"}, "channel": "RCS"}, 422},
		"bad channel":         {map[string]any{"keywords": []string{"x5"}, "channel": "FAX"}, 422},
		"unknown field":       {map[string]any{"keywords": []string{"x6"}, "delay": 5}, 422},
	} {
		if res := h.makeChatbot(tenant, sender, tc.extra); res.Code != tc.want {
			t.Errorf("%s = %d %s, want %d", name, res.Code, res.Body, tc.want)
		}
	}
	member := h.newAccount("member")
	if res := h.makeChatbot(member, sender, nil); res.Code != 403 {
		t.Errorf("member = %d, want 403", res.Code)
	}
	if res := h.do(http.MethodGet, "/v1/chatbots", "", nil); res.Code != 401 {
		t.Errorf("anonymous = %d, want 401", res.Code)
	}
	other := h.newAccount("owner")
	var list struct{ Chatbots []chatbotView }
	_ = json.Unmarshal(h.do(http.MethodGet, "/v1/chatbots", other.Token, nil).Body, &list)
	if len(list.Chatbots) != 0 {
		t.Errorf("another tenant sees %d chatbots", len(list.Chatbots))
	}
}
