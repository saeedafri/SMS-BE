package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// Trustsignal (Sigmo) end to end: the real connector against a fake
// rcsapi.trustsignal.io, every other layer real.

type fakeTrustsignalAPI struct {
	mu       sync.Mutex
	sends    []map[string]any
	keys     []string
	template map[string]any
	shape    string
	issued   string
}

func newTrustsignalHarness(t *testing.T) (*harness, *fakeTrustsignalAPI) {
	t.Helper()
	fake := &fakeTrustsignalAPI{issued: "ts-tpl-" + uuid.NewString()[:8]}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.keys = append(fake.keys, r.URL.Query().Get("api_key"))
		switch r.URL.Path {
		case "/api/v1/rcs/with_fallback":
			fake.sends = append(fake.sends, body)
			fmt.Fprintf(w, `{"success":true,"results":{"to":%q,"transaction_id":"TS-%d-%s"}}`,
				body["to"], len(fake.sends), fake.issued)
		case "/api/v1/template":
			fake.template, fake.shape = body, r.URL.Query().Get("s")
			fmt.Fprintf(w, `{"success":true,"template":{"id":%q,"status":"pending"}}`, fake.issued)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	carrier := &connector.TrustsignalRCS{BaseURL: server.URL, APIKey: "ts-test-key"}
	h := newSendHarness(t)
	h.server.RCSCarrier = carrier
	h.server.Carriers = connector.Registry{Default: h.server.Connector,
		ByChannel: map[string]connector.Connector{"RCS": carrier}}
	h.server.CarrierWebhookToken = webhookToken
	h.rebuildRouter()
	return h, fake
}

func (f *fakeTrustsignalAPI) lastSend() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) == 0 {
		return nil
	}
	return f.sends[len(f.sends)-1]
}

func (h *harness) trustsignalApproved(tenant account, name string) (senderID, templateID uuid.UUID, carrierID string) {
	h.t.Helper()
	carrierID = "ts-approved-" + uuid.NewString()[:8]
	templateID = h.rcsTemplate(tenant, name, []string{"first_name", "order_id"}, "UTILITY")
	if err := h.admin.QueryRow(context.Background(), `
		UPDATE templates SET carrier_vendor = 'trustsignal', carrier_template_id = $2,
		       carrier_status = 'approved', carrier_submitted_at = now()
		 WHERE id = $1 RETURNING sender_id`, templateID, carrierID).Scan(&senderID); err != nil {
		h.t.Fatalf("approve trustsignal template: %v", err)
	}
	return senderID, templateID, carrierID
}

func TestATrustsignalSendIsSettledByItsDeliveryWebhook(t *testing.T) {
	t.Parallel()
	h, fake := newTrustsignalHarness(t)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	senderID, templateID, carrierID := h.trustsignalApproved(tenant, "TS send")

	send := func(to, orderID string) uuid.UUID {
		res := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
			"senderId": senderID.String(), "templateId": templateID.String(), "to": to,
			"body":      "Hi Priya, your order " + orderID + " shipped.",
			"variables": map[string]string{"first_name": "Priya", "order_id": orderID},
		})
		if res.Code != http.StatusAccepted {
			t.Fatalf("send = %d %s", res.Code, res.Body)
		}
		var sent gen.SendMessageResult
		res.decode(t, &sent)
		if sent.Id == nil || (sent.Status != "sent" && sent.Status != "accepted") {
			t.Fatalf("send result = %s", res.Body)
		}
		return *sent.Id
	}

	delivered := send("9876543215", "A-5")
	body := fake.lastSend()
	if body["to"] != "+919876543215" || body["template_id"] != carrierID {
		t.Errorf("trustsignal saw %v, want the carrier's template id and an E.164 number", body)
	}
	variables, _ := body["rcs_variables"].(map[string]any)
	if variables["first_name"] != "Priya" || variables["order_id"] != "A-5" {
		t.Errorf("rcs_variables = %v", body["rcs_variables"])
	}
	if fake.keys[0] != "ts-test-key" {
		t.Errorf("api_key = %q", fake.keys[0])
	}

	res := h.postWebhook("trustsignal", webhookToken, map[string]any{
		"webhook_type": "rcs_message", "transaction_id": "TS-1-" + fake.issued,
		"status": "read", "to": "+919876543215", "dlrt": "2026-10-01T10:15:03Z",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("webhook = %d %s", res.Code, res.Body)
	}
	if status := h.messageStatus(tenant, delivered); status != "delivered" {
		t.Errorf("read report: status = %q, want delivered", status)
	}

	// A handset that cannot take RCS fails the message and releases the hold.
	nonRCS := send("9876543216", "A-6")
	h.postWebhook("trustsignal", webhookToken, map[string]any{
		"webhook_type": "rcs_message", "transaction_id": "TS-2-" + fake.issued, "status": "nonrcs",
	})
	if status := h.messageStatus(tenant, nonRCS); status == "delivered" || status == "accepted" {
		t.Errorf("nonrcs report: status = %q, want a failure", status)
	}
}

func TestATrustsignalCardTemplateRegistersAndItsApprovalArrivesByWebhook(t *testing.T) {
	t.Parallel()
	h, fake := newTrustsignalHarness(t)
	tenant := h.newAccount("owner")
	templateID := h.rcsTemplate(tenant, "TS card", []string{"first_name"}, "MARKETING")
	card, _ := json.Marshal(map[string]any{"kind": "card",
		"card": map[string]any{"title": "Hi {{first_name}}", "description": "20% off today",
			"mediaUrl": "https://cdn.example/sale.png"},
		"suggestions": []any{map[string]any{"type": "open_url", "text": "Shop",
			"url": "https://shop.example"}}})
	if _, err := h.admin.Exec(context.Background(),
		`UPDATE templates SET rcs_content = $2 WHERE id = $1`, templateID, card); err != nil {
		t.Fatal(err)
	}

	res := h.do(http.MethodPost, "/v1/templates/"+templateID.String()+"/carrier-registration",
		tenant.Token, map[string]any{"vendor": "trustsignal"})
	if res.Code != http.StatusOK {
		t.Fatalf("register = %d %s", res.Code, res.Body)
	}
	if fake.shape != "2" || fake.template["type"] != "rich_card" ||
		!strings.HasPrefix(fmt.Sprint(fake.template["botId"]), "ts-bot-") {
		t.Errorf("trustsignal saw s=%s %v, want a rich card under the TRUSTSIGNAL launch's bot",
			fake.shape, fake.template)
	}
	standAlone, _ := fake.template["standAlone"].(map[string]any)
	if standAlone["cardTitle"] != "Hi [first_name]" {
		t.Errorf("standAlone = %v", standAlone)
	}
	vendor, carrierID, status, _ := h.templateCarrierState(templateID)
	if vendor != "trustsignal" || carrierID != fake.issued || status != "pending" {
		t.Errorf("carrier state = %s %s %s", vendor, carrierID, status)
	}

	h.postWebhook("trustsignal", webhookToken, map[string]any{
		"webhook_type": "rcs_template", "template_id": fake.issued, "status": "active"})
	if _, _, status, _ := h.templateCarrierState(templateID); status != "approved" {
		t.Errorf("after the active webhook: carrier_status = %q, want approved", status)
	}
}

func TestAnUnknownRCSVendorIsStillRefused(t *testing.T) {
	t.Parallel()
	h, _ := newTrustsignalHarness(t)
	tenant := h.newAccount("owner")
	templateID := h.rcsTemplate(tenant, "TS vendor", []string{"first_name"}, "UTILITY")
	res := h.do(http.MethodPost, "/v1/templates/"+templateID.String()+"/carrier-registration",
		tenant.Token, map[string]any{"vendor": "bsnl"})
	if res.Code != http.StatusUnprocessableEntity || !strings.Contains(string(res.Body), "trustsignal") {
		t.Errorf("vendor bsnl = %d %s, want 422 naming the vendors", res.Code, res.Body)
	}
}

// Trustsignal has no lookup. Saying "could not be reached" would send someone
// chasing an outage that is not there.
func TestAReachabilityCheckThroughTrustsignalSaysItCannotCheck(t *testing.T) {
	t.Parallel()
	h, _ := newTrustsignalHarness(t)
	tenant := h.newAccount("owner")
	h.approveRegistration(tenant, "IN")
	agentID := uuid.MustParse(h.createAgent(tenant, "TS reach agent").Id)
	h.launchAgentOnCarrier(tenant, agentID, "TRUSTSIGNAL", "ts-bot-"+uuid.NewString()[:8])

	for _, numbers := range [][]string{{"+919820000001"}, {"+919820000001", "+919820000002"}} {
		res := h.do(http.MethodPost, "/v1/rcs/capabilities", tenant.Token,
			map[string]any{"rcsAgentId": agentID.String(), "msisdns": numbers})
		if res.Code != http.StatusServiceUnavailable ||
			!strings.Contains(string(res.Body), "cannot check handsets") {
			t.Errorf("%d numbers: %d %s, want 503 saying it cannot check", len(numbers), res.Code, res.Body)
		}
	}
}
