package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeJio is JBM's token host and messaging host on one test server.
type fakeJio struct {
	tokens atomic.Int32
	send   http.HandlerFunc
}

func jioStub(t *testing.T, fake *fakeJio) *JioRCS {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth/token" {
			q := r.URL.Query()
			if r.Method != http.MethodGet || q.Get("grant_type") != "client_credentials" ||
				q.Get("scope") != "read" {
				t.Errorf("token call = %s %s, want GET with grant_type and scope", r.Method, r.URL)
			}
			if q.Get("client_id") != "assistant-1" || q.Get("client_secret") != "secret-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			n := fake.tokens.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": fmt.Sprintf("token-%d", n), "expires_in": 3600,
				"scope": "read", "token_type": "Bearer"})
			return
		}
		fake.send(w, r)
	}))
	t.Cleanup(server.Close)
	return &JioRCS{TokenURL: server.URL, BaseURL: server.URL,
		Assistants: map[string]string{"assistant-1": "secret-1"}, HTTP: server.Client()}
}

func jioSubmission() Submission {
	return Submission{MessageID: "3f1c2b9e-0000-4000-8000-000000000001", Msisdn: "+919876543210",
		Channel: "RCS", Country: "IN", Carrier: "JIO", AgentID: "assistant-1",
		Body: "Your order has shipped."}
}

func TestJioSendsPlainTextUnderTheAssistantWithABearerToken(t *testing.T) {
	var body map[string]any
	fake := &fakeJio{send: func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost ||
			r.URL.Path != "/v1/messaging/users/+919876543210/assistantMessages/async" {
			t.Errorf("send = %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("messageId") != "3f1c2b9e-0000-4000-8000-000000000001" ||
			r.URL.Query().Get("assistantId") != "assistant-1" {
			t.Errorf("query = %s, want our message id and the submission's assistant", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer token-1" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"messageId":"3f1c2b9e-0000-4000-8000-000000000001"}`))
	}}
	jio := jioStub(t, fake)

	submission := jioSubmission()
	submission.Promotional = true
	receipts, err := jio.Submit(context.Background(), []Submission{submission})
	if err != nil {
		t.Fatal(err)
	}
	if !receipts[0].Accepted || receipts[0].CarrierRef != submission.MessageID {
		t.Fatalf("receipt = %+v, want accepted with our message id as the reference", receipts[0])
	}
	content, _ := body["content"].(map[string]any)
	if content["plainText"] != "Your order has shipped." || body["messageTrafficType"] != "PROMOTION" {
		t.Errorf("body = %v, want content.plainText and PROMOTION", body)
	}
}

func TestJioMintsOneTokenPerAssistantAndMintsAgainAfterA401(t *testing.T) {
	var unauthorized atomic.Bool
	fake := &fakeJio{send: func(w http.ResponseWriter, r *http.Request) {
		if unauthorized.CompareAndSwap(true, false) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}}
	jio := jioStub(t, fake)
	batch := make([]Submission, 20)
	for i := range batch {
		batch[i] = jioSubmission()
		batch[i].MessageID = fmt.Sprintf("m-%d", i)
	}
	if _, err := jio.Submit(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if n := fake.tokens.Load(); n != 1 {
		t.Fatalf("minted %d tokens for 20 sends, want 1", n)
	}

	unauthorized.Store(true)
	receipts, _ := jio.Submit(context.Background(), []Submission{jioSubmission()})
	if receipts[0].ErrorCode != "carrier_unauthorized" {
		t.Errorf("a 401 send = %+v, want carrier_unauthorized", receipts[0])
	}
	if _, _ = jio.Submit(context.Background(), []Submission{jioSubmission()}); fake.tokens.Load() != 2 {
		t.Errorf("tokens after a 401 = %d, want a fresh one minted", fake.tokens.Load())
	}
}

func TestJioRefusesWithoutAnAssistantSecretABodyOrAnAgent(t *testing.T) {
	fake := &fakeJio{send: func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("reached Jio: %s", r.URL)
	}}
	jio := jioStub(t, fake)
	cases := map[string]func(*Submission){
		"agent_not_launched": func(s *Submission) { s.AgentID = "assistant-we-hold-no-secret-for" },
		"body_required":      func(s *Submission) { s.Body = "  " },
		"agent_not_resolved": func(s *Submission) { s.AgentID = "" },
	}
	for want, change := range cases {
		submission := jioSubmission()
		change(&submission)
		receipts, err := jio.Submit(context.Background(), []Submission{submission})
		if err != nil || receipts[0].Accepted || receipts[0].ErrorCode != want {
			t.Errorf("%s: receipt = %+v err = %v", want, receipts, err)
		}
	}
}

func TestJioReadsTheErrCodeOfARefusedSend(t *testing.T) {
	for status, tc := range map[int]struct{ body, want string }{
		http.StatusBadRequest:      {`{"error":{"code":"x","errCode":24,"message":"User has opted out"}}`, "recipient_opted_out"},
		http.StatusForbidden:       {``, "agent_not_launched"},
		http.StatusTooManyRequests: {`{"error":{"errCode":28}}`, "carrier_throttled"},
		http.StatusBadGateway:      {``, "carrier_unavailable"},
	} {
		fake := &fakeJio{send: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(tc.body))
		}}
		receipts, _ := jioStub(t, fake).Submit(context.Background(), []Submission{jioSubmission()})
		if receipts[0].ErrorCode != tc.want {
			t.Errorf("%d %s = %q, want %q", status, tc.body, receipts[0].ErrorCode, tc.want)
		}
	}
}

func TestJioCapabilityAndReachability(t *testing.T) {
	var batchCalls atomic.Int32
	fake := &fakeJio{send: func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/capabilities"):
			if r.URL.Query().Get("requestId") == "" {
				t.Error("capability call carries no requestId")
			}
			if strings.Contains(r.URL.Path, "+910000000000") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"features":["RICHCARD_STANDALONE_SUPPORTED","DIAL_ACTION"]}`))
		case r.URL.Path == "/v1/messaging/usersBatchGet":
			batchCalls.Add(1)
			var body struct{ PhoneNumbers []string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			// Answered out of order.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"reachableUsers": []string{body.PhoneNumbers[3], body.PhoneNumbers[1]}})
		}
	}}
	jio := jioStub(t, fake)
	ctx := context.Background()

	capability, err := jio.Capability(ctx, "assistant-1", "+919876543210")
	if err != nil || !capability.Reachable || !capability.Supports("DIAL_ACTION") {
		t.Errorf("capability = %+v, %v", capability, err)
	}
	if capability, err := jio.Capability(ctx, "assistant-1", "+910000000000"); err != nil || capability.Reachable {
		t.Errorf("a 404 = %+v, %v, want unreachable and no error", capability, err)
	}

	few, err := jio.Reachable(ctx, "assistant-1", []string{"+919876543210", "+910000000000"})
	if err != nil || strings.Join(few, ",") != "+919876543210" || batchCalls.Load() != 0 {
		t.Errorf("under 500 = %v, %v, batch calls %d; want one by one", few, err, batchCalls.Load())
	}

	many := make([]string, JioBatchMinimum)
	for i := range many {
		many[i] = fmt.Sprintf("+9190000%05d", i)
	}
	reachable, err := jio.Reachable(ctx, "assistant-1", many)
	if err != nil || batchCalls.Load() != 1 || strings.Join(reachable, ",") != many[1]+","+many[3] {
		t.Errorf("500 numbers = %v, %v, batch calls %d; want one batch, in the caller's order",
			reachable, err, batchCalls.Load())
	}
}

func TestJioWebhookEvents(t *testing.T) {
	for name, tc := range map[string]struct {
		payload   string
		kind      RCSEventKind
		delivered bool
		code      string
		text      string
	}{
		"delivered": {`{"userPhoneNumber":"+919889800000","botId":"asst","entityType":"USER_EVENT",
			"entity":{"eventType":"MESSAGE_DELIVERED","messageId":"m-1","sendTime":"2023-10-09T10:20:20.849997Z"}}`,
			RCSEventDelivery, true, "", ""},
		"read": {`{"botId":"asst","entityType":"USER_EVENT","entity":{"eventType":"MESSAGE_READ","messageId":"m-1"}}`,
			RCSEventDelivery, true, "", ""},
		"accepted is not delivered": {`{"botId":"asst","entityType":"STATUS_EVENT",
			"entity":{"eventType":"SEND_MESSAGE_SUCCESS","messageId":"m-1"}}`, RCSEventIgnored, false, "", ""},
		"failed": {`{"botId":"asst","entityType":"STATUS_EVENT","entity":{"eventType":"SEND_MESSAGE_FAILURE",
			"error":{"code":"internal","errCode":5,"message":"x"},"messageId":"m-1"}}`,
			RCSEventDelivery, false, "unreachable_handset", ""},
		"expired": {`{"botId":"asst","entityType":"SERVER_EVENT","entity":{"eventType":"TTL_EXPIRATION_REVOKED","messageId":"m-1"}}`,
			RCSEventDelivery, false, "expired_before_delivery", ""},
		"reply": {`{"userPhoneNumber":"+919321800000","botId":"asst","entityType":"USER_MESSAGE",
			"entity":{"messageId":"their-id","text":"Hi","suggestionResponse":null}}`, RCSEventInbound, false, "", "Hi"},
		"suggestion tap": {`{"botId":"asst","entityType":"USER_MESSAGE","entity":{"messageId":"their-id","text":"",
			"suggestionResponse":{"postBack":{"data":"SR1L1C1"},"plainText":"Yes","type":"REPLY"}}}`,
			RCSEventInbound, false, "", "Yes"},
		"unsubscribe": {`{"botId":"asst","entityType":"USER_EVENT","entity":{"eventType":"UNSUBSCRIBE"}}`,
			RCSEventIgnored, false, "", ""},
	} {
		event, err := ParseJioWebhook([]byte(tc.payload))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if event.Kind != tc.kind || event.Delivered != tc.delivered || event.ErrorCode != tc.code ||
			event.Text != tc.text || event.Vendor != "jio" || event.AgentID != "asst" {
			t.Errorf("%s = %+v", name, event)
		}
		if tc.kind == RCSEventDelivery && event.CarrierRef != "m-1" {
			t.Errorf("%s carrier ref = %q, want the messageId we sent", name, event.CarrierRef)
		}
	}
	if _, err := ParseJioWebhook([]byte(`{"hello":"world"}`)); err == nil {
		t.Error("a payload with no entityType was accepted")
	}
}
