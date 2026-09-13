package connector

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
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
)

type sentMessage struct {
	agentID string
	body    string
}

// fakeGoogle is Google's token endpoint and the RBM calls Relay makes.
type fakeGoogle struct {
	server *httptest.Server
	key    *rsa.PrivateKey

	mu        sync.Mutex
	tokens    int
	sends     map[string]sentMessage // by messageId
	reachable map[string]bool
}

func startFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeGoogle{key: key, sends: map[string]sentMessage{},
		reachable: map[string]bool{"+919820000001": true}}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/token":
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" ||
			!f.validAssertion(r.Form.Get("assertion")) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.tokens++
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{"access_token":"ya29.test","expires_in":3600}`)
		return
	case r.Header.Get("Authorization") != "Bearer ya29.test":
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	phone := strings.TrimPrefix(r.URL.Path, "/v1/phones/")
	phone = phone[:strings.Index(phone, "/")]
	switch {
	case strings.HasSuffix(r.URL.Path, "/capabilities"):
		if !f.reachable[phone] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"features":["RICHCARD_STANDALONE","ACTION_DIAL"]}`)
	case strings.HasSuffix(r.URL.Path, "/agentMessages") && r.Method == http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.sends[r.URL.Query().Get("messageId")] = sentMessage{
			agentID: r.URL.Query().Get("agentId"), body: string(body)}
		f.mu.Unlock()
		if !f.reachable[phone] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// validAssertion checks the JWT is RS256-signed by the service account's key
// and asks for the RBM scope at this token endpoint.
func (f *fakeGoogle) validAssertion(assertion string) bool {
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&f.key.PublicKey, crypto.SHA256, digest[:], signature) != nil {
		return false
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	_ = json.Unmarshal(raw, &claims)
	return claims["scope"] == googleRBMScope && claims["aud"] == f.server.URL+"/token" &&
		claims["iss"] == "relay@test.iam.gserviceaccount.com"
}

func (f *fakeGoogle) adapter(t *testing.T) *GoogleRBM {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(f.key)
	if err != nil {
		t.Fatal(err)
	}
	account, _ := json.Marshal(map[string]string{
		"type": "service_account", "client_email": "relay@test.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"token_uri":   f.server.URL + "/token",
	})
	return &GoogleRBM{BaseURL: f.server.URL, ServiceAccountJSON: account}
}

// A tester's handset reports its features; a phone that is not a tester, or
// has no RCS, is unreachable rather than an error. Both calls share one token.
func TestGoogleRBMCapabilityUsesAServiceAccountToken(t *testing.T) {
	fake := startFakeGoogle(t)
	google := fake.adapter(t)

	reachable, err := google.Capability(context.Background(), "relay_agent@rbm.goog", "+919820000001")
	if err != nil || !reachable.Reachable || !reachable.Supports(RCSActionDial) {
		t.Fatalf("tester = %+v, %v; want reachable with its features", reachable, err)
	}
	other, err := google.Capability(context.Background(), "relay_agent@rbm.goog", "+919820000002")
	if err != nil || other.Reachable {
		t.Fatalf("non-tester = %+v, %v; want unreachable, no error", other, err)
	}
	if fake.tokens != 1 {
		t.Errorf("minted %d tokens for two calls, want 1", fake.tokens)
	}
	if _, err := google.Capability(context.Background(), "", "+919820000001"); err != ErrRCSNoAgent {
		t.Errorf("no agent = %v, want ErrRCSNoAgent", err)
	}
}

// A message goes to the handset under the customer's agent, with Relay's own
// message id so Google's receipts quote it back.
func TestGoogleRBMSendsTheBodyUnderTheAgentWithRelaysMessageID(t *testing.T) {
	fake := startFakeGoogle(t)
	google := fake.adapter(t)

	receipts, err := google.Submit(context.Background(), []Submission{
		{MessageID: "m-1", Msisdn: "+919820000001", Channel: "RCS", Body: "Your order shipped",
			AgentID: "relay_agent@rbm.goog", TTLSeconds: 600},
		{MessageID: "m-2", Msisdn: "+919820000002", Channel: "RCS", Body: "hi",
			AgentID: "relay_agent@rbm.goog"},
		{MessageID: "m-3", Msisdn: "+919820000001", Channel: "RCS", Body: "hi"},
		{MessageID: "m-4", Msisdn: "+919820000001", Channel: "RCS", AgentID: "relay_agent@rbm.goog"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"m-1": "", "m-2": "unreachable_handset",
		"m-3": "agent_not_resolved", "m-4": "body_required"}
	for _, receipt := range receipts {
		if receipt.ErrorCode != want[receipt.MessageID] || receipt.Accepted != (want[receipt.MessageID] == "") {
			t.Errorf("%s = %+v, want error %q", receipt.MessageID, receipt, want[receipt.MessageID])
		}
		if receipt.MessageID == "m-1" && receipt.CarrierRef != "m-1" {
			t.Errorf("carrier ref = %q, want Relay's message id", receipt.CarrierRef)
		}
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sends) != 2 {
		t.Errorf("Google saw %d sends, want only the two with an agent and a body", len(fake.sends))
	}
	sent := fake.sends["m-1"]
	if sent.agentID != "relay_agent@rbm.goog" ||
		sent.body != `{"contentMessage":{"text":"Your order shipped"},"ttl":"600s"}` {
		t.Errorf("m-1 reached Google as %+v, want the body and ttl under the agent", sent)
	}
}

// Google signs its callbacks. A delivery event with the right signature is read
// with Google's agent address as the agent; a forged one is refused; the
// console's verification handshake is recognised.
func TestGoogleWebhooksAreSignedAndCarryTheAgentAddress(t *testing.T) {
	const clientToken = "partner-client-token"
	event := `{"agentId":"relay_agent@rbm.goog","senderPhoneNumber":"+919820000001",` +
		`"eventType":"DELIVERED","eventId":"e1","messageId":"m-1","sendTime":"2026-09-13T10:00:00.123Z"}`
	envelope, _ := json.Marshal(map[string]any{"message": map[string]any{
		"data": base64.StdEncoding.EncodeToString([]byte(event)), "messageId": "p1"}})
	mac := hmac.New(sha512.New, []byte(clientToken))
	mac.Write([]byte(event))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !VerifyGoogleWebhookSignature(envelope, signature, clientToken) {
		t.Fatal("a correctly signed event was refused")
	}
	if VerifyGoogleWebhookSignature(envelope, signature, "another-token") ||
		VerifyGoogleWebhookSignature(envelope, "", clientToken) {
		t.Error("a wrongly signed or unsigned event was accepted")
	}

	parsed, err := ParseGoogleWebhook(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Kind != RCSEventDelivery || !parsed.Delivered || parsed.CarrierRef != "m-1" ||
		parsed.AgentID != "relay_agent@rbm.goog" || parsed.Vendor != "google" {
		t.Errorf("event = %+v", parsed)
	}

	handshake, ok := ParseGoogleWebhookVerification([]byte(`{"clientToken":"partner-client-token","secret":"s3"}`))
	if !ok || handshake.Secret != "s3" {
		t.Errorf("handshake = %+v, %v", handshake, ok)
	}
	if _, ok := ParseGoogleWebhookVerification(envelope); ok {
		t.Error("an event was read as a verification handshake")
	}
}
