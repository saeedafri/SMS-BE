package api_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

const googleClientToken = "test-google-client-token"

// googleRBMStub answers the token exchange and accepts every message, recording
// the agent each went out under.
type googleRBMStub struct {
	mu     sync.Mutex
	agents map[string]string // messageId -> agentId
}

func newGoogleHarness(t *testing.T) (*harness, *googleRBMStub) {
	t.Helper()
	stub := &googleRBMStub{agents: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = io.WriteString(w, `{"access_token":"ya29.test","expires_in":3600}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/agentMessages") {
			stub.mu.Lock()
			stub.agents[r.URL.Query().Get("messageId")] = r.URL.Query().Get("agentId")
			stub.mu.Unlock()
			_, _ = io.WriteString(w, `{}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	account, _ := json.Marshal(map[string]string{
		"client_email": "relay@test.iam.gserviceaccount.com",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"token_uri":    server.URL + "/token",
	})
	google := &connector.GoogleRBM{BaseURL: server.URL, ServiceAccountJSON: account}

	h := newSendHarness(t)
	h.server.RCSCarrier = google
	h.server.Carriers = connector.Registry{Default: h.server.Connector,
		ByChannel: map[string]connector.Connector{"RCS": google}}
	h.server.CarrierWebhookToken = webhookToken
	h.server.GoogleWebhookClientToken = googleClientToken
	h.rebuildRouter()
	return h, stub
}

func (h *harness) postGoogleEvent(event map[string]any, clientToken string) response {
	h.t.Helper()
	data, _ := json.Marshal(event)
	body, _ := json.Marshal(map[string]any{"message": map[string]any{
		"data": base64.StdEncoding.EncodeToString(data), "messageId": uuid.NewString()}})
	mac := hmac.New(sha512.New, []byte(clientToken))
	mac.Write(data)
	return h.doWithHeaders(http.MethodPost, "/v1/carrier-webhooks/rcs/google/"+webhookToken, "",
		json.RawMessage(body), map[string]string{
			"X-Goog-Signature": base64.StdEncoding.EncodeToString(mac.Sum(nil))})
}

// A test send through Google RBM, end to end: an RCS template Relay approved
// needs no carrier approval, the message goes out under the agent's Google
// address, and Google's signed delivery event settles it.
func TestAnRCSMessageGoesThroughGoogleRBMAndItsReceiptSettlesIt(t *testing.T) {
	h, stub := newGoogleHarness(t)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	templateID := h.rcsTemplate(tenant, "Google test", []string{"first_name", "order_id"}, "UTILITY")

	var senderID, agentID uuid.UUID
	if err := h.admin.QueryRow(context.Background(), `
		SELECT s.id, s.rcs_agent_id FROM templates t JOIN sender_ids s ON s.id = t.sender_id
		WHERE t.id = $1`, templateID).Scan(&senderID, &agentID); err != nil {
		t.Fatal(err)
	}
	googleAgent := "relay-test_" + uuid.NewString()[:8] + "_agent@rbm.goog"
	h.launchAgentOnCarrier(tenant, agentID, "GOOGLE", googleAgent)

	res := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": senderID.String(), "templateId": templateID.String(),
		"to": "9876543210", "body": "Hi Priya, your order A-1 shipped.",
		"variables": map[string]string{"first_name": "Priya", "order_id": "A-1"},
	})
	var sent gen.SendMessageResult
	res.decode(t, &sent)
	if res.Code != http.StatusAccepted || sent.Status != "sent" || sent.Id == nil {
		t.Fatalf("send = %d %s", res.Code, res.Body)
	}
	stub.mu.Lock()
	got := stub.agents[sent.Id.String()]
	stub.mu.Unlock()
	if got != googleAgent {
		t.Fatalf("Google received the message under %q, want %q", got, googleAgent)
	}

	delivered := map[string]any{"agentId": googleAgent, "senderPhoneNumber": "+919876543210",
		"eventType": "DELIVERED", "eventId": "e1", "messageId": sent.Id.String()}
	if res := h.postGoogleEvent(delivered, "forged-token"); res.Code != http.StatusNotFound {
		t.Errorf("forged event = %d, want 404", res.Code)
	}
	if status := h.messageStatus(tenant, *sent.Id); status == "delivered" {
		t.Fatal("a forged event settled the message")
	}
	if res := h.postGoogleEvent(delivered, googleClientToken); res.Code != http.StatusOK {
		t.Fatalf("signed event = %d %s", res.Code, res.Body)
	}
	if status := h.messageStatus(tenant, *sent.Id); status != "delivered" {
		t.Errorf("message status = %q, want delivered", status)
	}

	// The GOOGLE launch sends but is not a contract carrier, so it is not listed.
	agent := h.do(http.MethodGet, "/v1/rcs/agents/"+agentID.String(), tenant.Token, nil)
	if bytes.Contains(agent.Body, []byte(`"GOOGLE"`)) {
		t.Errorf("agent response lists a GOOGLE launch the contract cannot render: %s", agent.Body)
	}
}

// Saving the webhook in Google's console sends a handshake that must be
// answered with its secret, and only when the client token is ours.
func TestGooglesWebhookVerificationIsAnsweredOnlyForOurClientToken(t *testing.T) {
	h, _ := newGoogleHarness(t)
	path := "/v1/carrier-webhooks/rcs/google/" + webhookToken
	ok := h.do(http.MethodPost, path, "", map[string]string{
		"clientToken": googleClientToken, "secret": "echo-me"})
	if ok.Code != http.StatusOK || string(ok.Body) != "echo-me" {
		t.Errorf("handshake = %d %q, want 200 with the secret", ok.Code, ok.Body)
	}
	wrong := h.do(http.MethodPost, path, "", map[string]string{
		"clientToken": "someone-else", "secret": "echo-me"})
	if wrong.Code != http.StatusNotFound {
		t.Errorf("handshake with another token = %d, want 404", wrong.Code)
	}
}
