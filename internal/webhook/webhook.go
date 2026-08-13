// Package webhook signs and delivers outbound event payloads.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// client has a short timeout because a customer's slow endpoint must not hold
// our worker open. Redirects are refused: following one would send a signed
// payload somewhere the customer never registered.
var client = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// Result is what one delivery attempt produced.
type Result struct {
	Outcome         string
	HTTPStatus      *int
	ResponseSnippet *string
	Payload         []byte
}

// Sign computes the signature a receiver verifies.
//
// The timestamp is inside the signed string, not merely alongside it. Signing
// the body alone lets an attacker who captures one request replay it forever;
// binding the timestamp means a receiver can reject anything older than its
// tolerance window and the signature still covers that decision.
func Sign(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Deliver posts a signed payload and reports what happened.
func Deliver(ctx context.Context, endpoint, eventType string, payload []byte, secret string) Result {
	timestamp := time.Now().Unix()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		bytes.NewReader(payload))
	if err != nil {
		return failed(payload, fmt.Sprintf("could not build request: %v", err))
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Relay-Event", eventType)
	request.Header.Set("X-Relay-Timestamp", strconv.FormatInt(timestamp, 10))
	request.Header.Set("X-Relay-Signature", "v1="+Sign(secret, timestamp, payload))

	response, err := client.Do(request)
	if err != nil {
		// A network failure is recorded as a failed attempt rather than an
		// error, because it is the customer's endpoint that failed and they
		// need to see that in the delivery log.
		return failed(payload, truncate(err.Error()))
	}
	defer response.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	snippet := truncate(string(body))
	status := response.StatusCode
	outcome := "failed"
	// Any 2xx is success. A 3xx is not: we refuse redirects, so a redirect
	// means the payload was never accepted where it was registered.
	if status >= 200 && status < 300 {
		outcome = "succeeded"
	}
	return Result{Outcome: outcome, HTTPStatus: &status,
		ResponseSnippet: &snippet, Payload: payload}
}

func failed(payload []byte, detail string) Result {
	return Result{Outcome: "failed", ResponseSnippet: &detail, Payload: payload}
}

func truncate(value string) string {
	const limit = 300
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

// SamplePayload is the body of a test event. It mirrors a real
// message.delivered payload so a customer's parser is exercised properly
// rather than against a shape that never occurs in production.
func SamplePayload() []byte {
	return []byte(`{"event":"message.delivered","data":{` +
		`"messageId":"00000000-0000-0000-0000-000000000000",` +
		`"status":"delivered","msisdn":"+919876543210","segments":1,` +
		`"test":true}}`)
}

// ValidateURL refuses endpoints we must not sign payloads for.
//
// Plain http leaks message content and lets anyone on the path forge our
// events. Private and loopback addresses are refused because an endpoint
// pointing at our own network turns the webhook sender into an SSRF primitive
// — a tenant could otherwise use it to probe internal services from inside our
// perimeter.
func ValidateURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("That URL could not be parsed.")
	}
	if parsed.Scheme != "https" {
		return errors.New("Webhook endpoints must use https.")
	}
	host := parsed.Hostname()
	if host == "" {
		return errors.New("That URL has no host.")
	}
	if ip := net.ParseIP(host); ip != nil && isInternal(ip) {
		return errors.New("That address is not routable on the public internet.")
	}
	return nil
}

func isInternal(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsUnspecified() || ip.IsMulticast()
}

// ValidateCIDR rejects a malformed allowlist entry. Storing one would silently
// match nothing and lock a tenant out of their own API with no indication why.
func ValidateCIDR(value string) error {
	if _, _, err := net.ParseCIDR(value); err != nil {
		// A bare address is a reasonable thing to type, so it is accepted and
		// treated as a single-host range rather than rejected pedantically.
		if net.ParseIP(value) == nil {
			return errors.New("That is not a valid IP address or CIDR range.")
		}
	}
	return nil
}
