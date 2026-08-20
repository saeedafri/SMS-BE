package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The parser's entire correctness argument is ORDER: every Chromium browser
// still claims "Chrome" in its User-Agent, and Chrome still claims "Safari", so
// a naive set of independent checks reports every browser on this list as
// Safari. Each row below is a real header that breaks a different shortcut.
func TestParseUserAgentPicksTheMostSpecificBrowser(t *testing.T) {
	cases := []struct {
		name            string
		ua              string
		device, browser string
	}{
		{
			name:    "chrome on macos is not safari",
			ua:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36",
			device:  "macOS · Chrome",
			browser: "Chrome 141",
		},
		{
			name:    "edge is not chrome",
			ua:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.3537.57",
			device:  "Windows · Edge",
			browser: "Edge 141",
		},
		{
			name:    "opera is not chrome",
			ua:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 OPR/125.0.0.0",
			device:  "Windows · Opera",
			browser: "Opera 125",
		},
		{
			name:    "real safari has a Version token and nothing more specific",
			ua:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Safari/605.1.15",
			device:  "macOS · Safari",
			browser: "Safari 17",
		},
		{
			name:    "safari on iphone is Mobile Safari",
			ua:      "Mozilla/5.0 (iPhone; CPU iPhone OS 17_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Mobile/15E148 Safari/604.1",
			device:  "iOS · Mobile Safari",
			browser: "Mobile Safari 17",
		},
		{
			// iPadOS 13+ reports itself as a Macintosh. Testing "Macintosh"
			// before the touch hint reads every iPad as a desktop Mac.
			name:    "ipad reports Macintosh but is still iOS",
			ua:      "Mozilla/5.0 (iPad; CPU OS 17_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Mobile/15E148 Safari/604.1",
			device:  "iOS · Mobile Safari",
			browser: "Mobile Safari 17",
		},
		{
			// Chrome on iOS is CriOS and carries no "Chrome/" token at all.
			name:    "chrome on ios",
			ua:      "Mozilla/5.0 (iPhone; CPU iPhone OS 17_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/141.0.0.0 Mobile/15E148 Safari/604.1",
			device:  "iOS · Chrome",
			browser: "Chrome 141",
		},
		{
			name:    "firefox on linux",
			ua:      "Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0",
			device:  "Linux · Firefox",
			browser: "Firefox 131",
		},
		{
			name:    "android chrome",
			ua:      "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36",
			device:  "Android · Chrome",
			browser: "Chrome 141",
		},
		{
			// The report that prompted this fix was reproduced with curl, so
			// naming the tool is worth more than a third "Unknown".
			name:    "curl is named, not Unknown",
			ua:      "curl/8.7.1",
			device:  "API client",
			browser: "curl",
		},
		{
			name:    "no header at all",
			ua:      "",
			device:  "Unknown",
			browser: "Unknown",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseUserAgent(tc.ua)
			if got.Device != tc.device || got.Browser != tc.browser {
				t.Fatalf("parseUserAgent(%q)\n got device=%q browser=%q\nwant device=%q browser=%q",
					tc.ua, got.Device, got.Browser, tc.device, tc.browser)
			}
		})
	}
}

// The address must come from RemoteAddr, which chi's RealIP has already
// rewritten from a proxy header where there was one. Reading the header here as
// well would let a client on a deployment with no proxy forge its own address.
func TestClientIPStripsThePortAndIgnoresForgedHeaders(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "203.0.113.9:54321"
	request.Header.Set("X-Forwarded-For", "198.51.100.7")

	if got := clientIP(request); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want 203.0.113.9 (the header must not win here)", got)
	}

	// RealIP leaves a bare address behind when it rewrites from a header.
	request.RemoteAddr = "198.51.100.7"
	if got := clientIP(request); got != "198.51.100.7" {
		t.Fatalf("clientIP with no port = %q, want 198.51.100.7", got)
	}
}

// A handler that runs without the middleware must still produce the two
// "Unknown" strings the frontend renders, not two empty cells.
func TestClientInfoFromIsUnknownRatherThanEmpty(t *testing.T) {
	got := clientInfoFrom(t.Context())
	if got.Device != "Unknown" || got.Browser != "Unknown" {
		t.Fatalf("clientInfoFrom(empty ctx) = %+v, want both Unknown", got)
	}
}

// The middleware and the funnel that reads it, joined up: this is the assertion
// that would have caught the original bug, where issueSession passed literal
// "Unknown" strings and no request detail ever reached the database.
func TestWithClientInfoReachesTheHandler(t *testing.T) {
	var seen clientInfo
	handler := withClientInfo(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = clientInfoFrom(r.Context())
	}))

	request := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
	request.RemoteAddr = "203.0.113.9:443"
	request.Header.Set("User-Agent",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if seen.Device != "macOS · Chrome" || seen.Browser != "Chrome 141" || seen.IP != "203.0.113.9" {
		t.Fatalf("handler saw %+v, want macOS · Chrome / Chrome 141 / 203.0.113.9", seen)
	}
}
