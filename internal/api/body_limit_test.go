package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A body over the ceiling is refused with 413 before a handler reads it; the
// import route, which carries every row in its body, keeps a larger ceiling.
func TestOversizedBodiesAreRefusedWith413(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acct := h.newAccount("owner")

	send := func(path string, size int, chunked bool) response {
		body := `{"x":"` + strings.Repeat("a", size) + `"}`
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if chunked {
			req.ContentLength = -1
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+acct.Token)
		rec := httptest.NewRecorder()
		h.router.ServeHTTP(rec, req)
		return response{Code: rec.Code, Body: rec.Body.Bytes(), Header: rec.Header()}
	}

	if res := send("/v1/messages", 2<<20, false); res.Code != http.StatusRequestEntityTooLarge ||
		res.errorCode(t) != "payload_too_large" {
		t.Errorf("2 MB declared = %d %s, want 413 payload_too_large", res.Code, res.Body)
	}
	if res := send("/v1/messages", 2<<20, true); res.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("2 MB chunked = %d %s, want 413", res.Code, res.Body)
	}
	if res := send("/v1/messages", 100<<10, false); res.Code == http.StatusRequestEntityTooLarge {
		t.Errorf("100 KB refused as too large")
	}
	// Import rows live in the body, so 5 MB is an ordinary import, not an attack.
	if res := send("/v1/contacts/import", 5<<20, false); res.Code == http.StatusRequestEntityTooLarge {
		t.Errorf("5 MB import refused as too large")
	}
}
