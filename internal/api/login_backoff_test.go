package api_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Sign-in backoff: three wrong passwords lock the address for 30 seconds; after
// the lock it gets three fresh chances, and each further lock doubles — 30s,
// 60s, 120s — up to the surface's maximum.

func lockKey(scope, email string) string {
	sum := sha256.Sum256([]byte(email))
	return fmt.Sprintf("relay:login:lock:%s:%x", scope, sum[:12])
}

func TestThreeWrongPasswordsLockFor30SecondsThenEachLockDoubles(t *testing.T) {
	for _, surface := range []struct{ scope, path string }{
		{"tenant", "/v1/auth/login"}, {"operator", "/v1/operator/login"},
	} {
		t.Run(surface.scope, func(t *testing.T) {
			h := newSendHarness(t)
			if h.server.Redis == nil {
				t.Skip("REDIS_URL not set")
			}
			ctx := context.Background()
			email := fmt.Sprintf("backoff-%d@relay.test", rand.Int())

			for round, want := range []struct {
				lock    time.Duration
				message string
			}{
				{30 * time.Second, "30 seconds"},
				{60 * time.Second, "1 minute"},
				{120 * time.Second, "2 minutes"},
			} {
				// Three chances: the first two are plain refusals.
				for attempt := 1; attempt <= 2; attempt++ {
					if res := h.loginFrom(randomIP(), surface.path, email, "wrong"); res.errorCode(t) != "unauthenticated" {
						t.Fatalf("round %d attempt %d = %s, want a plain refusal", round+1, attempt, res.Body)
					}
				}
				h.loginFrom(randomIP(), surface.path, email, "wrong") // the third locks

				res := h.loginFrom(randomIP(), surface.path, email, "wrong")
				if res.errorCode(t) != "too_many_attempts" || !strings.Contains(string(res.Body), "Try again in "+want.message+".") {
					t.Fatalf("round %d after three wrong = %s, want too_many_attempts saying %q", round+1, res.Body, want.message)
				}
				ttl, err := h.server.Redis.PTTL(ctx, lockKey(surface.scope, email)).Result()
				if err != nil || ttl > want.lock || ttl < want.lock-5*time.Second {
					t.Fatalf("round %d lock lasts %s, want %s", round+1, ttl, want.lock)
				}
				// The lock runs out.
				h.server.Redis.Del(ctx, lockKey(surface.scope, email))
			}
		})
	}
}

// A lock is a lock: the right password during it is refused too.
func TestTheRightPasswordDuringA30SecondLockIsRefused(t *testing.T) {
	h := newSendHarness(t)
	if h.server.Redis == nil {
		t.Skip("REDIS_URL not set")
	}
	account := h.newAccount("owner")
	for i := 0; i < 3; i++ {
		h.loginFrom(randomIP(), "/v1/auth/login", account.Email, "wrong")
	}
	res := h.loginFrom(randomIP(), "/v1/auth/login", account.Email, "test-password-123")
	if res.Code != http.StatusUnauthorized || res.errorCode(t) != "too_many_attempts" {
		t.Fatalf("right password during the lock = %d %s, want 401 too_many_attempts", res.Code, res.Body)
	}
}
