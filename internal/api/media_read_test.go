package api_test

import (
	"bytes"
	"time"

	"encoding/json"
	"github.com/google/uuid"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
		ID string `json:"id"`
	}
	_ = json.Unmarshal(res.Body, &asset)
	// Artwork is returned at its plain public address; the signed form is still
	// served, and is what a link issued before that change looks like.
	parsed, err := url.Parse(h.server.Media.SignedURL(acct.TenantID, uuid.MustParse(asset.ID), "logo.png", time.Hour))
	if err != nil || !strings.Contains(parsed.Path, "/v1/media/") {
		t.Fatalf("signed url = %v", parsed)
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

// A carrier reviews a bot or template for days, long after a 24 hour link has
// lapsed, and the artwork URL is stored by the carrier exactly as it was sent.
// Brand artwork is not secret and has to keep loading; the signature is still
// required. A verification document, which IS sensitive, stays expiring.
func TestBrandArtworkUrlsOutliveTheirExpiryButDocumentsDoNot(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	assetID := func(purpose, name, ctype string, body []byte) string {
		res := h.uploadAttempt(acct, purpose, name, ctype, body)
		if res.Code != http.StatusCreated {
			t.Fatalf("upload %s = %d %s", purpose, res.Code, res.Body)
		}
		var a struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(res.Body, &a)
		return a.ID
	}
	logo := assetID("agent_logo", "logo.png", "image/png", png224())
	doc := assetID("verification_document", "loa.pdf", "application/pdf", []byte("%PDF-1.4\nletter\n"))

	h.server.AdminDB = nil
	expired := func(id, name string) string {
		raw := h.server.Media.SignedURL(acct.TenantID, uuid.MustParse(id), name, -48*time.Hour)
		parsed, _ := url.Parse(raw)
		return parsed.RequestURI()
	}
	get := func(target string) int {
		rec := httptest.NewRecorder()
		h.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec.Code
	}
	if code := get(expired(logo, "logo.png")); code != 200 {
		t.Errorf("an expired artwork link = %d, want 200", code)
	}
	if code := get(strings.Replace(expired(logo, "logo.png"), "signature=", "signature=00", 1)); code != 403 {
		t.Errorf("a forged artwork signature = %d, want 403", code)
	}
	if code := get(expired(doc, "loa.pdf")); code != 403 {
		t.Errorf("an expired verification document = %d, want 403", code)
	}
}

// What a validator sees when it is handed an artwork URL: a plain https path
// that ends in the image extension, no query string, answering GET and HEAD
// with the right type and length. Trustsignal rejected our signed-and-expiring
// URL with "logo url should be a valid image" while accepting a plain
// .../<uuid>.png from its own storage.
func TestArtworkGetsAPlainPermanentUrlThatAnswersGetAndHead(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	h.server.AdminDB = nil

	upload := func(purpose, name, ctype string, body []byte) (id, rawURL string) {
		res := h.uploadAttempt(acct, purpose, name, ctype, body)
		if res.Code != http.StatusCreated {
			t.Fatalf("upload %s = %d %s", purpose, res.Code, res.Body)
		}
		var a struct{ ID, URL string }
		_ = json.Unmarshal(res.Body, &a)
		return a.ID, a.URL
	}
	id, rawURL := upload("agent_logo", "My Logo.PNG", "image/png", png224())
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.RawQuery != "" || parsed.Path != "/brand/"+id+".png" {
		t.Fatalf("artwork url = %q, want a plain /brand/<id>.png with no query string", rawURL)
	}
	do := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.router.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}
	get := do(http.MethodGet, parsed.Path)
	if get.Code != 200 || get.Header().Get("Content-Type") != "image/png" || !bytes.Equal(get.Body.Bytes(), png224()) {
		t.Fatalf("GET = %d %s", get.Code, get.Header().Get("Content-Type"))
	}
	if get.Header().Get("Content-Length") != strconv.Itoa(len(png224())) ||
		!strings.Contains(get.Header().Get("Cache-Control"), "public") {
		t.Errorf("headers = %v", get.Header())
	}
	head := do(http.MethodHead, parsed.Path)
	if head.Code != 200 || head.Header().Get("Content-Type") != "image/png" || head.Body.Len() != 0 ||
		head.Header().Get("Content-Length") != strconv.Itoa(len(png224())) {
		t.Errorf("HEAD = %d %v (%d body bytes)", head.Code, head.Header(), head.Body.Len())
	}
	// Anything that is not exactly that asset with its own extension is a 404,
	// the same one, so the path cannot be used to find out what exists.
	for name, path := range map[string]string{
		"wrong extension": "/brand/" + id + ".jpg",
		"no extension":    "/brand/" + id,
		"unknown id":      "/brand/00000000-0000-0000-0000-000000000000.png",
		"junk":            "/brand/not-a-uuid.png",
	} {
		if rec := do(http.MethodGet, path); rec.Code != 404 {
			t.Errorf("%s = %d, want 404", name, rec.Code)
		}
	}
	// A verification document is never public.
	docID, docURL := upload("verification_document", "loa.pdf", "application/pdf", []byte("%PDF-1.4\nletter\n"))
	if !strings.Contains(docURL, "signature=") {
		t.Errorf("a verification document got a public url: %q", docURL)
	}
	for _, ext := range []string{".pdf", ".png"} {
		if rec := do(http.MethodGet, "/brand/"+docID+ext); rec.Code != 404 {
			t.Errorf("document at /brand/%s = %d, want 404", ext, rec.Code)
		}
	}
	// The case only the purpose check can stop: a document that happens to be a
	// PNG has the right extension and a servable type.
	pngDoc, pngDocURL := upload("verification_document", "scan.png", "image/png", png224())
	if !strings.Contains(pngDocURL, "signature=") {
		t.Errorf("a PNG verification document got a public url: %q", pngDocURL)
	}
	if rec := do(http.MethodGet, "/brand/"+pngDoc+".png"); rec.Code != 404 {
		t.Errorf("a PNG verification document at /brand = %d, want 404", rec.Code)
	}
	// The signed form already issued still works.
	signed, _ := url.Parse(h.server.Media.SignedURL(acct.TenantID, uuid.MustParse(id), "logo.png", time.Hour))
	if rec := do(http.MethodGet, signed.RequestURI()); rec.Code != 200 {
		t.Errorf("an issued signed url stopped working: %d", rec.Code)
	}
}
