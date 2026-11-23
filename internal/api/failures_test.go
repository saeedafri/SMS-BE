package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"
)

type loggedFailure struct {
	Layer        *string `json:"layer"`
	Code         string  `json:"code"`
	Title        string  `json:"title"`
	Charged      bool    `json:"charged"`
	Retryable    bool    `json:"retryable"`
	ProviderCode *string `json:"providerCode"`
	Source       string  `json:"source"`
}

type loggedMessage struct {
	ID       string         `json:"id"`
	Status   string         `json:"status"`
	Failure  *loggedFailure `json:"failure"`
	Timeline []struct {
		Stage  string `json:"stage"`
		Step   string `json:"step"`
		Failed *bool  `json:"failed"`
	} `json:"timeline"`
}

func (h *harness) messagesWith(token, query string) []loggedMessage {
	h.t.Helper()
	res := h.do(http.MethodGet, "/v1/messages?"+query, token, nil)
	if res.Code != http.StatusOK {
		h.t.Fatalf("messages?%s = %d\n%s", query, res.Code, res.Body)
	}
	var page struct {
		Messages []loggedMessage `json:"messages"`
	}
	res.decode(h.t, &page)
	return page.Messages
}

// settleSandbox applies the sandbox's delivery reports, as the worker does.
func (h *harness) settleSandbox() {
	h.t.Helper()
	conn, err := h.server.ClickHouse.Conn(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	drainer := &sending.Service{DB: h.server.DB, ClickHouse: conn,
		Connector: h.server.Connector, Settled: h.server.MessageSettled}
	if _, err := drainer.DrainSandboxReports(context.Background()); err != nil {
		h.t.Fatalf("drain: %v", err)
	}
}

// insertFailedMessage writes a failed message straight into the warehouse, for
// a raw code no connector here would produce.
func (h *harness) insertFailedMessage(tenant account, status, code string, otp bool) uuid.UUID {
	h.t.Helper()
	conn, err := h.server.ClickHouse.Conn(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	id, now := uuid.New(), time.Now().UTC()
	if err := store.InsertMessages(context.Background(), conn, []store.MessageRecord{{
		TenantID: tenant.TenantID, ID: id, Channel: "SMS", Country: "IN", SenderHeader: "TESTER",
		Msisdn: "+919876543210", Status: status, ErrorCode: &code, FraudFlag: "none",
		Segments: 1, Currency: "INR", CreatedAt: now, UpdatedAt: now, Version: 1, OTP: otp,
	}}); err != nil {
		h.t.Fatal(err)
	}
	return id
}

// Batch 1 rows 1 and 9: every message that did not arrive says why, in a
// sentence, and says it was not charged.
func TestEveryFailedOrRefusedMessageSaysWhyAndWasNotCharged(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)
	for _, to := range []string{"9876543000", "9876543001", "9876543002"} {
		h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
			"senderId": sender, "templateId": template, "to": to, "body": "Hello"})
	}
	pending := h.approvedSenderOn(tenant, "SMS")
	if _, err := h.admin.Exec(context.Background(),
		`UPDATE sender_ids SET status = 'pending' WHERE id = $1`, pending); err != nil {
		t.Fatal(err)
	}
	h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": pending, "to": "9876543210", "body": "Hello"})
	h.settleSandbox()

	failed := h.messagesWith(tenant.Token, "status=failed&limit=50")
	rejected := h.messagesWith(tenant.Token, "status=rejected&limit=50")
	if len(failed) < 3 || len(rejected) < 1 {
		t.Fatalf("failed %d, rejected %d: the fixture did not produce both", len(failed), len(rejected))
	}
	for _, message := range append(failed, rejected...) {
		if message.Failure == nil || message.Failure.Title == "" || message.Failure.Charged {
			t.Errorf("%s %s: failure = %+v, want a title and charged false",
				message.Status, message.ID, message.Failure)
		}
	}
	for _, message := range rejected {
		if message.Failure.Source != "gate" || message.Failure.ProviderCode != nil {
			t.Errorf("refusal %s = %+v, want source gate and no provider code", message.ID, message.Failure)
		}
	}
	for _, message := range failed {
		if len(message.Timeline) == 0 || message.Timeline[len(message.Timeline)-1].Failed == nil {
			t.Errorf("failed %s timeline = %+v, want it to end on the failing step", message.ID, message.Timeline)
		}
	}
}

// Batch 1 row 2: every refusal code the contract declares has a rule.
func TestEveryRefusalCodeHasADictionaryRow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	res := h.do(http.MethodGet, "/v1/operator/failure-reasons?source=gate&limit=200", h.operatorToken(), nil)
	var page struct {
		Items []struct {
			Code  string `json:"code"`
			Title string `json:"title"`
			Match struct {
				Value string `json:"value"`
			} `json:"match"`
		} `json:"items"`
	}
	res.decode(t, &page)
	have := map[string]bool{}
	for _, item := range page.Items {
		if item.Title != "" {
			have[item.Match.Value] = true
		}
	}
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range spec.Components.Schemas["MessageRefusalCode"].Value.Enum {
		if !have[code.(string)] {
			t.Errorf("refusal code %s has no dictionary row", code)
		}
	}
}

// Batch 1 rows 5 and 6: an unmapped code sits in the queue until a rule
// matches it, then leaves; an edit shows on the next read of every message it
// matches, one version higher.
func TestARuleTakesACodeOutOfTheQueueAndAnEditShowsOnTheNextRead(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	operator := h.operatorToken()
	tenant := h.newAccount("owner")
	raw := "0xT" + strings.ToUpper(uuid.NewString()[:7])
	message := h.insertFailedMessage(tenant, "carrier_rejected", raw, false)

	inQueue := func() bool {
		res := h.do(http.MethodGet, "/v1/operator/failure-reasons/unmapped?source=smpp_submit&limit=200",
			operator, nil)
		return strings.Contains(string(res.Body), `"providerCode":"`+raw+`"`)
	}
	if !inQueue() {
		t.Fatal("an unmapped code is not in the queue")
	}
	if got := h.logged(tenant, message); got.Failure.Layer != nil || got.Failure.Code != "unmapped" ||
		got.Failure.ProviderCode == nil || *got.Failure.ProviderCode != raw {
		t.Fatalf("before a rule = %+v, want unmapped with the raw code", got.Failure)
	}

	created := h.do(http.MethodPost, "/v1/operator/failure-reasons", operator, map[string]any{
		"channel": "SMS", "source": "smpp_submit", "match": map[string]any{"kind": "exact", "value": raw},
		"code": "test_code", "layer": "carrier", "title": "First title", "retryable": false,
		"customerVisible": true})
	if created.Code != http.StatusCreated {
		t.Fatalf("create rule = %d\n%s", created.Code, created.Body)
	}
	var rule struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	created.decode(t, &rule)
	t.Cleanup(func() {
		_, _ = h.admin.Exec(context.Background(), `DELETE FROM failure_reasons WHERE id = $1`, rule.ID)
	})
	if inQueue() {
		t.Error("the code stayed in the queue after a rule matched it")
	}
	if again := h.do(http.MethodPost, "/v1/operator/failure-reasons", operator, map[string]any{
		"channel": "SMS", "source": "smpp_submit", "match": map[string]any{"kind": "exact", "value": raw},
		"code": "other", "layer": "ours", "title": "Again", "retryable": false,
		"customerVisible": true}); again.Code != http.StatusConflict {
		t.Errorf("duplicate rule = %d, want 409", again.Code)
	}

	edited := h.do(http.MethodPatch, "/v1/operator/failure-reasons/"+rule.ID, operator,
		map[string]any{"title": "Second title", "layer": "handset"})
	var after struct {
		Version int `json:"version"`
	}
	edited.decode(t, &after)
	if edited.Code != http.StatusOK || after.Version != rule.Version+1 {
		t.Fatalf("edit = %d version %d, want 200 and version %d", edited.Code, after.Version, rule.Version+1)
	}
	if got := h.logged(tenant, message); got.Failure.Title != "Second title" ||
		got.Failure.Layer == nil || *got.Failure.Layer != "handset" {
		t.Errorf("after the edit the message reads %+v", got.Failure)
	}
	var audited int
	_ = h.admin.QueryRow(context.Background(), `SELECT count(*) FROM operator_audit_log
		WHERE action IN ('failure_reason.create', 'failure_reason.update') AND target_label = 'test_code'`).
		Scan(&audited)
	if audited < 2 {
		t.Errorf("audit rows = %d, want the create and the edit", audited)
	}
}

func (h *harness) logged(tenant account, id uuid.UUID) loggedMessage {
	h.t.Helper()
	var message loggedMessage
	res := h.do(http.MethodGet, "/v1/messages/"+id.String(), tenant.Token, nil)
	res.decode(h.t, &message)
	if message.Failure == nil {
		h.t.Fatalf("message %s has no failure\n%s", id, res.Body)
	}
	return message
}

// The order the brief fixes: 404 before 422, an empty body is 422, and a body
// with one bad field changes nothing.
func TestEditingARuleJudgesTheRecordFirstAndWritesAllOrNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	operator := h.operatorToken()
	if res := h.do(http.MethodPatch, "/v1/operator/failure-reasons/fr-9999", operator,
		map[string]any{"layer": "nonsense"}); res.Code != http.StatusNotFound {
		t.Errorf("unknown rule with a bad body = %d, want 404", res.Code)
	}
	if res := h.do(http.MethodPatch, "/v1/operator/failure-reasons/fr-0001", operator,
		map[string]any{}); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty body = %d, want 422", res.Code)
	}
	before, err := store.GetFailureRule(context.Background(), h.pool, "fr-0001")
	if err != nil {
		t.Fatal(err)
	}
	res := h.do(http.MethodPatch, "/v1/operator/failure-reasons/fr-0001", operator,
		map[string]any{"title": "Changed", "retryable": "yes"})
	after, _ := store.GetFailureRule(context.Background(), h.pool, "fr-0001")
	if res.Code != http.StatusUnprocessableEntity || after.Title != before.Title || after.Version != before.Version {
		t.Errorf("half-bad edit = %d, title %q v%d, want 422 and nothing written", res.Code, after.Title, after.Version)
	}
	if res := h.do(http.MethodPatch, "/v1/operator/failure-reasons/fr-0001", operator,
		map[string]any{"bogus": 1}); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("undeclared field = %d, want 422", res.Code)
	}
}

// Raw delivery-receipt text is never shown for a one-time code.
func TestAOneTimeCodesProviderCodeIsWithheld(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	otp := h.insertFailedMessage(tenant, "undelivered", "UNDELIV:123456", true)
	plain := h.insertFailedMessage(tenant, "undelivered", "UNDELIV:001", false)
	if got := h.logged(tenant, otp); got.Failure.ProviderCode != nil {
		t.Errorf("OTP providerCode = %q, want withheld", *got.Failure.ProviderCode)
	}
	if got := h.logged(tenant, plain); got.Failure.ProviderCode == nil {
		t.Error("an ordinary failure lost its provider code")
	}
}

// The layer filter narrows the log to failures whose cause lies in that layer.
func TestTheLayerFilterNarrowsTheLog(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)
	for _, to := range []string{"9876543000", "9876543001", "9876543210"} {
		h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
			"senderId": sender, "templateId": template, "to": to, "body": "Hello"})
	}
	h.settleSandbox()
	handset := h.messagesWith(tenant.Token, "layer=handset")
	if len(handset) != 1 || handset[0].Failure == nil || *handset[0].Failure.Layer != "handset" {
		t.Errorf("layer=handset = %+v, want the one absent-subscriber message", handset)
	}
	if res := h.do(http.MethodGet, "/v1/messages?layer=nobody", tenant.Token, nil); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown layer = %d, want 422", res.Code)
	}
}

// Batch 1 row 10: the message.failed webhook carries the same failure the log
// returns for that message.
func TestTheFailedWebhookCarriesTheLogsFailure(t *testing.T) {
	h := newSendHarness(t)
	endpoint := startCustomerEndpoint(t)
	tenant := h.newAccount("owner")
	h.subscribeEndpoint(tenant, endpoint, "message.failed")
	h.deliverOneSMS(tenant, "9876543001")
	waitFor(t, "the message.failed event", func() bool { return len(endpoint.received("message.failed")) > 0 })

	var body struct {
		Type string `json:"type"`
		Data struct {
			MessageID string          `json:"messageId"`
			Failure   json.RawMessage `json:"failure"`
		} `json:"data"`
	}
	if err := json.Unmarshal(endpoint.received("message.failed")[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	res := h.do(http.MethodGet, "/v1/messages/"+body.Data.MessageID, tenant.Token, nil)
	var logged struct {
		Failure json.RawMessage `json:"failure"`
	}
	res.decode(t, &logged)
	var fromHook, fromLog map[string]any
	_ = json.Unmarshal(body.Data.Failure, &fromHook)
	_ = json.Unmarshal(logged.Failure, &fromLog)
	hookJSON, _ := json.Marshal(fromHook)
	logJSON, _ := json.Marshal(fromLog)
	if body.Type != "message.failed" || len(fromHook) == 0 || string(hookJSON) != string(logJSON) {
		t.Errorf("webhook failure %s\nlog failure     %s", hookJSON, logJSON)
	}
}
