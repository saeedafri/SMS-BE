package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeTrustsignal answers like rcsapi.trustsignal.io and records what it saw.
type fakeTrustsignal struct {
	mu       sync.Mutex
	paths    []string
	queries  []map[string]string
	bodies   []map[string]any
	status   int
	response string
}

func (f *fakeTrustsignal) serve(t *testing.T) (*TrustsignalRCS, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		query := map[string]string{}
		for key := range r.URL.Query() {
			query[key] = r.URL.Query().Get(key)
		}
		f.mu.Lock()
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		f.queries = append(f.queries, query)
		f.bodies = append(f.bodies, body)
		status, response := f.status, f.response
		f.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	return &TrustsignalRCS{BaseURL: server.URL, APIKey: "secret-key-123"}, server
}

func TestTrustsignalSendsTheTemplateWithNamedVariables(t *testing.T) {
	fake := &fakeTrustsignal{response: `{"success":true,"message":"RCS message request accepted",
		"results":{"to":"+919876543210","transaction_id":"TS987654321","cost":"0.1"}}`}
	ts, _ := fake.serve(t)

	receipts, err := ts.Submit(context.Background(), []Submission{{
		MessageID: "m1", Msisdn: "919876543210", CarrierTemplateID: "tpl-1", AgentID: "bot-1",
		TemplateVariables: []TemplateVariable{{"first_name", "Priya"}, {"order_id", "A-1"}},
		TTLSeconds:        120,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !receipts[0].Accepted || receipts[0].CarrierRef != "TS987654321" {
		t.Fatalf("receipt = %+v, want accepted with the transaction id", receipts[0])
	}
	if fake.paths[0] != "POST /api/v1/rcs/with_fallback" || fake.queries[0]["api_key"] != "secret-key-123" {
		t.Errorf("request = %s %v", fake.paths[0], fake.queries[0])
	}
	body := fake.bodies[0]
	if body["to"] != "+919876543210" || body["template_id"] != "tpl-1" || body["ttl"] != "120s" {
		t.Errorf("body = %v", body)
	}
	variables, _ := body["rcs_variables"].(map[string]any)
	if variables["first_name"] != "Priya" || variables["order_id"] != "A-1" {
		t.Errorf("rcs_variables = %v", body["rcs_variables"])
	}
	if _, fallback := body["sms_fallback"]; fallback {
		t.Error("sms_fallback sent; Relay runs its own fallback leg")
	}
}

func TestTrustsignalRefusalsBecomeErrorCodes(t *testing.T) {
	for name, tc := range map[string]struct {
		status   int
		response string
		want     string
	}{
		"template": {200, `{"success":false,"errors":[{"code":"201","codeMsg":"TEMPLATE_NOT_FOUND","message":"x"}]}`, "template_not_approved"},
		"key":      {200, `{"success":false,"errors":[{"code":"101","codeMsg":"API_KEY_MISSING"}]}`, "carrier_unauthorized"},
		"credits":  {200, `{"success":false,"message":"Insufficient credits"}`, "carrier_account_unfunded"},
		"401":      {401, `{}`, "carrier_unauthorized"},
		"429":      {429, `{}`, "carrier_throttled"},
		"500":      {502, `oops`, "carrier_unavailable"},
		"no ref":   {200, `{"success":true,"results":{}}`, "carrier_rejected"},
	} {
		fake := &fakeTrustsignal{status: tc.status, response: tc.response}
		ts, _ := fake.serve(t)
		receipts, _ := ts.Submit(context.Background(), []Submission{{MessageID: "m",
			Msisdn: "+919876543210", CarrierTemplateID: "tpl"}})
		if receipts[0].Accepted || receipts[0].ErrorCode != tc.want {
			t.Errorf("%s: receipt = %+v, want refused %s", name, receipts[0], tc.want)
		}
	}

	// No carrier template: refused without a round trip.
	fake := &fakeTrustsignal{}
	ts, _ := fake.serve(t)
	receipts, _ := ts.Submit(context.Background(), []Submission{{MessageID: "m", Msisdn: "+91"}})
	if receipts[0].ErrorCode != "template_not_registered" || len(fake.paths) != 0 {
		t.Errorf("receipt %+v after %d calls", receipts[0], len(fake.paths))
	}
}

func TestTrustsignalErrorsNeverCarryTheAPIKey(t *testing.T) {
	fake := &fakeTrustsignal{}
	ts, server := fake.serve(t)
	server.Close()
	_, err := ts.RegisterTemplate(context.Background(), "bot",
		RCSTemplateSpec{Name: "n", Text: "hi"})
	if err == nil || strings.Contains(err.Error(), "secret-key-123") {
		t.Fatalf("error = %v, want one that does not quote the key", err)
	}
	if _, err := ts.TemplateStatus(context.Background(), "", "tpl"); err == nil ||
		strings.Contains(err.Error(), "secret-key-123") {
		t.Fatalf("status error = %v", err)
	}
}

func TestTrustsignalRegistersATextTemplateWithButtons(t *testing.T) {
	fake := &fakeTrustsignal{response: `{"success":true,"message":"Template Added successfully!",
		"template":{"id":"tpl-9","status":"pending"}}`}
	ts, _ := fake.serve(t)

	got, err := ts.RegisterTemplate(context.Background(), "bot-7", RCSTemplateSpec{
		Name: "order shipped", Text: "Hi {{first_name}}, order {{ order_id }} shipped.",
		Suggestions: []RCSSuggestion{{Type: "reply", Text: "Thanks"},
			{Type: "open_url", Text: "Track", URL: "https://t.example"},
			{Type: "dial", Text: "Call", PhoneNumber: "+911234567890"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.CarrierTemplateID != "tpl-9" || got.Status != RCSTemplatePending {
		t.Errorf("registration = %+v", got)
	}
	if fake.paths[0] != "POST /api/v1/template" || fake.queries[0]["s"] != "1" {
		t.Errorf("request = %s %v", fake.paths[0], fake.queries[0])
	}
	body := fake.bodies[0]
	if body["type"] != "text_message" || body["botId"] != "bot-7" ||
		body["textMessageContent"] != "Hi [first_name], order [order_id] shipped." {
		t.Errorf("body = %v", body)
	}
	suggestions, _ := body["suggestions"].([]any)
	want := []string{"reply", "url_action", "dialer_action"}
	if len(suggestions) != 3 {
		t.Fatalf("suggestions = %v", body["suggestions"])
	}
	for i, s := range suggestions {
		if s.(map[string]any)["suggestionType"] != want[i] {
			t.Errorf("suggestion %d = %v, want %s", i, s, want[i])
		}
	}
	if suggestions[1].(map[string]any)["url"] != "https://t.example" ||
		suggestions[2].(map[string]any)["phoneNumber"] != "+911234567890" {
		t.Errorf("suggestion targets lost: %v", suggestions)
	}
}

func TestTrustsignalRegistersARichCard(t *testing.T) {
	fake := &fakeTrustsignal{response: `{"success":true,"template":{"id":"card-1","status":"active"}}`}
	ts, _ := fake.serve(t)

	got, err := ts.RegisterTemplate(context.Background(), "bot-7", RCSTemplateSpec{
		Name: "sale", Card: &RCSCard{Title: "Hi {{first_name}}", Description: "{{pct}} off today",
			MediaURL: "https://cdn.example/sale.png"},
		Suggestions: []RCSSuggestion{{Type: "open_url", Text: "Shop", URL: "https://shop.example"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != RCSTemplateApproved {
		t.Errorf("status = %q, want approved for active", got.Status)
	}
	if fake.queries[0]["s"] != "2" || fake.bodies[0]["type"] != "rich_card" {
		t.Errorf("request = %v %v", fake.queries[0], fake.bodies[0])
	}
	card, _ := fake.bodies[0]["standAlone"].(map[string]any)
	if card["cardTitle"] != "Hi [first_name]" || card["cardDescription"] != "[pct] off today" ||
		card["mediaUrl"] != "https://cdn.example/sale.png" {
		t.Errorf("standAlone = %v", card)
	}
	if buttons, _ := card["suggestions"].([]any); len(buttons) != 1 {
		t.Errorf("card suggestions = %v", card["suggestions"])
	}
}

func TestTrustsignalTemplateRefusalIsTheCustomersToFix(t *testing.T) {
	fake := &fakeTrustsignal{response: `{"success":false,"errors":[{"code":"301",
		"codeMsg":"INVALID_BOT","message":"Bot is not active"}]}`}
	ts, _ := fake.serve(t)
	_, err := ts.RegisterTemplate(context.Background(), "bot", RCSTemplateSpec{Name: "n", Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), "Bot is not active") ||
		strings.HasPrefix(err.Error(), "trustsignal rcs:") {
		t.Fatalf("error = %v, want their words without the transport prefix", err)
	}

	fake = &fakeTrustsignal{status: 503, response: `{}`}
	ts, _ = fake.serve(t)
	_, err = ts.RegisterTemplate(context.Background(), "bot", RCSTemplateSpec{Name: "n", Text: "hi"})
	if err == nil || !strings.HasPrefix(err.Error(), "trustsignal rcs:") {
		t.Fatalf("5xx error = %v, want a transport error", err)
	}
}

func TestTrustsignalTemplateStatusReadsTheirWords(t *testing.T) {
	for status, want := range map[string]string{"active": RCSTemplateApproved,
		"rejected": RCSTemplateRejected, "failed": RCSTemplateRejected, "submitted": RCSTemplatePending} {
		fake := &fakeTrustsignal{response: `{"success":true,"templates":[{"id":"tpl-3","status":"` +
			status + `","error":"logo too large"}]}`}
		ts, _ := fake.serve(t)
		got, err := ts.TemplateStatus(context.Background(), "", "tpl-3")
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != want || fake.paths[0] != "GET /api/v1/template/tpl-3" {
			t.Errorf("%s: %+v via %s", status, got, fake.paths[0])
		}
		if (want == RCSTemplateRejected) != (got.RejectionReason == "logo too large") {
			t.Errorf("%s: reason = %q", status, got.RejectionReason)
		}
	}
}

func TestTrustsignalHasNoCapabilityLookup(t *testing.T) {
	ts := &TrustsignalRCS{BaseURL: "https://x", APIKey: "k"}
	if _, err := ts.Reachable(context.Background(), "bot", []string{"+91"}); err != ErrRCSNoCapabilityLookup {
		t.Errorf("Reachable err = %v", err)
	}
	if !ts.Health(context.Background()).Healthy || (&TrustsignalRCS{}).Health(context.Background()).Healthy {
		t.Error("health should follow configuration")
	}
}

func TestTrustsignalWebhooks(t *testing.T) {
	cases := map[string]struct {
		payload string
		check   func(RCSEvent) bool
	}{
		"delivered": {`{"webhook_type":"rcs_message","transaction_id":"TS1","status":"delivered",
			"to":"+919999999999","bot_id":"b","dlrt":"2026-07-23T10:15:03Z"}`,
			func(e RCSEvent) bool {
				return e.Kind == RCSEventDelivery && e.Delivered && !e.Read && e.CarrierRef == "TS1" &&
					e.OccurredAt.Format("15:04:05") == "10:15:03"
			}},
		"read": {`{"webhook_type":"rcs_message","transaction_id":"TS1","status":"read"}`,
			func(e RCSEvent) bool { return e.Kind == RCSEventDelivery && e.Delivered && e.Read }},
		"nonrcs": {`{"webhook_type":"rcs_message","transaction_id":"TS1","status":"nonrcs"}`,
			func(e RCSEvent) bool {
				return e.Kind == RCSEventDelivery && !e.Delivered && e.ErrorCode == "unreachable_handset"
			}},
		"failed": {`{"webhook_type":"rcs_message","transaction_id":"TS1","status":"failed","error_code":"E1"}`,
			func(e RCSEvent) bool { return e.Kind == RCSEventDelivery && e.ErrorCode == "carrier_failed" }},
		"click": {`{"webhook_type":"rcs_message","transaction_id":"TS1","status":"click"}`,
			func(e RCSEvent) bool { return e.Kind == RCSEventIgnored }},
		"fallback": {`{"webhook_type":"rcs_fallback_status","transaction_id":"TS1","status":"delivered"}`,
			func(e RCSEvent) bool { return e.Kind == RCSEventIgnored }},
		"bot": {`{"webhook_type":"rcs_bot","bot_id":"b","status":"active"}`,
			func(e RCSEvent) bool { return e.Kind == RCSEventIgnored }},
		"template active": {`{"webhook_type":"rcs_template","template_id":"tpl","status":"active"}`,
			func(e RCSEvent) bool {
				return e.Kind == RCSEventTemplate && e.CarrierTemplateID == "tpl" &&
					e.TemplateStatus == RCSTemplateApproved
			}},
		"template rejected": {`{"webhook_type":"rcs_template","template_id":"tpl","status":"rejected","error":"bad logo"}`,
			func(e RCSEvent) bool {
				return e.TemplateStatus == RCSTemplateRejected && e.RejectionReason == "bad logo"
			}},
		"reply": {`{"webhook_type":"rcs_user_response","from":"+919999999999","bot_id":"bot_1",
			"response":"Yes please","mtype":"suggestion","tlmsgid":"msg_9","sendTime":"2026-07-23T11:15:30Z"}`,
			func(e RCSEvent) bool {
				return e.Kind == RCSEventInbound && e.Msisdn == "+919999999999" && e.AgentID == "bot_1" &&
					e.Text == "Yes please" && e.PostbackData == "Yes please" && e.ContextRef == "msg_9"
			}},
	}
	for name, tc := range cases {
		event, err := ParseTrustsignalWebhook([]byte(tc.payload))
		if err != nil || event.Vendor != "trustsignal" || !tc.check(event) {
			t.Errorf("%s: event = %+v, err = %v", name, event, err)
		}
	}
	for _, bad := range []string{`not json`, `{"status":"delivered"}`} {
		if _, err := ParseTrustsignalWebhook([]byte(bad)); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestAirtelRefusesACardRatherThanStoringItAsText(t *testing.T) {
	err := ValidateAirtelTemplate(RCSTemplateSpec{Name: "n", UseCase: "PROMOTIONAL", Text: "hi",
		Card: &RCSCard{Title: "t"}})
	if err == nil || !strings.Contains(err.Error(), "portal") {
		t.Errorf("err = %v, want a refusal pointing at the portal", err)
	}
}

// The shape Trustsignal's RCS send REALLY answers with, captured from a live
// send on 8 Oct 2026: `result` (singular) with `phone`, not the `results` and
// `to` their Postman example shows. The fixtures above followed the example, so
// every send the carrier accepted was recorded as refused: the customer's
// handset got the message and the wallet was never charged for it.
func TestTrustsignalAcceptsTheRealSingularResultReply(t *testing.T) {
	fake := &fakeTrustsignal{response: `{"message":"Request process successfully","result":{"phone":"+917408485420",` +
		`"transaction_id":"179139989480908666791740848542048451","cost":0.2,"sms_cost":0},"success":true}`}
	ts, _ := fake.serve(t)
	receipts, err := ts.Submit(context.Background(), []Submission{{MessageID: "m1",
		Msisdn: "+917408485420", CarrierTemplateID: "gqakwexzanh"}})
	if err != nil {
		t.Fatal(err)
	}
	if !receipts[0].Accepted || receipts[0].CarrierRef != "179139989480908666791740848542048451" {
		t.Errorf("receipt = %+v, want accepted with the transaction id as the carrier ref", receipts[0])
	}
}
