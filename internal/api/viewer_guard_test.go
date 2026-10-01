package api_test

import (
	"net/http"
	"testing"
)

// A viewer reads what its tenant has and cannot change any of it, on routes the
// guard was never told about.
func TestAViewerCanReadButNotWrite(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	viewer := h.newAccount("viewer")

	for _, path := range []string{"/v1/campaigns", "/v1/me", "/v1/frequency-caps",
		"/v1/links", "/v1/wallet/balances", "/v1/templates"} {
		if res := h.do(http.MethodGet, path, viewer.Token, nil); res.Code != http.StatusOK {
			t.Errorf("viewer GET %s = %d %s, want 200", path, res.Code, res.Body)
		}
	}
	for _, write := range []struct{ method, path string }{
		{http.MethodPost, "/v1/messages"},
		{http.MethodPost, "/v1/campaigns"},
		{http.MethodPost, "/v1/links"},
		{http.MethodPut, "/v1/frequency-caps/SMS"},
		{http.MethodPost, "/v1/contacts/import"},
		{http.MethodPost, "/v1/team/invite"},
		{http.MethodPost, "/v1/wallet/topup"},
		{http.MethodPost, "/v1/developer/api-keys"},
		{http.MethodDelete, "/v1/suppressions/%2B919810000999"},
		{http.MethodPatch, "/v1/data-retention"},
	} {
		res := h.do(write.method, write.path, viewer.Token, map[string]any{})
		if res.Code != http.StatusForbidden || res.errorCode(t) != "forbidden" {
			t.Errorf("viewer %s %s = %d %s, want 403", write.method, write.path, res.Code, res.Body)
		}
	}
	// Its own sign-in is its own business.
	if res := h.do(http.MethodPost, "/v1/auth/logout", viewer.Token, nil); res.Code == http.StatusForbidden {
		t.Errorf("a viewer was refused logging out: %s", res.Body)
	}
}

func TestAnOwnerCanInviteAViewerAndOthersStillWrite(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	owner := h.newAccount("owner")
	res := h.do(http.MethodPost, "/v1/team/invite", owner.Token,
		map[string]any{"email": "viewer-invite@example.test", "role": "viewer"})
	if res.Code != http.StatusCreated {
		t.Fatalf("invite viewer = %d %s", res.Code, res.Body)
	}
	if res := h.do(http.MethodPost, "/v1/team/invite", owner.Token,
		map[string]any{"email": "x@example.test", "role": "superuser"}); res.Code != 422 {
		t.Errorf("unknown role = %d, want 422", res.Code)
	}
	member := h.newAccount("member")
	if res := h.do(http.MethodPost, "/v1/links", member.Token,
		map[string]any{"destination": "https://shop.example.org/a"}); res.Code != http.StatusCreated {
		t.Errorf("a member lost the ability to write: %d %s", res.Code, res.Body)
	}
}
