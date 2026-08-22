package api_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// Self-registration must be closed when the deployment says so.
//
// Open signup on a public deployment was half of a go-live blocker reported on
// 2026-08-21: a stranger could self-register, and at the time could then fund
// their own wallet for nothing and send. This is the other half.
func TestSignupIsGatedByAnInviteCode(t *testing.T) {
	h := newHarnessWithInviteCode(t, "let-me-in-please")

	body := map[string]any{
		// Unique per run: a leftover account from a previous run turns the
		// control below into a 409 and hides whether the gate works.
		"fullName": "A Stranger",
		"email":    fmt.Sprintf("stranger-%d@newco.test", time.Now().UnixNano()),
		"password": "a-good-password", "orgName": "Newco", "country": "IN",
	}

	without := h.do(http.MethodPost, "/v1/auth/signup", "", body)
	if without.Code != http.StatusForbidden {
		t.Fatalf("signup with no invite code = %d, want 403\n%s", without.Code, without.Body)
	}

	wrong := h.doWithHeaders(http.MethodPost, "/v1/auth/signup", "", body,
		map[string]string{"X-Invite-Code": "not-the-code"})
	if wrong.Code != http.StatusForbidden {
		t.Fatalf("signup with a wrong code = %d, want 403\n%s", wrong.Code, wrong.Body)
	}

	// The control: without this, a handler that refused everyone would satisfy
	// both assertions above and look correct.
	right := h.doWithHeaders(http.MethodPost, "/v1/auth/signup", "", body,
		map[string]string{"X-Invite-Code": "let-me-in-please"})
	if right.Code != http.StatusOK {
		t.Fatalf("signup with the right code = %d, want 200\n%s", right.Code, right.Body)
	}
}

// An unset code leaves signup open, which is the right default for a local
// instance and is what every other test in this package relies on.
func TestSignupIsOpenWhenNoCodeIsConfigured(t *testing.T) {
	h := newHarness(t)
	res := h.do(http.MethodPost, "/v1/auth/signup", "", map[string]any{
		"fullName": "Open Signup",
		"email":    fmt.Sprintf("open-%d@newco.test", time.Now().UnixNano()),
		"password": "a-good-password", "orgName": "Open Co", "country": "IN",
	})
	if res.Code == http.StatusForbidden {
		t.Fatalf("signup refused with no code configured\n%s", res.Body)
	}
}
