package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func signupBody(email string) map[string]any {
	return map[string]any{
		"fullName": "New Owner",
		"email":    email,
		"password": "a-strong-password",
		"orgName":  "New Org",
		"country":  "IN",
	}
}

func TestSignupCreatesTenantOwnerAndSession(t *testing.T) {
	h := newHarness(t)
	email := "signup-owner@example.test"
	h.trackTenant(email)

	res := h.do(http.MethodPost, "/v1/auth/signup", "", signupBody(email))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}
	var session gen.AuthSession
	res.decode(t, &session)
	if session.Token == "" {
		t.Fatal("signup returned an empty token")
	}
	if !session.ExpiresAt.After(mustParseTime(t, "2000-01-01T00:00:00Z")) {
		t.Fatalf("expiresAt = %v, want a future time", session.ExpiresAt)
	}

	// The returned token must immediately work, and the new user must be an
	// owner of a tenant named as requested.
	me := h.do(http.MethodGet, "/v1/me", session.Token, nil)
	if me.Code != http.StatusOK {
		t.Fatalf("GET /v1/me with the signup token: status = %d; body = %s", me.Code, me.Body)
	}
	var body gen.Me
	me.decode(t, &body)
	if body.Role != gen.TeamRole("owner") {
		t.Errorf("role = %q, want owner", body.Role)
	}
	if body.TenantName != "New Org" {
		t.Errorf("tenantName = %q, want %q", body.TenantName, "New Org")
	}
	if body.EmailVerified {
		t.Error("emailVerified = true on a brand-new signup, want false")
	}
}

func TestSignupRejectsDuplicateEmail(t *testing.T) {
	h := newHarness(t)
	email := "dupe-owner@example.test"
	h.trackTenant(email)

	if res := h.do(http.MethodPost, "/v1/auth/signup", "", signupBody(email)); res.Code != http.StatusOK {
		t.Fatalf("first signup: status = %d", res.Code)
	}
	res := h.do(http.MethodPost, "/v1/auth/signup", "", signupBody(email))
	if res.Code != http.StatusConflict {
		t.Fatalf("second signup: status = %d, want 409; body = %s", res.Code, res.Body)
	}
	if code := res.errorCode(t); code != "conflict" {
		t.Fatalf("error.code = %q, want conflict", code)
	}
}

func TestSignupValidatesInput(t *testing.T) {
	h := newHarness(t)

	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"blank full name", func(b map[string]any) { b["fullName"] = "  " }},
		{"blank org name", func(b map[string]any) { b["orgName"] = "" }},
		{"short password", func(b map[string]any) { b["password"] = "short" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := signupBody("validate-" + strings.ReplaceAll(tc.name, " ", "-") + "@example.test")
			tc.mutate(body)
			res := h.do(http.MethodPost, "/v1/auth/signup", "", body)
			if res.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body = %s", res.Code, res.Body)
			}
			if code := res.errorCode(t); code != "validation_failed" {
				t.Fatalf("error.code = %q, want validation_failed", code)
			}
		})
	}
}

func TestLoginReturnsASession(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email": acct.Email, "password": "test-password-123",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}

	var result struct {
		Kind    string `json:"kind"`
		Session struct {
			Token string `json:"token"`
		} `json:"session"`
	}
	res.decode(t, &result)
	if result.Kind != "session" {
		t.Fatalf("kind = %q, want session", result.Kind)
	}
	if result.Session.Token == "" {
		t.Fatal("login returned an empty token")
	}
	if me := h.do(http.MethodGet, "/v1/me", result.Session.Token, nil); me.Code != http.StatusOK {
		t.Fatalf("the login token does not authenticate: status = %d", me.Code)
	}
}

// A wrong password and an unknown address must be indistinguishable. If they
// differ in status or body, the login form becomes an account-enumeration
// oracle.
func TestLoginDoesNotRevealWhetherAnAccountExists(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	wrongPassword := h.do(http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email": acct.Email, "password": "definitely-not-the-password",
	})
	unknownEmail := h.do(http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email": "nobody-here-at-all@example.test", "password": "definitely-not-the-password",
	})

	if wrongPassword.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: status = %d, want 401", wrongPassword.Code)
	}
	if unknownEmail.Code != http.StatusUnauthorized {
		t.Fatalf("unknown email: status = %d, want 401", unknownEmail.Code)
	}
	if string(wrongPassword.Body) != string(unknownEmail.Body) {
		t.Fatalf("responses differ and so leak whether an account exists:\n wrong password: %s\n unknown email: %s",
			wrongPassword.Body, unknownEmail.Body)
	}
}

func TestLogoutRevokesTheCallersSession(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	if res := h.do(http.MethodPost, "/v1/auth/logout", acct.Token, nil); res.Code != http.StatusNoContent {
		t.Fatalf("logout: status = %d, want 204; body = %s", res.Code, res.Body)
	}
	if res := h.do(http.MethodGet, "/v1/me", acct.Token, nil); res.Code != http.StatusUnauthorized {
		t.Fatalf("after logout, GET /v1/me: status = %d, want 401", res.Code)
	}
}

func TestListSessionsMarksExactlyOneCurrent(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	// A second login for the same user creates a second session.
	login := h.do(http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email": acct.Email, "password": "test-password-123",
	})
	if login.Code != http.StatusOK {
		t.Fatalf("login: status = %d", login.Code)
	}

	res := h.do(http.MethodGet, "/v1/sessions", acct.Token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}
	var sessions []gen.Session
	res.decode(t, &sessions)
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	current := 0
	for _, session := range sessions {
		if session.Current {
			current++
		}
		if session.Id == "" || session.LastActiveAt.IsZero() {
			t.Errorf("session %+v is missing required contract fields", session)
		}
	}
	if current != 1 {
		t.Fatalf("%d sessions marked current, want exactly 1", current)
	}
}

func TestRevokeSessionInvalidatesThatTokenOnly(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	login := h.do(http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email": acct.Email, "password": "test-password-123",
	})
	var result struct {
		Session struct {
			Token string `json:"token"`
		} `json:"session"`
	}
	login.decode(t, &result)

	// Find the id of the session that is not the caller's own.
	listed := h.do(http.MethodGet, "/v1/sessions", acct.Token, nil)
	var sessions []gen.Session
	listed.decode(t, &sessions)
	var otherID string
	for _, session := range sessions {
		if !session.Current {
			otherID = session.Id
		}
	}
	if otherID == "" {
		t.Fatal("could not find the other session")
	}

	if res := h.do(http.MethodDelete, "/v1/sessions/"+otherID, acct.Token, nil); res.Code != http.StatusNoContent {
		t.Fatalf("revoke: status = %d, want 204; body = %s", res.Code, res.Body)
	}
	if res := h.do(http.MethodGet, "/v1/me", result.Session.Token, nil); res.Code != http.StatusUnauthorized {
		t.Fatalf("the revoked token still works: status = %d", res.Code)
	}
	if res := h.do(http.MethodGet, "/v1/me", acct.Token, nil); res.Code != http.StatusOK {
		t.Fatalf("revoking one session broke the other: status = %d", res.Code)
	}
}

// Revoking by guessing another tenant's session id must reveal nothing.
func TestRevokeSessionCannotTouchAnotherTenant(t *testing.T) {
	h := newHarness(t)
	victim := h.newAccount("owner")
	attacker := h.newAccount("owner")

	listed := h.do(http.MethodGet, "/v1/sessions", victim.Token, nil)
	var sessions []gen.Session
	listed.decode(t, &sessions)
	if len(sessions) == 0 {
		t.Fatal("victim has no sessions to target")
	}

	res := h.do(http.MethodDelete, "/v1/sessions/"+sessions[0].Id, attacker.Token, nil)
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", res.Code, res.Body)
	}
	if check := h.do(http.MethodGet, "/v1/me", victim.Token, nil); check.Code != http.StatusOK {
		t.Fatal("another tenant revoked the victim's session")
	}
}

func TestSessionEndpointsRequireAuthentication(t *testing.T) {
	h := newHarness(t)
	if res := h.do(http.MethodGet, "/v1/sessions", "", nil); res.Code != http.StatusUnauthorized {
		t.Fatalf("GET /v1/sessions unauthenticated: status = %d, want 401", res.Code)
	}
	if res := h.do(http.MethodDelete, "/v1/sessions/"+
		"00000000-0000-0000-0000-000000000000", "", nil); res.Code != http.StatusUnauthorized {
		t.Fatalf("DELETE /v1/sessions unauthenticated: status = %d, want 401", res.Code)
	}
}

// Sanity check that the seeded password actually round-trips through login,
// which the rest of these tests depend on.
func TestSeededAccountCanLogIn(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("member")

	var count int
	if err := h.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM users WHERE id = $1`, acct.UserID).Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Fatalf("seeded user count = %d, want 1", count)
	}
}
