package api

import (
	"context"
	"net"
	"net/http"
	"strings"
)

type clientKey struct{}

// clientInfo is what the security screen shows about the device a session was
// created on: GET /v1/sessions renders one row per session, and every row read
// "Unknown · Unknown" with a blank IP because issueSession hardcoded those
// strings rather than looking at the request.
//
// It is read from the request by the middleware below rather than passed down
// as arguments, because the strict-server handlers take a context and not an
// *http.Request — the same reason the caller's identity travels this way.
type clientInfo struct {
	Device  string
	Browser string
	IP      string
}

// withClientInfo records who is calling, for whichever handler ends up minting
// a session. Parsing here rather than in issueSession keeps the one place that
// touches an *http.Request in the middleware layer, where the rest of the
// request-scoped values already live.
func withClientInfo(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := parseUserAgent(r.Header.Get("User-Agent"))
		info.IP = clientIP(r)
		ctx := context.WithValue(r.Context(), clientKey{}, info)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// clientInfoFrom returns what withClientInfo recorded.
//
// The zero case returns the two "Unknown" strings rather than empty ones so a
// session minted on a path the middleware does not cover still reads the way
// the frontend expects, instead of rendering two blank cells.
func clientInfoFrom(ctx context.Context) clientInfo {
	info, ok := ctx.Value(clientKey{}).(clientInfo)
	if !ok {
		return clientInfo{Device: unknownClient, Browser: unknownClient}
	}
	return info
}

const (
	unknownClient = "Unknown"
	maxUserAgent  = 512
)

// clientIP is the caller's address without its port.
//
// chi's middleware.RealIP already runs ahead of this and has rewritten
// RemoteAddr from X-Forwarded-For where a proxy set one, so this does not need
// to read those headers itself — and must not, or a client could forge its own
// address by sending the header directly on a deployment with no proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// No port to split: RealIP leaves a bare address behind when it
		// rewrites from a header.
		host = strings.TrimSpace(r.RemoteAddr)
	}
	// Validated rather than stored as given. RealIP copies X-Forwarded-For into
	// RemoteAddr without checking it is an address at all, so without this an
	// arbitrary string from a header ends up rendered on the security screen.
	if net.ParseIP(host) == nil {
		return ""
	}
	return host
}

// parseUserAgent picks an operating system and a browser out of a User-Agent
// header.
//
// Hand-rolled rather than pulling in a UA-parsing dependency: this feeds one
// cosmetic column on one screen, so the cost of a library that ships a
// regularly-regenerated regex database — and needs updating as browsers ship —
// is not worth it. The failure mode is a row reading "Unknown", which is
// exactly what the screen shows today.
//
// Order matters throughout. Every Chromium browser still claims "Chrome", and
// Chrome itself still claims "Safari", so the specific tokens have to be tested
// before the generic ones or every browser on the list reads as Safari.
func parseUserAgent(ua string) clientInfo {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return clientInfo{Device: unknownClient, Browser: unknownClient}
	}
	// A header is caller-controlled and this ends up in a database column and
	// on a screen, so it is truncated before anything else looks at it. Real
	// agents are well under this; the longest in the test table is 137 bytes.
	if len(ua) > maxUserAgent {
		ua = ua[:maxUserAgent]
	}

	os := parseOS(ua)
	name, version := parseBrowser(ua)

	if name == "" {
		// Not a browser. curl, a Go client and the backend's own probes all
		// land here, and naming the tool is far more useful on the security
		// screen than a third "Unknown" — an API token in use from somewhere
		// unexpected is precisely what someone reads this screen to find.
		tool := ua
		if cut, _, found := strings.Cut(ua, " "); found {
			tool = cut
		}
		if slash := strings.IndexByte(tool, '/'); slash > 0 {
			tool = tool[:slash]
		}
		if tool == "" {
			return clientInfo{Device: unknownClient, Browser: unknownClient}
		}
		device := "API client"
		if os != unknownClient {
			device = os + " · API client"
		}
		return clientInfo{Device: device, Browser: tool}
	}

	// "Mobile Safari" is the name the seeded fixture rows use, and the one
	// people recognise for a phone browser.
	if name == "Safari" && strings.Contains(ua, "Mobile") {
		name = "Mobile Safari"
	}

	browser := name
	if version != "" {
		browser = name + " " + version
	}
	return clientInfo{Device: os + " · " + name, Browser: browser}
}

func parseOS(ua string) string {
	switch {
	// iPadOS 13+ reports itself as a Macintosh, so the touch hint has to be
	// tested before the Mac one or every iPad reads as a desktop.
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"),
		strings.Contains(ua, "iPod"):
		return "iOS"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "Windows NT"):
		return "Windows"
	case strings.Contains(ua, "Macintosh"), strings.Contains(ua, "Mac OS X"):
		return "macOS"
	case strings.Contains(ua, "CrOS"):
		return "ChromeOS"
	case strings.Contains(ua, "Linux"):
		return "Linux"
	default:
		return unknownClient
	}
}

// browserTokens is ordered from most specific to least. Edge and Opera both
// carry "Chrome", and Chrome carries "Safari", so the first match wins and the
// order below is the whole correctness argument.
var browserTokens = []struct{ token, name string }{
	{"Edg/", "Edge"},
	{"EdgiOS/", "Edge"},
	{"OPR/", "Opera"},
	{"SamsungBrowser/", "Samsung Internet"},
	{"Firefox/", "Firefox"},
	{"FxiOS/", "Firefox"},
	{"CriOS/", "Chrome"},
	{"Chrome/", "Chrome"},
	{"Version/", "Safari"},
}

func parseBrowser(ua string) (name, version string) {
	for _, candidate := range browserTokens {
		// Safari is the one entry that cannot be identified by its own token:
		// "Safari/" appears in every Chromium UA too. Real Safari is the case
		// where a "Version/" number is present AND nothing more specific
		// matched, which is why it sits last.
		if candidate.name == "Safari" && !strings.Contains(ua, "Safari/") {
			continue
		}
		index := strings.Index(ua, candidate.token)
		if index < 0 {
			continue
		}
		return candidate.name, majorVersion(ua[index+len(candidate.token):])
	}
	return "", ""
}

// majorVersion takes the leading digits of a version string. Only the major
// number: "Chrome 141" is what a person reading the screen wants, and the full
// "141.0.7390.55" makes the column unreadable and changes every fortnight.
func majorVersion(rest string) string {
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	return rest[:end]
}
