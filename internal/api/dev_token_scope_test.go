package api

import "testing"

// The fixed dev tokens must reach fixture addresses and nothing else.
//
// Gating them on ENABLE_DEV_ENDPOINTS alone was a live, unauthenticated account
// takeover on the public API, reproduced end to end against the deployment:
//
//	POST /v1/auth/password/forgot {"email": <any real address>}   -> 204
//	POST /v1/auth/password/reset  {"token": "dev-reset-token", …} -> 204
//	POST /v1/auth/login           {"email": …, "password": …}     -> 200
//
// /v1/auth/password/reset is an ordinary public endpoint, so the nginx rule
// that denies the /v1/dev/* prefix never covered it. No inbox access was
// required at any point.
//
// The table below is deliberately weighted towards the addresses that must NOT
// qualify, because that is the direction the bug ran in — a false positive here
// hands over a real person's account, while a false negative only breaks a test.
func TestOnlyFixtureAddressesGetTheFixedDevTokens(t *testing.T) {
	cases := []struct {
		email   string
		fixture bool
		why     string
	}{
		// Every address the browser suite uses. These must keep working, or
		// the fix has simply broken the gate instead.
		{"founder@acme.test", true, "the main fixture account"},
		{"ada@newco.test", true, "signup spec"},
		{"ops@relay.internal", true, "operator console"},
		{"help@support.acmert.example", true, "email sender spec"},
		{"new.person@acme.test", true, "team roster spec"},

		// Real addresses. Every one of these was takeable before the fix.
		{"agentantigravity33@gmail.com", false, "a real inbox"},
		{"founder@acme.com", false, ".com is registrable — near-miss on the fixture domain"},
		{"someone@saqibsaeed.cloud", false, "the deployment's own domain"},
		{"admin@example.com.attacker.io", false, "reserved TLD in the middle, not at the end"},
		{"user@test.evil.com", false, "reserved label as a subdomain, not the TLD"},
		{"a@b.testing", false, "longer TLD that merely starts with the reserved one"},

		// Malformed input must never be treated as a fixture.
		{"no-at-sign", false, "not an address at all"},
		{"", false, "empty"},
		{"trailing@", false, "empty domain"},
	}

	for _, tc := range cases {
		t.Run(tc.email, func(t *testing.T) {
			if got := isFixtureAddress(tc.email); got != tc.fixture {
				t.Fatalf("isFixtureAddress(%q) = %v, want %v — %s",
					tc.email, got, tc.fixture, tc.why)
			}
		})
	}
}

// Case must not decide whether an account can be taken over.
func TestFixtureAddressIgnoresCase(t *testing.T) {
	for _, email := range []string{"Founder@ACME.TEST", "OPS@Relay.Internal"} {
		if !isFixtureAddress(email) {
			t.Fatalf("isFixtureAddress(%q) = false, want true", email)
		}
	}
	if isFixtureAddress("Victim@GMAIL.COM") {
		t.Fatal("isFixtureAddress said a real address was a fixture")
	}
}
