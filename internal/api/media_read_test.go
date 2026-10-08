package api_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func png224() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 224, 224))
	for x := 0; x < 224; x++ {
		for y := 0; y < 224; y++ {
			img.Set(x, y, color.RGBA{20, 80, 200, 255})
		}
	}
	var out bytes.Buffer
	_ = png.Encode(&out, img)
	return out.Bytes()
}

// A carrier fetching brand artwork has no Relay login, and production does not
// give the API process the migration-role pool, so the read has to work without
// it. It used to dereference that pool and answered 500 on every fetch, which
// nothing noticed because no test ever fetched a URL.
func TestASignedMediaUrlServesTheFileWithoutASessionOrAnAdminPool(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	res := h.uploadAttempt(acct, "agent_logo", "logo.png", "image/png", png224())
	if res.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", res.Code, res.Body)
	}
	var asset struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(res.Body, &asset)
	parsed, err := url.Parse(asset.URL)
	if err != nil || !strings.Contains(parsed.Path, "/v1/media/") {
		t.Fatalf("url = %q", asset.URL)
	}

	h.server.AdminDB = nil // the shape of production
	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec
	}
	rec := get(parsed.RequestURI())
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), png224()) {
		t.Fatalf("fetch = %d %s (%d bytes)", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	if got := get(strings.Replace(parsed.RequestURI(), "signature=", "signature=00", 1)); got.Code != 403 {
		t.Errorf("a forged signature = %d, want 403", got.Code)
	}
	if got := get("/v1/media/00000000-0000-0000-0000-000000000000/x.png?expires=9999999999&signature=aa"); got.Code != 404 {
		t.Errorf("an unknown asset = %d, want 404", got.Code)
	}
}
