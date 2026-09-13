package api_test

import (
	"fmt"
	"math/rand"
	"net/http"
	"testing"
)

func (h *harness) loginFrom(ip, path, email, password string) response {
	return h.doWithHeaders(http.MethodPost, path, "", map[string]string{"email": email, "password": password},
		map[string]string{"X-Forwarded-For": ip})
}

func randomIP() string {
	return fmt.Sprintf("198.18.%d.%d", rand.Intn(255), 1+rand.Intn(250))
}

// Five wrong passwords lock an address: the sixth attempt is refused even with
// the right password, with the same answer an unknown address gets. Another
// account from another address is unaffected, and a success before the limit
// resets the count.
func TestRepeatedWrongPasswordsLockTheAccountForAWhile(t *testing.T) {
	h := newSendHarness(t)
	if h.server.Redis == nil {
		t.Skip("REDIS_URL not set")
	}
	victim := h.newAccount("owner")
	other := h.newAccount("owner")
	ip := randomIP()

	for i := 0; i < 2; i++ {
		h.loginFrom(ip, "/v1/auth/login", victim.Email, "wrong-password-1")
	}
	if res := h.loginFrom(ip, "/v1/auth/login", victim.Email, "test-password-123"); res.Code != http.StatusOK {
		t.Fatalf("correct password after two mistakes = %d\n%s", res.Code, res.Body)
	}
	for i := 0; i < 4; i++ {
		if res := h.loginFrom(ip, "/v1/auth/login", victim.Email, "wrong-password-1"); res.errorCode(t) != "unauthenticated" {
			t.Fatalf("attempt %d after a success was treated as locked: %s", i+1, res.Body)
		}
	}
	h.loginFrom(ip, "/v1/auth/login", victim.Email, "wrong-password-1") // fifth failure locks
	res := h.loginFrom(randomIP(), "/v1/auth/login", victim.Email, "test-password-123")
	if res.Code != http.StatusUnauthorized || res.errorCode(t) != "too_many_attempts" {
		t.Fatalf("correct password on a locked account = %d %s, want 401 too_many_attempts", res.Code, res.Body)
	}
	if res := h.loginFrom(randomIP(), "/v1/auth/login", other.Email, "test-password-123"); res.Code != http.StatusOK {
		t.Errorf("an unrelated account was locked too: %d %s", res.Code, res.Body)
	}
}

// Operators lock after three.
func TestOperatorLoginLocksAfterThreeWrongPasswords(t *testing.T) {
	h := newSendHarness(t)
	if h.server.Redis == nil {
		t.Skip("REDIS_URL not set")
	}
	email := fmt.Sprintf("nobody-%d@relay.test", rand.Int())
	ip := randomIP()
	for i := 0; i < 3; i++ {
		if res := h.loginFrom(ip, "/v1/operator/login", email, "wrong"); res.errorCode(t) != "unauthenticated" {
			t.Fatalf("attempt %d = %s, want a plain refusal", i+1, res.Body)
		}
	}
	if res := h.loginFrom(ip, "/v1/operator/login", email, "wrong"); res.errorCode(t) != "too_many_attempts" {
		t.Fatalf("fourth operator attempt = %s, want too_many_attempts", res.Body)
	}
}
