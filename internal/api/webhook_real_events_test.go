package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/webhook"
)

// P1-4: customer webhooks fire for real messages, signed with the secret the
// customer was shown.

type receivedHook struct {
	Event     string
	Timestamp int64
	Signature string
	Body      []byte
}

// customerEndpoint is a customer's HTTPS receiver. statuses are answered in
// order, the last one repeating.
type customerEndpoint struct {
	server   *httptest.Server
	mu       sync.Mutex
	got      []receivedHook
	statuses []int
}

func startCustomerEndpoint(t *testing.T, statuses ...int) *customerEndpoint {
	t.Helper()
	if len(statuses) == 0 {
		statuses = []int{http.StatusOK}
	}
	c := &customerEndpoint{statuses: statuses}
	c.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ts, _ := strconv.ParseInt(r.Header.Get("X-Relay-Timestamp"), 10, 64)
		c.mu.Lock()
		c.got = append(c.got, receivedHook{Event: r.Header.Get("X-Relay-Event"), Timestamp: ts,
			Signature: strings.TrimPrefix(r.Header.Get("X-Relay-Signature"), "v1="), Body: body})
		status := c.statuses[min(len(c.got)-1, len(c.statuses)-1)]
		c.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(c.server.Close)
	previous := webhook.HTTPClient
	webhook.HTTPClient = c.server.Client()
	t.Cleanup(func() { webhook.HTTPClient = previous })
	return c
}

func (c *customerEndpoint) received(event string) []receivedHook {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []receivedHook
	for _, hook := range c.got {
		if hook.Event == event {
			out = append(out, hook)
		}
	}
	return out
}

// subscribeEndpoint creates the endpoint through the API, so the secret is the
// one a customer is shown, then points it at the local receiver (creation
// refuses loopback addresses, rightly).
func (h *harness) subscribeEndpoint(tenant account, endpoint *customerEndpoint, events ...string) gen.WebhookEndpointCreated {
	h.t.Helper()
	res := h.do(http.MethodPost, "/v1/developer/webhooks", tenant.Token, map[string]any{
		"url": "https://" + uuid.NewString()[:8] + ".example.com/hook", "environment": "test",
		"subscribedEvents": events,
	})
	if res.Code != http.StatusCreated {
		h.t.Fatalf("create webhook = %d: %s", res.Code, res.Body)
	}
	var created gen.WebhookEndpointCreated
	res.decode(h.t, &created)
	if _, err := h.admin.Exec(context.Background(),
		`UPDATE webhook_endpoints SET url = $2 WHERE id = $1`, created.Id, endpoint.server.URL); err != nil {
		h.t.Fatal(err)
	}
	return created
}

// deliverOneSMS sends through the sandbox and drains its report the way the
// delivery-drainer worker does.
func (h *harness) deliverOneSMS(tenant account, to string) {
	h.t.Helper()
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)
	if res := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": sender, "templateId": template, "to": to, "body": "Your order has shipped.",
	}); res.Code != http.StatusAccepted {
		h.t.Fatalf("send = %d: %s", res.Code, res.Body)
	}
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

func waitFor(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestADeliveredSMSEmitsOneEventSignedWithTheFullSecret(t *testing.T) {
	h := newSendHarness(t)
	endpoint := startCustomerEndpoint(t)
	tenant := h.newAccount("owner")
	created := h.subscribeEndpoint(tenant, endpoint, "message.delivered")

	h.deliverOneSMS(tenant, "9876543210")

	waitFor(t, "message.delivered at the customer's endpoint", func() bool {
		return len(endpoint.received("message.delivered")) > 0
	})
	time.Sleep(500 * time.Millisecond)
	hooks := endpoint.received("message.delivered")
	if len(hooks) != 1 {
		t.Fatalf("message.delivered arrived %d times, want exactly 1", len(hooks))
	}
	hook := hooks[0]
	if want := webhook.Sign(created.SigningSecret, hook.Timestamp, hook.Body); hook.Signature != want {
		t.Errorf("signature does not verify with the secret shown at creation")
	}
	if prefixSigned := webhook.Sign(created.SigningSecretPrefix, hook.Timestamp, hook.Body); hook.Signature == prefixSigned {
		t.Errorf("the event is signed with the display prefix, which no customer can verify")
	}
	var payload struct {
		Event string `json:"event"`
		Data  struct {
			MessageID string `json:"messageId"`
			Status    string `json:"status"`
			Msisdn    string `json:"msisdn"`
		} `json:"data"`
	}
	if err := json.Unmarshal(hook.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Event != "message.delivered" || payload.Data.Status != "delivered" ||
		payload.Data.Msisdn != "+919876543210" || payload.Data.MessageID == "" {
		t.Errorf("payload = %s", hook.Body)
	}
}

func TestAnUndeliveredSMSEmitsMessageFailed(t *testing.T) {
	h := newSendHarness(t)
	endpoint := startCustomerEndpoint(t)
	tenant := h.newAccount("owner")
	h.subscribeEndpoint(tenant, endpoint, "message.failed")

	h.deliverOneSMS(tenant, "9876543001") // sandbox: accepted, then ABSENT_SUBSCRIBER

	waitFor(t, "message.failed at the customer's endpoint", func() bool {
		return len(endpoint.received("message.failed")) == 1
	})
}

func TestACustomer500IsRetriedAndEachAttemptLogged(t *testing.T) {
	h := newSendHarness(t)
	h.server.WebhookRetryDelays = []time.Duration{50 * time.Millisecond, 50 * time.Millisecond}
	endpoint := startCustomerEndpoint(t, http.StatusInternalServerError, http.StatusOK)
	tenant := h.newAccount("owner")
	created := h.subscribeEndpoint(tenant, endpoint, "message.delivered")

	h.deliverOneSMS(tenant, "9876543210")

	waitFor(t, "the retry to succeed", func() bool { return len(endpoint.received("message.delivered")) == 2 })
	var log struct {
		Events []struct {
			Attempt int    `json:"attempt"`
			Outcome string `json:"outcome"`
		} `json:"events"`
	}
	waitFor(t, "both attempts in the delivery log", func() bool {
		h.do(http.MethodGet, "/v1/developer/webhooks/"+created.Id.String()+"/events", tenant.Token, nil).decode(t, &log)
		return len(log.Events) == 2
	})
	outcomes := map[int]string{}
	for _, e := range log.Events {
		outcomes[e.Attempt] = e.Outcome
	}
	if outcomes[1] != "failed" || outcomes[2] != "succeeded" {
		t.Errorf("delivery log = %+v, want attempt 1 failed and attempt 2 succeeded", log.Events)
	}
}

// An endpoint created before secrets were stored has no secret to sign with.
// It is skipped, and the log says why, rather than signed with something the
// customer cannot verify.
func TestAnEndpointWithNoStoredSecretIsSkippedAndSaysWhy(t *testing.T) {
	h := newSendHarness(t)
	endpoint := startCustomerEndpoint(t)
	tenant := h.newAccount("owner")
	created := h.subscribeEndpoint(tenant, endpoint, "message.delivered")
	if _, err := h.admin.Exec(context.Background(),
		`UPDATE webhook_endpoints SET signing_secret_sealed = NULL WHERE id = $1`, created.Id); err != nil {
		t.Fatal(err)
	}

	h.deliverOneSMS(tenant, "9876543210")

	var log struct {
		Events []struct {
			Outcome         string  `json:"outcome"`
			ResponseSnippet *string `json:"responseSnippet"`
		} `json:"events"`
	}
	waitFor(t, "a skipped attempt in the delivery log", func() bool {
		h.do(http.MethodGet, "/v1/developer/webhooks/"+created.Id.String()+"/events", tenant.Token, nil).decode(t, &log)
		return len(log.Events) == 1
	})
	if log.Events[0].Outcome != "failed" || log.Events[0].ResponseSnippet == nil ||
		!strings.Contains(*log.Events[0].ResponseSnippet, "Recreate") {
		t.Errorf("log = %+v, want a failed attempt telling the customer to recreate the endpoint", log.Events)
	}
	if n := len(endpoint.received("message.delivered")); n != 0 {
		t.Errorf("an endpoint with no secret received %d events", n)
	}
}
