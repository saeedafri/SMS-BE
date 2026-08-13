package audience_test

import (
	"testing"

	"github.com/saeedafri/sms-be/internal/domain/audience"
)

// These cases mirror ../SMS-UI/src/lib/contacts/phone.ts. The user approves a
// CSV preview normalised by that code; if ours disagrees, a row shown as valid
// is rejected on submit, or stored under a different msisdn than was previewed
// — which breaks deduplication silently.
func TestNormaliseMsisdn(t *testing.T) {
	cases := []struct {
		raw, country, want string
		ok                 bool
	}{
		{"9876543210", "IN", "+919876543210", true},
		{"09876543210", "IN", "+919876543210", true},  // trunk zero
		{"919876543210", "IN", "+919876543210", true}, // already international
		{"+91 98765 43210", "IN", "+919876543210", true},
		// The UI's normalizeMsisdn rejects a 00 trunk prefix on the
		// country-scoped path (it only strips a single leading 0), so ours does
		// too. Matching it exactly matters more than being individually
		// permissive: a divergence means the preview and the stored value differ.
		{"0091-9876543210", "IN", "", false},
		{"4155550100", "US", "+14155550100", true},
		{"14155550100", "US", "+14155550100", true},
		{"501234567", "AE", "+971501234567", true},
		{"7911123456", "GB", "+447911123456", true},

		{"12345", "IN", "", false},          // too short
		{"98765432101234", "IN", "", false}, // too long
		{"", "IN", "", false},
		{"not a number", "IN", "", false},
		{"9876543210", "ZZ", "", false}, // unknown country
	}
	for _, tc := range cases {
		got, ok := audience.NormaliseMsisdn(tc.raw, tc.country)
		if ok != tc.ok || got != tc.want {
			t.Errorf("NormaliseMsisdn(%q, %q) = (%q, %v), want (%q, %v)",
				tc.raw, tc.country, got, ok, tc.want, tc.ok)
		}
	}
}

// The suppression list is global: someone who opts out of an Indian campaign
// must stay suppressed when a US sender targets them, so it cannot use the
// country-scoped rules.
func TestNormaliseE164IsCountryAgnostic(t *testing.T) {
	cases := []struct {
		raw, want string
		ok        bool
	}{
		{"+919876543210", "+919876543210", true},
		{"00919876543210", "+919876543210", true},
		{"+1 415 555 0100", "+14155550100", true},
		{"1234567", "", false},          // below the 8-digit floor
		{"1234567890123456", "", false}, // above E.164's 15
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := audience.NormaliseE164(tc.raw)
		if ok != tc.ok || got != tc.want {
			t.Errorf("NormaliseE164(%q) = (%q, %v), want (%q, %v)", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

// Case-insensitivity is a suppression correctness issue: an opt-out recorded
// as Alice@example.com must still match a later import spelling it lowercase.
func TestNormaliseEmailLowercases(t *testing.T) {
	got, ok := audience.NormaliseEmail("  Alice@Example.COM ")
	if !ok || got != "alice@example.com" {
		t.Fatalf("NormaliseEmail = (%q, %v), want (alice@example.com, true)", got, ok)
	}
	for _, bad := range []string{"", "no-at-sign", "a@b", "a@@b.c", "a b@c.d"} {
		if _, ok := audience.NormaliseEmail(bad); ok {
			t.Errorf("NormaliseEmail(%q) was accepted", bad)
		}
	}
}
