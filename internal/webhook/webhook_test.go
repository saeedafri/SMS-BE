package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// ValidateURL sees only a hostname, so a name that resolves to a private
// address passes it. The connection itself must then be refused, or the
// webhook sender reaches the metadata service or our own network.
func TestAHostnameThatResolvesInternallyIsNeverDialled(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()
	address, _ := url.Parse(server.URL)
	byName := "http://localhost:" + address.Port() + "/hook"

	if err := ValidateURL(strings.Replace(byName, "http:", "https:", 1)); err != nil {
		t.Fatalf("ValidateURL refused a hostname, so this test proves nothing: %v", err)
	}
	result := Deliver(context.Background(), byName, "message.delivered", []byte(`{}`), "secret")
	if result.Outcome != "failed" || hits.Load() != 0 {
		t.Fatalf("outcome %q with %d request(s) reaching a loopback server, want failed and 0",
			result.Outcome, hits.Load())
	}
	if result.ResponseSnippet == nil || !strings.Contains(*result.ResponseSnippet, "not a public address") {
		t.Fatalf("failure is not the address check: %v", result.ResponseSnippet)
	}
}

func TestOnlyPublicAddressesMayBeDialled(t *testing.T) {
	for address, refused := range map[string]bool{
		"169.254.169.254:80":   true, // instance metadata
		"10.0.0.5:443":         true,
		"172.16.0.1:443":       true,
		"192.168.1.1:443":      true,
		"127.0.0.1:443":        true,
		"100.64.0.1:443":       true,
		"0.0.0.0:443":          true,
		"[::1]:443":            true,
		"[fd00:ec2::254]:80":   true, // IPv6 instance metadata
		"[::ffff:10.0.0.1]:80": true,
		"93.184.215.14:443":    false,
		"[2606:4700::1]:443":   false,
	} {
		err := refuseInternal("tcp", address, nil)
		if (err != nil) != refused {
			t.Errorf("%s: refused = %v, want %v", address, err != nil, refused)
		}
	}
}
