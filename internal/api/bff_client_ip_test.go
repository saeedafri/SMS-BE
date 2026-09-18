package api_test

import (
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"
)

const bffToken = "test-bff-token-0123456789abcdef0123456789abcdef"

// Ask 42. The dashboard is a BFF: every customer's sign-in reaches the API from
// the dashboard server's address. Only a request that proves it is the BFF may
// name the user's own address.

func bffHarness(t *testing.T) *harness {
	t.Helper()
	h := newSendHarness(t)
	if h.server.Redis == nil {
		t.Skip("REDIS_URL not set")
	}
	h.server.BFFClientIPToken = bffToken
	return h
}

// failFromBFF sends a wrong password for an unknown address through one BFF
// socket, so only the per-IP rule can be what blocks.
func (h *harness) failFromBFF(socket, clientIP, token string) response {
	headers := map[string]string{"X-Relay-Client-IP": clientIP}
	if token != "" {
		headers["X-Relay-BFF-Token"] = token
	}
	return h.doFrom(socket+":443", http.MethodPost, "/v1/auth/login", "",
		map[string]string{"email": fmt.Sprintf("nobody-%d@relay.test", rand.Int()), "password": "wrong"},
		headers)
}

func (h *harness) loginFromBFF(socket, clientIP, token, email string) response {
	return h.doFrom(socket+":443", http.MethodPost, "/v1/auth/login", "",
		map[string]string{"email": email, "password": "test-password-123"},
		map[string]string{"X-Relay-BFF-Token": token, "X-Relay-Client-IP": clientIP})
}

// Test 1: many users behind one BFF are not one IP.
func TestManyUsersBehindTheDashboardAreNotOneAddress(t *testing.T) {
	t.Parallel()
	h := bffHarness(t)
	socket := randomIP()
	for i := 0; i < 20; i++ {
		h.failFromBFF(socket, randomIP(), bffToken)
	}
	account := h.newAccount("owner")
	if res := h.loginFromBFF(socket, randomIP(), bffToken, account.Email); res.Code != http.StatusOK {
		t.Fatalf("a customer signing in after 20 other users' mistakes = %d %s, want 200 — "+
			"the dashboard's own address was blocked for everyone", res.Code, res.Body)
	}
	if strings.Contains(h.logs.String(), bffToken) {
		t.Error("the BFF token appears in the server's logs")
	}
}

// Test 2: one user behind the BFF is still one IP.
func TestOneUserBehindTheDashboardIsStillLimited(t *testing.T) {
	t.Parallel()
	h := bffHarness(t)
	client := randomIP()
	for i := 0; i < 20; i++ {
		h.failFromBFF(randomIP(), client, bffToken)
	}
	if res := h.failFromBFF(randomIP(), client, bffToken); res.errorCode(t) != "too_many_attempts" {
		t.Fatalf("21st failure from one user through the BFF = %s, want too_many_attempts", res.Body)
	}
}

// Tests 3 and 4: without the right token the header is ignored.
func TestTheClientAddressHeaderNeedsTheBFFToken(t *testing.T) {
	t.Parallel()
	for name, token := range map[string]string{"no token": "", "wrong token": "not-the-token"} {
		t.Run(name, func(t *testing.T) {
			h := bffHarness(t)
			socket := randomIP()
			for i := 0; i < 20; i++ {
				h.failFromBFF(socket, randomIP(), token)
			}
			if res := h.failFromBFF(socket, randomIP(), token); res.errorCode(t) != "too_many_attempts" {
				t.Fatalf("21st failure from one socket with %s = %s, want too_many_attempts — "+
					"an unproven X-Relay-Client-IP must not choose the address", name, res.Body)
			}
		})
	}
}

// Test 5: a malformed address is ignored and the socket address counts.
func TestAMalformedClientAddressFallsBackToTheSocket(t *testing.T) {
	t.Parallel()
	h := bffHarness(t)
	socket := randomIP()
	for i := 0; i < 20; i++ {
		h.failFromBFF(socket, "not-an-ip", bffToken)
	}
	if res := h.failFromBFF(socket, "also-not-an-ip", bffToken); res.errorCode(t) != "too_many_attempts" {
		t.Fatalf("21st failure with a malformed client address = %s, want too_many_attempts", res.Body)
	}
}

// The feature is off when the server has no token, whatever the request sends.
func TestNoServerTokenMeansNoBFFTrust(t *testing.T) {
	t.Parallel()
	h := bffHarness(t)
	h.server.BFFClientIPToken = ""
	socket := randomIP()
	for i := 0; i < 20; i++ {
		h.failFromBFF(socket, randomIP(), "")
	}
	if res := h.failFromBFF(socket, randomIP(), ""); res.errorCode(t) != "too_many_attempts" {
		t.Fatalf("with no server token = %s, want too_many_attempts", res.Body)
	}
}
