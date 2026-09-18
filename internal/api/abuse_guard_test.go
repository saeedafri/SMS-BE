package api_test

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/saeedafri/sms-be/internal/api"
)

// The abuse guard: every route, per caller address and per credential, with
// automatic bans that escalate for an address that keeps coming back.

func guardHarness(t *testing.T, perIP, perToken int) *harness {
	t.Helper()
	h := newSendHarness(t)
	if h.server.Redis == nil {
		t.Skip("REDIS_URL not set")
	}
	h.server.Abuse = api.AbuseLimits{PerIPMinute: perIP, PerTokenMinute: perToken}
	// A clock the test moves, so no test straddles a window boundary by accident.
	h.clock = time.Now()
	h.server.Now = func() time.Time { return h.clock }
	return h
}

func (h *harness) hit(ip, path, token string) response {
	return h.doFrom(ip+":1234", http.MethodGet, path, token, nil, nil)
}

func retryAfter(t *testing.T, res response) time.Duration {
	t.Helper()
	seconds, err := strconv.Atoi(res.Header.Get("Retry-After"))
	if err != nil {
		t.Fatalf("Retry-After = %q on a %d", res.Header.Get("Retry-After"), res.Code)
	}
	return time.Duration(seconds) * time.Second
}

func TestAnAddressOverItsLimitIsBannedAndOthersAreNot(t *testing.T) {
	t.Parallel()
	h := guardHarness(t, 5, 0)
	attacker, bystander := randomIP(), randomIP()

	for i := 0; i < 5; i++ {
		if res := h.hit(attacker, "/v1/me", ""); res.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d under the limit was refused", i+1)
		}
	}
	res := h.hit(attacker, "/v1/me", "")
	if res.Code != http.StatusTooManyRequests || res.errorCode(t) != "rate_limited" {
		t.Fatalf("6th request = %d %s, want 429 rate_limited", res.Code, res.Body)
	}
	if wait := retryAfter(t, res); wait < 14*time.Minute {
		t.Errorf("first ban lasts %s, want 15 minutes", wait)
	}
	// Still banned two windows later: a ban is not the window.
	h.clock = h.clock.Add(2 * time.Minute)
	if got := h.hit(attacker, "/v1/auth/login", ""); got.Code != http.StatusTooManyRequests {
		t.Errorf("a banned address reached another route: %d", got.Code)
	}
	if got := h.hit(bystander, "/v1/me", ""); got.Code == http.StatusTooManyRequests {
		t.Errorf("another address was refused too")
	}
	if !contains(h.logs.String(), "address banned") {
		t.Error("the ban was not logged, so no alarm can fire on it")
	}
}

func TestAnAddressBannedAgainIsBannedLonger(t *testing.T) {
	t.Parallel()
	h := guardHarness(t, 3, 0)
	attacker := randomIP()
	var bans []time.Duration
	for round := 0; round < 3; round++ {
		var last response
		for i := 0; i < 4; i++ {
			last = h.hit(attacker, "/v1/me", "")
		}
		bans = append(bans, retryAfter(t, last))
		// Lift the ban and the window, keep the history: the next round is the
		// same address coming back.
		clearAbuseState(t, h, attacker, false)
	}
	if !(bans[0] < bans[1] && bans[1] < bans[2]) {
		t.Fatalf("ban lengths %v, want each longer than the last", bans)
	}
}

func TestOneCredentialIsLimitedAcrossAddresses(t *testing.T) {
	t.Parallel()
	h := guardHarness(t, 0, 5)
	tenant := h.newAccount("owner")
	for i := 0; i < 5; i++ {
		h.hit(randomIP(), "/v1/me", tenant.Token)
	}
	if res := h.hit(randomIP(), "/v1/me", tenant.Token); res.Code != http.StatusTooManyRequests {
		t.Fatalf("6th request on one credential from a new address = %d, want 429", res.Code)
	}
	if res := h.hit(randomIP(), "/v1/me", ""); res.Code == http.StatusTooManyRequests {
		t.Error("an address was banned for a credential's burst; only the credential is limited")
	}
}

// Behind the dashboard, the ban falls on the user's own address, never on the
// dashboard server every customer shares.
func TestABanBehindTheDashboardFallsOnTheUser(t *testing.T) {
	t.Parallel()
	h := guardHarness(t, 5, 0)
	h.server.BFFClientIPToken = bffToken
	socket, user, neighbour := randomIP(), randomIP(), randomIP()
	via := func(client string) response {
		return h.doFrom(socket+":443", http.MethodGet, "/v1/me", "", nil,
			map[string]string{"X-Relay-BFF-Token": bffToken, "X-Relay-Client-IP": client})
	}
	for i := 0; i < 6; i++ {
		via(user)
	}
	if res := via(user); res.Code != http.StatusTooManyRequests {
		t.Fatalf("the flooding user = %d, want 429", res.Code)
	}
	if res := via(neighbour); res.Code == http.StatusTooManyRequests {
		t.Fatal("another customer on the same dashboard server was refused")
	}
}

func TestCarrierWebhooksAndHealthAreNeverLimited(t *testing.T) {
	t.Parallel()
	h := guardHarness(t, 2, 0)
	carrier := randomIP()
	for i := 0; i < 6; i++ {
		if res := h.hit(carrier, "/healthz", ""); res.Code == http.StatusTooManyRequests {
			t.Fatal("/healthz was rate limited")
		}
		res := h.doFrom(carrier+":1234", http.MethodPost, "/v1/carrier-webhooks/rcs/airtel/"+webhookToken, "",
			map[string]any{"eventType": "READ", "messageId": "x"}, nil)
		if res.Code == http.StatusTooManyRequests {
			t.Fatal("a carrier's delivery-report burst was rate limited")
		}
	}
}

func TestAnIgnoredNetworkIsNeverLimited(t *testing.T) {
	t.Parallel()
	h := guardHarness(t, 2, 0)
	_, office, _ := net.ParseCIDR("198.51.100.0/24")
	h.server.Abuse.Ignore = []*net.IPNet{office}
	for i := 0; i < 6; i++ {
		if res := h.hit(fmt.Sprintf("198.51.100.%d", 1+rand.Intn(200)), "/v1/me", ""); res.Code == http.StatusTooManyRequests {
			t.Fatal("an address in ABUSE_IGNORE_CIDRS was limited")
		}
	}
}

// Unbanning, as the operator CLI does it.
func TestUnbanLiftsABanAtOnce(t *testing.T) {
	t.Parallel()
	h := guardHarness(t, 2, 0)
	attacker := randomIP()
	for i := 0; i < 3; i++ {
		h.hit(attacker, "/v1/me", "")
	}
	if err := api.Unban(context.Background(), h.server.Redis, attacker); err != nil {
		t.Fatal(err)
	}
	clearAbuseState(t, h, attacker, false)
	if res := h.hit(attacker, "/v1/me", ""); res.Code == http.StatusTooManyRequests {
		t.Fatalf("still refused after unban: %s", res.Body)
	}
}

// clearAbuseState drops the address's ban and current window, and its strike
// history too when forget is set.
func clearAbuseState(t *testing.T, h *harness, ip string, forget bool) {
	t.Helper()
	ctx := context.Background()
	keys, err := h.server.Redis.Keys(ctx, "relay:abuse:*"+ip+"*").Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if !forget && contains(key, ":strikes:") {
			continue
		}
		h.server.Redis.Del(ctx, key)
	}
}
