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

func airtelSendStub(t *testing.T, handler http.HandlerFunc) *AirtelRCS {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &AirtelRCS{
		BaseURL: server.URL, AuthToken: "dGVzdDp0ZXN0", AgentID: "relay_agent",
		CustomerID: "Profile_1", SubAccountID: "sub-1", HTTP: server.Client(),
	}
}

func TestAirtelSendsATemplateAndReturnsTheCarriersReference(t *testing.T) {
	airtel := airtelSendStub(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/rcs/message/send") {
			t.Errorf("path = %q, want the send endpoint", r.URL.Path)
		}
		var body struct {
			CustomerID   string `json:"customerId"`
			SubAccountID string `json:"subAccountId"`
			AgentID      string `json:"agentId"`
			Msisdn       string `json:"msisdn"`
			TemplateID   string `json:"templateId"`
			TTL          int    `json:"ttl"`
			VariableData struct {
				TextVariables []string `json:"textVariables"`
			} `json:"templateVariableData"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.CustomerID == "" || body.SubAccountID == "" || body.AgentID == "" {
			t.Errorf("account identifiers missing: %+v — Airtel rejects the send without all three", body)
		}
		if body.TemplateID != "carrier-tmpl-1" {
			t.Errorf("templateId = %q, want the carrier's id", body.TemplateID)
		}
		// Values only, in order. Airtel's placeholders are positional.
		if strings.Join(body.VariableData.TextVariables, "|") != "Priya|20%" {
			t.Errorf("textVariables = %v, want values in the template's order",
				body.VariableData.TextVariables)
		}
		if body.TTL != 120 {
			t.Errorf("ttl = %d, want 120", body.TTL)
		}
		fmt.Fprint(w, `{"success":true,"code":200,"messageRequestId":"01kf0vy2s2ap1bb3an5z7vpva0","status":"INITIATED"}`)
	})

	receipts, err := airtel.Submit(context.Background(), []Submission{{
		MessageID: "relay-1", Msisdn: "+919820000002", Channel: "RCS",
		CarrierTemplateID: "carrier-tmpl-1", TTLSeconds: 120,
		TemplateVariables: []TemplateVariable{
			{Name: "first_name", Value: "Priya"},
			{Name: "discount", Value: "20%"},
		},
	}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if len(receipts) != 1 || !receipts[0].Accepted {
		t.Fatalf("receipts = %+v, want one accepted", receipts)
	}
	// INITIATED means Airtel took it, not that a handset has it. The reference
	// is what every later delivery webhook will quote.
	if receipts[0].CarrierRef != "01kf0vy2s2ap1bb3an5z7vpva0" {
		t.Errorf("CarrierRef = %q, want the messageRequestId", receipts[0].CarrierRef)
	}
}

// Neither carrier permits a free-form send outside an open conversation, so
// this is refused here rather than discovered after a round trip.
func TestASendWithNoCarrierTemplateIsRefusedWithoutCallingTheCarrier(t *testing.T) {
	var called int32
	airtel := airtelSendStub(t, func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&called, 1)
	})

	receipts, err := airtel.Submit(context.Background(), []Submission{{
		MessageID: "relay-1", Msisdn: "+919820000002", Channel: "RCS",
	}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if receipts[0].Accepted || receipts[0].ErrorCode != "template_not_registered" {
		t.Errorf("receipt = %+v, want a refusal naming the missing registration", receipts[0])
	}
	if called != 0 {
		t.Error("the carrier was called for a send it was always going to refuse")
	}
}

// Airtel's send failures are sentences, not codes. A ledger cannot group by a
// sentence and a screen cannot show one — it quotes the agent id.
func TestAirtelSendFailuresBecomeCodesAScreenCanGroupBy(t *testing.T) {
	cases := []struct {
		carrierMessage string
		want           string
	}{
		{"Template is not active/approved!!", "template_not_approved"},
		{"Template not found for provided templateId", "template_not_approved"},
		{"Agent is not launched yet. Only launched agents can send messages!!", "agent_not_launched"},
		{"RCS is not enabled for this customer!!", "rcs_not_enabled"},
		{"Validation Error - Invalid Phone Number!", "invalid_recipient"},
		{"Free Flow Messages have been limited for this agent.", "conversation_required"},
		{"Values for all the variables not provided!!", "template_variables_invalid"},
		{"Something nobody has documented", "carrier_rejected"},
	}

	for _, testCase := range cases {
		airtel := airtelSendStub(t, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(w, `{"success":false,"code":400,"message":%q}`, testCase.carrierMessage)
		})
		receipts, err := airtel.Submit(context.Background(), []Submission{{
			MessageID: "relay-1", Msisdn: "+919820000002",
			CarrierTemplateID: "carrier-tmpl-1",
		}})
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if receipts[0].Accepted {
			t.Errorf("%q was accepted", testCase.carrierMessage)
		}
		if receipts[0].ErrorCode != testCase.want {
			t.Errorf("%q → %q, want %q", testCase.carrierMessage,
				receipts[0].ErrorCode, testCase.want)
		}
		// The carrier's own words must not survive into the code: they quote
		// the agent id, and codes end up on tenant-visible screens.
		if strings.Contains(receipts[0].ErrorCode, " ") {
			t.Errorf("error code %q is a sentence, not a code", receipts[0].ErrorCode)
		}
	}
}

func TestAirtelThrottlingIsItsOwnOutcome(t *testing.T) {
	airtel := airtelSendStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
	})

	receipts, _ := airtel.Submit(context.Background(), []Submission{{
		MessageID: "relay-1", Msisdn: "+919820000002", CarrierTemplateID: "t",
	}})
	// A throttled send should be retried after a pause; a rejected one should
	// not. Collapsing them would have a campaign either give up or hammer.
	if receipts[0].ErrorCode != "carrier_throttled" {
		t.Errorf("ErrorCode = %q, want carrier_throttled", receipts[0].ErrorCode)
	}
}

func TestABatchIsSentOneMessageAtATimeAndKeepsItsOrder(t *testing.T) {
	var calls int32
	airtel := airtelSendStub(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var body struct {
			Msisdn string `json:"msisdn"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprintf(w, `{"success":true,"code":200,"messageRequestId":"ref-%s","status":"INITIATED"}`,
			body.Msisdn[len(body.Msisdn)-1:])
	})

	submissions := make([]Submission, 0, 5)
	for i := 0; i < 5; i++ {
		submissions = append(submissions, Submission{
			MessageID:         fmt.Sprintf("relay-%d", i),
			Msisdn:            fmt.Sprintf("+91982000000%d", i),
			CarrierTemplateID: "carrier-tmpl-1",
		})
	}

	receipts, err := airtel.Submit(context.Background(), submissions)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if calls != 5 {
		t.Errorf("made %d calls, want 5 — Airtel documents no batch send endpoint", calls)
	}
	// Receipts are matched to messages by position as well as by id, so an
	// out-of-order batch would settle the wrong wallet.
	for i, receipt := range receipts {
		if receipt.MessageID != fmt.Sprintf("relay-%d", i) {
			t.Fatalf("receipt %d is for %q", i, receipt.MessageID)
		}
		if receipt.CarrierRef != fmt.Sprintf("ref-%d", i) {
			t.Errorf("receipt %d carries %q", i, receipt.CarrierRef)
		}
	}
}

// --- Vi ---

func TestViSendsATemplateWithNamedParametersAsAJSONString(t *testing.T) {
	var tokens int32
	vi := viStub(t, &tokens, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "/agentMessages/async") {
			t.Errorf("%s %s, want POST to the Google-style send endpoint", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("messageId") != "relay-77" {
			t.Errorf("messageId = %q — Relay's own id must travel as Vi's, or the "+
				"delivery webhook cannot be correlated",
				r.URL.Query().Get("messageId"))
		}
		var body struct {
			ContentMessage struct {
				TemplateMessage struct {
					TemplateCode string `json:"templateCode"`
					CustomParams string `json:"customParams"`
				} `json:"templateMessage"`
			} `json:"contentMessage"`
			TTL string `json:"ttl"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		template := body.ContentMessage.TemplateMessage
		if template.TemplateCode != "vi-template-9" {
			t.Errorf("templateCode = %q", template.TemplateCode)
		}
		// customParams is a JSON STRING, not an object. Vi rejects a nested
		// object outright.
		var named map[string]string
		if err := json.Unmarshal([]byte(template.CustomParams), &named); err != nil {
			t.Fatalf("customParams is not a JSON string: %q", template.CustomParams)
		}
		if named["first_name"] != "Priya" || named["discount"] != "20%" {
			t.Errorf("customParams = %v, want the names Vi's placeholders use", named)
		}
		// Vi wants a duration ending in 's'; a bare number is rejected.
		if body.TTL != "120s" {
			t.Errorf("ttl = %q, want \"120s\"", body.TTL)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	})

	receipts, err := vi.Submit(context.Background(), []Submission{{
		MessageID: "relay-77", Msisdn: "+914253136789", Channel: "RCS",
		CarrierTemplateID: "vi-template-9", TTLSeconds: 120,
		TemplateVariables: []TemplateVariable{
			{Name: "first_name", Value: "Priya"},
			{Name: "discount", Value: "20%"},
		},
	}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !receipts[0].Accepted {
		t.Fatalf("receipt = %+v, want accepted", receipts[0])
	}
	// Vi returns no reference of its own, so the id we supplied IS the key the
	// delivery webhook will quote back.
	if receipts[0].CarrierRef != "relay-77" {
		t.Errorf("CarrierRef = %q, want the id we supplied", receipts[0].CarrierRef)
	}
}

func TestViHasNoTemplateAPIAndSaysSo(t *testing.T) {
	vi := &ViRCS{
		BaseURL: "https://example.test", TokenURL: "https://example.test/t",
		ClientID: "c", ClientSecret: "s", BotID: "b",
	}
	_, err := vi.RegisterTemplate(context.Background(), RCSTemplateSpec{Name: "x"})
	if err != ErrTemplateRegistrationManual {
		t.Errorf("RegisterTemplate err = %v, want ErrTemplateRegistrationManual", err)
	}
	if _, err := vi.TemplateStatus(context.Background(), "code"); err != ErrTemplateRegistrationManual {
		t.Errorf("TemplateStatus err = %v, want ErrTemplateRegistrationManual", err)
	}
}

// --- Airtel template registration ---

func TestAirtelRegistersATemplateAndReturnsItPending(t *testing.T) {
	airtel := airtelSendStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/v1/rcs/template") {
			t.Errorf("%s %s, want POST to the template endpoint", r.Method, r.URL.Path)
		}
		var body struct {
			TemplateName     string `json:"templateName"`
			TemplateCategory string `json:"templateCategory"`
			TemplateUseCase  string `json:"templateUseCase"`
			CreatedBy        string `json:"createdBy"`
			UpdatedBy        string `json:"updatedBy"`
			TTL              int    `json:"ttl"`
			TemplateData     struct {
				Text string `json:"text"`
			} `json:"templateData"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.TemplateCategory != "TEXT" {
			t.Errorf("templateCategory = %q — only the text shape is modelled", body.TemplateCategory)
		}
		if body.TemplateUseCase != "TRANSACTIONAL" {
			t.Errorf("templateUseCase = %q, want the agent's own use case", body.TemplateUseCase)
		}
		// Airtel requires a valid address and records it in the event log,
		// which is the only audit trail of who submitted what.
		if body.CreatedBy != "ops@acme.test" || body.UpdatedBy != "ops@acme.test" {
			t.Errorf("createdBy/updatedBy = %q/%q", body.CreatedBy, body.UpdatedBy)
		}
		if body.TemplateData.Text != "Hi {{1}}, your code is {{2}}." {
			t.Errorf("text = %q", body.TemplateData.Text)
		}
		fmt.Fprint(w, `{"success":true,"code":200,"message":"success","rcsTemplate":{
			"templateId":"01kct02npb5demxdk62wxjqmqb","templateStatus":"PENDING"}}`)
	})

	registration, err := airtel.RegisterTemplate(context.Background(), RCSTemplateSpec{
		Name: "Login OTP", UseCase: "TRANSACTIONAL",
		Text: "Hi {{1}}, your code is {{2}}.", SubmittedBy: "ops@acme.test",
	})
	if err != nil {
		t.Fatalf("RegisterTemplate: %v", err)
	}
	if registration.CarrierTemplateID != "01kct02npb5demxdk62wxjqmqb" {
		t.Errorf("CarrierTemplateID = %q", registration.CarrierTemplateID)
	}
	if registration.Status != RCSTemplatePending {
		t.Errorf("Status = %q, want pending — Airtel reviews for up to 24 hours", registration.Status)
	}
}

// An id-less success is a failure whatever the carrier called it: there is
// nothing to send against and nothing for the approval webhook to match on.
func TestATemplateAcceptedWithNoIdIsAFailure(t *testing.T) {
	airtel := airtelSendStub(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"code":200,"message":"success"}`)
	})
	if _, err := airtel.RegisterTemplate(context.Background(),
		RCSTemplateSpec{Name: "x", UseCase: "OTP", Text: "hi", SubmittedBy: "a@b.test"}); err == nil {
		t.Fatal("a response with no templateId was treated as a success")
	}
}

func TestAirtelReadsBackATemplatesApprovalState(t *testing.T) {
	airtel := airtelSendStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("templateId") != "tmpl-1" {
			t.Errorf("templateId query = %q", r.URL.Query().Get("templateId"))
		}
		if r.URL.Query().Get("customerId") == "" || r.URL.Query().Get("subAccountId") == "" {
			t.Error("account identifiers missing from the fetch query")
		}
		fmt.Fprint(w, `{"success":true,"code":200,"rcsTemplate":{
			"templateId":"tmpl-1","templateStatus":"APPROVED"}}`)
	})

	registration, err := airtel.TemplateStatus(context.Background(), "tmpl-1")
	if err != nil {
		t.Fatalf("TemplateStatus: %v", err)
	}
	if registration.Status != RCSTemplateApproved {
		t.Errorf("Status = %q, want approved", registration.Status)
	}
}

// Guessing upward would let an unrecognised carrier state unblock sending, and
// a send against a template the carrier has not approved is refused at the
// gateway AFTER the money has moved.
func TestAnUnrecognisedCarrierStateIsPendingNotApproved(t *testing.T) {
	for _, state := range []string{"UNDER_REVIEW", "SOMETHING_NEW", ""} {
		if got := normaliseCarrierTemplateStatus(state); got != RCSTemplatePending {
			t.Errorf("normaliseCarrierTemplateStatus(%q) = %q, want pending", state, got)
		}
	}
	if normaliseCarrierTemplateStatus("APPROVED") != RCSTemplateApproved {
		t.Error("APPROVED did not map to approved")
	}
	if normaliseCarrierTemplateStatus("REJECTED") != RCSTemplateRejected {
		t.Error("REJECTED did not map to rejected")
	}
}
