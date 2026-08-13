package api_test

import (
	"context"
	"net/http"
	"testing"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func TestGetMeReturnsEveryContractField(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodGet, "/v1/me", acct.Token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}

	var me gen.Me
	res.decode(t, &me)

	if me.UserId != acct.UserID {
		t.Errorf("userId = %s, want %s", me.UserId, acct.UserID)
	}
	if me.TenantId != acct.TenantID {
		t.Errorf("tenantId = %s, want %s", me.TenantId, acct.TenantID)
	}
	if string(me.Email) != acct.Email {
		t.Errorf("email = %s, want %s", me.Email, acct.Email)
	}
	if me.Name != "Test User" {
		t.Errorf("name = %q, want %q", me.Name, "Test User")
	}
	if me.Role != gen.TeamRole("owner") {
		t.Errorf("role = %q, want owner", me.Role)
	}
	if me.Country != gen.CountryCode("IN") {
		t.Errorf("country = %q, want IN", me.Country)
	}
	if !me.EmailVerified {
		t.Error("emailVerified = false, want true")
	}
	if me.MfaEnabled {
		t.Error("mfaEnabled = true, want false for a fresh account")
	}
	if me.TenantName == "" {
		t.Error("tenantName is empty; the contract requires it")
	}
	// The frontend's capability gating reads this; an empty set would silently
	// hide every feature rather than fail loudly.
	if len(me.Capabilities) == 0 {
		t.Error("capabilities is empty; a new tenant should have the default set")
	}
}

func TestGetMeRejectsBadCredentials(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	cases := []struct{ name, token string }{
		{"no token", ""},
		{"unknown token", "not-a-real-token"},
		{"token with wrong case prefix handled", "x" + acct.Token},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := h.do(http.MethodGet, "/v1/me", tc.token, nil)
			if res.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body = %s", res.Code, res.Body)
			}
			if code := res.errorCode(t); code != "unauthenticated" {
				t.Fatalf("error.code = %q, want unauthenticated", code)
			}
		})
	}
}

// A revoked session must stop working immediately. This is the property that
// justified opaque tokens over JWTs, so it gets a test of its own.
func TestRevokedSessionIsRejectedImmediately(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	if res := h.do(http.MethodGet, "/v1/me", acct.Token, nil); res.Code != http.StatusOK {
		t.Fatalf("precondition failed: status = %d, want 200", res.Code)
	}
	if _, err := h.admin.Exec(context.Background(),
		`UPDATE sessions SET revoked_at = now() WHERE tenant_id = $1`, acct.TenantID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	res := h.do(http.MethodGet, "/v1/me", acct.Token, nil)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d after revocation, want 401", res.Code)
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	if _, err := h.admin.Exec(context.Background(),
		`UPDATE sessions SET expires_at = now() - interval '1 minute' WHERE tenant_id = $1`,
		acct.TenantID); err != nil {
		t.Fatalf("expire: %v", err)
	}

	if res := h.do(http.MethodGet, "/v1/me", acct.Token, nil); res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d for an expired session, want 401", res.Code)
	}
}

func TestUpdateMeChangesTheName(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPatch, "/v1/me", acct.Token, map[string]string{"name": "Renamed User"})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}
	var me gen.Me
	res.decode(t, &me)
	if me.Name != "Renamed User" {
		t.Fatalf("name = %q, want %q", me.Name, "Renamed User")
	}

	// It must be persisted, not just echoed back.
	after := h.do(http.MethodGet, "/v1/me", acct.Token, nil)
	after.decode(t, &me)
	if me.Name != "Renamed User" {
		t.Fatalf("after re-reading, name = %q, want %q", me.Name, "Renamed User")
	}
}

// The frontend's middleware gates whole areas on this role, and it fails
// closed on a non-2xx. So a member's 403 has to be genuine, not cosmetic.
func TestUpdateMeForbiddenForMemberRole(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("member")

	res := h.do(http.MethodPatch, "/v1/me", acct.Token, map[string]string{"name": "Nope"})
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", res.Code, res.Body)
	}
	if code := res.errorCode(t); code != "forbidden" {
		t.Fatalf("error.code = %q, want forbidden", code)
	}
}

func TestUpdateMeRejectsBlankName(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	for _, name := range []string{"", "   "} {
		res := h.do(http.MethodPatch, "/v1/me", acct.Token, map[string]string{"name": name})
		if res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("name %q: status = %d, want 422; body = %s", name, res.Code, res.Body)
		}
		if code := res.errorCode(t); code != "validation_failed" {
			t.Fatalf("name %q: error.code = %q, want validation_failed", name, code)
		}
	}
}

func TestUpdateTenantRenamesTheOrganisation(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("admin")

	res := h.do(http.MethodPatch, "/v1/tenant", acct.Token, map[string]string{"name": "Acme Messaging"})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}
	var me gen.Me
	res.decode(t, &me)
	if me.TenantName != "Acme Messaging" {
		t.Fatalf("tenantName = %q, want %q", me.TenantName, "Acme Messaging")
	}

	after := h.do(http.MethodGet, "/v1/me", acct.Token, nil)
	after.decode(t, &me)
	if me.TenantName != "Acme Messaging" {
		t.Fatalf("after re-reading, tenantName = %q, want %q", me.TenantName, "Acme Messaging")
	}
}

func TestUpdateTenantForbiddenForMemberRole(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("member")

	res := h.do(http.MethodPatch, "/v1/tenant", acct.Token, map[string]string{"name": "Nope"})
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.Code)
	}
}

// One tenant renaming itself must not touch another's row. RLS should make
// this impossible, and this test is what proves the handler actually runs
// scoped rather than reaching around it.
func TestUpdateTenantCannotAffectAnotherTenant(t *testing.T) {
	h := newHarness(t)
	first := h.newAccount("owner")
	second := h.newAccount("owner")

	if res := h.do(http.MethodPatch, "/v1/tenant", first.Token,
		map[string]string{"name": "First Org Renamed"}); res.Code != http.StatusOK {
		t.Fatalf("rename: status = %d", res.Code)
	}

	res := h.do(http.MethodGet, "/v1/me", second.Token, nil)
	var me gen.Me
	res.decode(t, &me)
	if me.TenantName == "First Org Renamed" {
		t.Fatal("renaming one tenant changed another tenant's name")
	}
}
