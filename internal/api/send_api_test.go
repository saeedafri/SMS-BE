package api_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/store"
)

// newSendHarness is the ordinary harness plus the two things a send needs: a
// carrier to submit to and a warehouse to record the message in. Without both,
// the endpoint correctly reports that this deployment cannot send, which is a
// different thing from the endpoint being wrong.
func newSendHarness(t *testing.T) *harness {
	t.Helper()
	chURL := os.Getenv("TEST_CLICKHOUSE_URL")
	if chURL == "" {
		t.Skip("TEST_CLICKHOUSE_URL not set")
	}
	h := newHarness(t)
	// Redis is optional here and the send path treats it that way: without it
	// the rate limiter fails open, which is the deployment's own stance.
	var rdb *redis.Client
	if url := os.Getenv("REDIS_URL"); url != "" {
		client, err := store.OpenRedis(context.Background(), url)
		if err == nil {
			rdb = client
			t.Cleanup(func() { _ = client.Close() })
		}
	}
	// Mutated in place rather than rebuilt: handlers are methods on the Server
	// pointer the router already holds, so adding a dependency here needs no
	// second copy of that literal to keep in step with newHarness.
	pool := store.NewClickHousePool(chURL, nil)
	h.server.ClickHouse = pool
	h.server.Connector = connector.NewSandbox(0)
	h.server.Redis = rdb

	// ClickHouse has no foreign keys, so a message survives the tenant that
	// sent it. Once the Postgres cleanup started actually working, those
	// orphans became the reconciler's problem: it found them stale, tried to
	// refund a wallet whose tenant was gone, and failed on the foreign key —
	// in a DIFFERENT package's test, hours of confusion away from the cause.
	//
	// Registered last so it runs FIRST: t.Cleanup is a stack, and the messages
	// have to go before the tenants they belong to.
	t.Cleanup(func() {
		conn, err := pool.Conn(context.Background())
		if err != nil {
			return
		}
		for _, tenantID := range h.tenants {
			// mutations_sync = 1 because ClickHouse deletes are asynchronous by
			// default: without it the statement returns before anything is
			// gone and the next run finds them all still there.
			_ = conn.Exec(context.Background(),
				`ALTER TABLE messages DELETE WHERE tenant_id = ? SETTINGS mutations_sync = 1`,
				tenantID)
			_ = conn.Exec(context.Background(),
				`ALTER TABLE message_events DELETE WHERE tenant_id = ? SETTINGS mutations_sync = 1`,
				tenantID)
		}
	})
	return h
}

// A customer must be able to send one message from their own code.
//
// Before this, /v1/messages was read-only: the only way to send anything
// through the platform was to build a campaign in the dashboard. And the API
// keys were the other half of it — store.ResolveAPIKey had no callers at all,
// so every sk_live_ key a customer pasted into their integration
// authenticated nothing.
func TestAnApiKeyCanSendOneMessage(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)
	secret := h.apiKey(tenant, []string{"send:sms"})

	sent := h.do(http.MethodPost, "/v1/messages", secret, map[string]any{
		"senderId": sender, "templateId": template,
		"to": "9876543210", "body": "Your order has shipped.",
	})
	if sent.Code != http.StatusAccepted {
		t.Fatalf("send = %d, want 202\n%s", sent.Code, sent.Body)
	}
	var result struct {
		ID        *string `json:"id"`
		Status    string  `json:"status"`
		Segments  int     `json:"segments"`
		CostMinor int64   `json:"costMinor"`
		ErrorCode *string `json:"errorCode"`
	}
	sent.decode(t, &result)
	if result.Status != "sent" && result.Status != "queued" {
		reason := ""
		if result.ErrorCode != nil {
			reason = " (" + *result.ErrorCode + ")"
		}
		t.Fatalf("status = %q%s, want sent or queued", result.Status, reason)
	}
	if result.ID == nil || *result.ID == "" {
		t.Fatal("no message id — the caller cannot look up what happened to it")
	}
	if result.CostMinor <= 0 {
		t.Errorf("costMinor = %d — a send that costs nothing is not billed", result.CostMinor)
	}
}

// A key's scopes have to mean something, or they are decoration on a form.
func TestAnApiKeyWithoutTheSendScopeIsRefused(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	h.fundWallet(tenant)
	secret := h.apiKey(tenant, []string{"read:messages"})

	refused := h.do(http.MethodPost, "/v1/messages", secret, map[string]any{
		"senderId": sender, "to": "9876543210", "body": "Hello",
	})
	if refused.Code != http.StatusForbidden {
		t.Fatalf("send without send:sms = %d, want 403\n%s", refused.Code, refused.Body)
	}
}

// An API key is not a session. It carries scopes and no role, and no other
// handler checks scopes — so a key that authenticated the whole API would let a
// read:messages key read the team roster and the billing history too.
func TestAnApiKeyDoesNotAuthenticateTheRestOfTheApi(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	secret := h.apiKey(tenant, []string{"send:sms", "read:messages"})

	for _, path := range []string{"/v1/team", "/v1/billing/invoices", "/v1/me"} {
		res := h.do(http.MethodGet, path, secret, nil)
		if res.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with an API key = %d, want 401\n%s", path, res.Code, res.Body)
		}
	}
}

func TestSendingWithoutACredentialIsRefused(t *testing.T) {
	h := newSendHarness(t)
	res := h.do(http.MethodPost, "/v1/messages", "", map[string]any{
		"senderId": "00000000-0000-4000-8000-000000000001",
		"to":       "9876543210", "body": "Hello",
	})
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("send with no token = %d, want 401\n%s", res.Code, res.Body)
	}
}

// The dashboard session path, so the endpoint is usable from the product too.
func TestASessionCanSendOneMessage(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	h.fundWallet(tenant)

	sent := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": sender, "to": "9876543210", "body": "Hello from the dashboard.",
	})
	if sent.Code != http.StatusAccepted {
		t.Fatalf("session send = %d, want 202\n%s", sent.Code, sent.Body)
	}
}

func (h *harness) approvedSender(tenant account) string {
	h.t.Helper()
	return h.approvedSenderOn(tenant, "SMS")
}

// approvedSenderOn is the same for a channel other than SMS, which the scope
// tests need: send:sms and send:rcs are separate permissions and cannot be told
// apart without a sender on each.
func (h *harness) approvedSenderOn(tenant account, channel string) string {
	h.t.Helper()
	var id string
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, 'SENDAP', $2, 'IN', 'approved') RETURNING id`,
		tenant.TenantID, channel).Scan(&id); err != nil {
		h.t.Fatalf("seed sender: %v", err)
	}
	return id
}

// wildcardTemplate is an approved template whose entire body is one variable,
// which makes it a legal registered template for any text.
//
// India's regime requires every send to carry a registered template and to be
// an instantiation of it. That rule has its own tests, in the messaging package
// and in send_compliance_test.go. These tests are about authentication, scopes,
// throttling and settlement; making each of them construct a matching body
// would make them worse at the thing they exist to check, without testing the
// binding any harder.
func (h *harness) wildcardTemplate(tenant account, senderID string) string {
	h.t.Helper()
	var templateID string
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO templates (tenant_id, sender_id, name, channel, country, body, status)
		VALUES ($1, $2, $3, 'SMS', 'IN', '{{message}}', 'approved') RETURNING id`,
		tenant.TenantID, senderID,
		fmt.Sprintf("Wildcard %d", h.nextSenderSeq())).Scan(&templateID); err != nil {
		h.t.Fatalf("seed wildcard template: %v", err)
	}
	return templateID
}

func (h *harness) fundWallet(tenant account) {
	h.t.Helper()
	h.appendTopup(tenant, "INR", 1_000_000)
}

func (h *harness) apiKey(tenant account, scopes []string) string {
	h.t.Helper()
	key, err := store.CreateAPIKey(context.Background(), h.pool,
		store.Identity{TenantID: tenant.TenantID}, "test key", "test", scopes)
	if err != nil {
		h.t.Fatalf("create api key: %v", err)
	}
	return key.Secret
}

// A public send endpoint with no throttle is a leaked key or a retry loop away
// from draining a wallet as fast as the network allows.
//
// GetRateLimit has always told customers their budget — 100 a second on live,
// 10 on test — and until now nothing enforced it. The numbers here are that
// same tier: a limit a customer is shown and a limit that is applied have to be
// one number.
func TestSendsAreThrottledToTheTierTheCustomerIsShown(t *testing.T) {
	if os.Getenv("REDIS_URL") == "" {
		t.Skip("REDIS_URL not set — the limiter fails open without Redis, by design")
	}
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	h.fundWallet(tenant)
	// A test key: burst 20, so the limit is reachable in a test without firing
	// two hundred sends at ClickHouse.
	secret := h.apiKey(tenant, []string{"send:sms"})

	// Fired concurrently, not in a loop.
	//
	// The window is one second, so a sequential loop only trips the limit while
	// the machine is fast enough to finish 30 sends inside it. Alone it was; run
	// as part of the full suite it was not, and the test failed on load rather
	// than on behaviour. Concurrency makes the burst a burst regardless.
	const burst = 30
	codes := make(chan int, burst)
	var wg sync.WaitGroup
	for attempt := 0; attempt < burst; attempt++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := h.do(http.MethodPost, "/v1/messages", secret, map[string]any{
				"senderId": sender, "to": "9876543210", "body": "Hello",
			})
			codes <- res.Code
		}()
	}
	wg.Wait()
	close(codes)

	throttled := 0
	for code := range codes {
		if code == http.StatusTooManyRequests {
			throttled++
		}
	}
	if throttled == 0 {
		t.Fatalf("%d concurrent sends and not one was throttled — the rate limit the "+
			"developer settings page advertises is not enforced", burst)
	}
}

// A sender that is not approved must be refused, not crash.
//
// Found on production the day the endpoint shipped: sending from the fixture's
// pending header returned 500 "an unexpected error occurred". The customer
// cannot tell a rejected send from a broken platform, and neither can we.
func TestSendingFromAnUnapprovedSenderIsRefusedCleanly(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	var pending string
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, 'PENDNG', 'SMS', 'IN', 'pending_review') RETURNING id`,
		tenant.TenantID).Scan(&pending); err != nil {
		t.Fatalf("seed pending sender: %v", err)
	}

	res := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": pending, "to": "9876543210", "body": "Hello",
	})
	if res.Code >= 500 {
		t.Fatalf("sending from a pending sender = %d\n%s", res.Code, res.Body)
	}
	if res.Code == http.StatusAccepted {
		var result struct {
			Status    string  `json:"status"`
			ErrorCode *string `json:"errorCode"`
		}
		res.decode(t, &result)
		if result.Status == "sent" || result.Status == "queued" {
			t.Fatalf("a pending sender sent a message (status %q)", result.Status)
		}
		if result.ErrorCode == nil || *result.ErrorCode == "" {
			t.Error("refused with no reason the caller can act on")
		}
	}
}

// Every gate refusal has to come back as an answer, not a crash. Each of these
// is a rule a customer can hit on their first integration.
func TestEveryGateRefusalIsAnAnswerNotAFiveHundred(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	h.fundWallet(tenant)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"a recipient that is not a number", map[string]any{
			"senderId": sender, "to": "not-a-phone-number", "body": "Hello"}},
		{"a template that belongs to another sender", map[string]any{
			"senderId": sender, "to": "9876543210", "body": "Hello",
			"templateId": h.templateForOtherSender(tenant)}},
	}
	for _, c := range cases {
		res := h.do(http.MethodPost, "/v1/messages", tenant.Token, c.body)
		if res.Code >= 500 {
			t.Errorf("%s = %d\n%s", c.name, res.Code, res.Body)
			continue
		}
		if res.Code == http.StatusAccepted {
			var result struct {
				Status    string  `json:"status"`
				ErrorCode *string `json:"errorCode"`
			}
			res.decode(t, &result)
			if result.Status == "sent" || result.Status == "queued" {
				t.Errorf("%s was accepted and sent", c.name)
			}
			if result.ErrorCode == nil || *result.ErrorCode == "" {
				t.Errorf("%s: refused with no reason", c.name)
			}
		}
	}
}

// A template hung off a DIFFERENT approved sender, which the gate refuses.
func (h *harness) templateForOtherSender(tenant account) string {
	h.t.Helper()
	var otherSender, template string
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, 'OTHERS', 'SMS', 'IN', 'approved') RETURNING id`,
		tenant.TenantID).Scan(&otherSender); err != nil {
		h.t.Fatalf("seed other sender: %v", err)
	}
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO templates (tenant_id, sender_id, name, channel, country, body, status)
		VALUES ($1, $2, 'Mismatched', 'SMS', 'IN', 'Hello', 'approved') RETURNING id`,
		tenant.TenantID, otherSender).Scan(&template); err != nil {
		h.t.Fatalf("seed template: %v", err)
	}
	return template
}

// walletBalance reads the tenant's INR balance straight from the ledger, which
// is the only place money is authoritative. Reading it through the API would
// test the wallet endpoint at the same time and confuse a failure there with a
// failure in what is actually under test.
func (h *harness) walletBalance(tenant account) int64 {
	h.t.Helper()
	balances, err := store.ListWalletBalances(context.Background(), h.pool,
		store.Identity{TenantID: tenant.TenantID})
	if err != nil {
		h.t.Fatalf("read balances: %v", err)
	}
	for _, entry := range balances {
		if entry.Currency == "INR" {
			return entry.BalanceMinor
		}
	}
	return 0
}

// messageStatus reads one message's current state out of the warehouse.
//
// Ordered by version because ReplacingMergeTree keeps every version until a
// merge runs — an unqualified read returns whichever row it finds first, which
// is often the pre-submit one, and the test then fails describing a state the
// message left milliseconds ago.
func (h *harness) messageStatus(tenant account, messageID uuid.UUID) string {
	h.t.Helper()
	conn, err := store.NewClickHousePool(os.Getenv("TEST_CLICKHOUSE_URL"), nil).
		Conn(context.Background())
	if err != nil {
		h.t.Fatalf("clickhouse: %v", err)
	}
	var status string
	if err := conn.QueryRow(context.Background(), `
		SELECT status FROM messages
		 WHERE tenant_id = ? AND id = ?
		 ORDER BY version DESC LIMIT 1`,
		tenant.TenantID, messageID).Scan(&status); err != nil {
		h.t.Fatalf("read message status: %v", err)
	}
	return status
}

// messageCarrierRef reads the two fields a later carrier callback depends on.
func (h *harness) messageCarrierRef(tenant account, messageID uuid.UUID) (carrierRef, templateID string) {
	h.t.Helper()
	conn, err := store.NewClickHousePool(os.Getenv("TEST_CLICKHOUSE_URL"), nil).
		Conn(context.Background())
	if err != nil {
		h.t.Fatalf("clickhouse: %v", err)
	}
	var ref, template *string
	if err := conn.QueryRow(context.Background(), `
		SELECT carrier_ref, toString(template_id) FROM messages
		 WHERE tenant_id = ? AND id = ?
		 ORDER BY version DESC LIMIT 1`,
		tenant.TenantID, messageID).Scan(&ref, &template); err != nil {
		h.t.Fatalf("read carrier ref: %v", err)
	}
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return deref(ref), deref(template)
}

// messageCarrier reads which carrier the log says carried a message.
func (h *harness) messageCarrier(tenant account, messageID uuid.UUID) string {
	h.t.Helper()
	conn, err := store.NewClickHousePool(os.Getenv("TEST_CLICKHOUSE_URL"), nil).
		Conn(context.Background())
	if err != nil {
		h.t.Fatalf("clickhouse: %v", err)
	}
	var carrier string
	if err := conn.QueryRow(context.Background(), `
		SELECT carrier FROM messages
		 WHERE tenant_id = ? AND id = ?
		 ORDER BY version DESC LIMIT 1`,
		tenant.TenantID, messageID).Scan(&carrier); err != nil {
		h.t.Fatalf("read carrier: %v", err)
	}
	return carrier
}
