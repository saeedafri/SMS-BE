package api

import (
	"strings"
	"testing"
)

// The contract's enums are the authority; these lists must not drift from them.
//
// oapi-codegen gives us no type safety here — ChannelId("TELEPATHY") compiles —
// so the lists in enums.go are hand-copied, and a hand-copied list is exactly
// the kind of thing that goes stale silently. VOICE is the one that matters
// most: it was added to the product after the first four channels, and a list
// that had missed it would reject every legitimate voice sender with a 422 that
// looked like a client bug.
func TestEnumListsCoverTheValuesTheProductActuallyUses(t *testing.T) {
	mustContain := map[string][]string{
		"channels":     {"SMS", "RCS", "WHATSAPP", "EMAIL", "VOICE"},
		"currencies":   {"INR", "USD", "GBP", "AED"},
		"environments": {"live", "test"},
		"frequencies":  {"daily", "weekly", "monthly"},
		"cardBrands":   {"visa", "mastercard", "amex"},
		"roles":        {"owner", "admin", "member"},
	}
	lists := map[string][]string{
		"channels":     validChannels,
		"currencies":   validCurrencies,
		"environments": validEnvironments,
		"frequencies":  validFrequencies,
		"cardBrands":   validCardBrands,
		"roles":        validRoles,
	}
	for name, required := range mustContain {
		for _, value := range required {
			if !oneOf(value, lists[name]) {
				t.Errorf("%s is missing %q — a legitimate request would be refused", name, value)
			}
		}
		if len(lists[name]) != len(required) {
			t.Errorf("%s has %d entries, want %d — an extra value is a hole",
				name, len(lists[name]), len(required))
		}
	}
}

// The values that were actually sent at the deployment when this was found.
func TestOneOfRejectsWhatTheProbeSent(t *testing.T) {
	rejected := []struct {
		value   string
		allowed []string
	}{
		{"TELEPATHY", validChannels},   // was accepted, and PERSISTED, by POST /v1/sender-ids
		{"XXX", validCurrencies},       // 500 from PUT /v1/wallet/auto-recharge
		{"", validCurrencies},          // 500 too — an omitted field is the empty string
		{"staging", validEnvironments}, // 500 from three developer endpoints
		{"hourly", validFrequencies},   // 500 from POST /v1/analytics/reports
		{"notabrand", validCardBrands}, // 500 from POST /v1/wallet/payment-methods
		{"superuser", validRoles},
	}
	for _, tc := range rejected {
		if oneOf(tc.value, tc.allowed) {
			t.Errorf("oneOf(%q) accepted a value that reached the database", tc.value)
		}
	}
}

// Case-sensitive deliberately: the database CHECK constraints compare exactly,
// so accepting "sms" would write a row that reads fine in an API response and
// matches nothing in a WHERE clause.
func TestOneOfIsCaseSensitive(t *testing.T) {
	for _, value := range []string{"sms", "Sms", "LIVE", "Daily", "VISA"} {
		if oneOf(value, validChannels) || oneOf(value, validEnvironments) ||
			oneOf(value, validFrequencies) || oneOf(value, validCardBrands) {
			t.Errorf("oneOf accepted %q — wrong case writes a row nothing matches", value)
		}
	}
}

// The message has to name the options, or the caller is left guessing at a set
// that is public information in the contract they already hold.
func TestEnumMessageNamesTheAllowedValues(t *testing.T) {
	got := enumMessage("Channel", validChannels)
	for _, value := range validChannels {
		if !strings.Contains(got, value) {
			t.Fatalf("enumMessage did not mention %q: %s", value, got)
		}
	}
	if !strings.HasPrefix(got, "Channel must be one of") {
		t.Fatalf("enumMessage should name the field first: %s", got)
	}
}
