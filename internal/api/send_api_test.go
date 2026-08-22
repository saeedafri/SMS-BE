package api_test

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/saeedafri/sms-be/internal/api"
	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/domain/billing"
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
	h.router = api.NewRouter(&api.Server{
		DB: h.pool, OperatorDB: h.operatorPool, AdminDB: h.admin,
		EnableDevEndpoints: true, Gateway: billing.ManualGateway{},
		ClickHouse: store.NewClickHousePool(chURL, nil),
		Connector:  connector.NewSandbox(0),
		Redis:      rdb,
		Logger:     slog.New(slog.NewJSONHandler(h.logs, nil)),
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
	h.fundWallet(tenant)
	secret := h.apiKey(tenant, []string{"send:sms"})

	sent := h.do(http.MethodPost, "/v1/messages", secret, map[string]any{
		"senderId": sender, "to": "9876543210", "body": "Your order has shipped.",
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
	var id string
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, 'SENDAP', 'SMS', 'IN', 'approved') RETURNING id`,
		tenant.TenantID).Scan(&id); err != nil {
		h.t.Fatalf("seed sender: %v", err)
	}
	return id
}

func (h *harness) fundWallet(tenant account) {
	h.t.Helper()
	if _, err := store.AppendLedgerEntry(context.Background(), h.pool,
		store.Identity{TenantID: tenant.TenantID}, store.LedgerEntry{
			Currency: "INR", Type: "topup", AmountMinor: 1_000_000,
		}); err != nil {
		h.t.Fatalf("fund wallet: %v", err)
	}
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

	throttled := 0
	for attempt := 0; attempt < 30; attempt++ {
		res := h.do(http.MethodPost, "/v1/messages", secret, map[string]any{
			"senderId": sender, "to": "9876543210", "body": "Hello",
		})
		if res.Code == http.StatusTooManyRequests {
			throttled++
		}
	}
	if throttled == 0 {
		t.Fatal("30 sends in a burst and not one was throttled — the rate limit the " +
			"developer settings page advertises is not enforced")
	}
}
