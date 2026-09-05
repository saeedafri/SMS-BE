package api

import (
	"strings"
	"testing"

	"github.com/saeedafri/sms-be/internal/domain/messaging"
	gen "github.com/saeedafri/sms-be/internal/gen/api"
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

// Every state a message can be in must map to a status the contract can spell.
//
// This lived in the messaging package against a hand-copied list of the enum,
// which is how it went stale: `rejected` was added to MessageStatus and the
// list still said the six values it had the day it was written. Here the
// generated Valid() is the authority, so the check cannot drift from the
// contract — it fails the moment a state maps to something the frontend would
// not recognise.
func TestEveryMessageStateMapsToAContractStatus(t *testing.T) {
	for _, state := range []messaging.State{
		messaging.StateQueued, messaging.StateSubmitting, messaging.StateSubmitted,
		messaging.StateAccepted, messaging.StateDelivered, messaging.StateUndelivered,
		messaging.StateRejected, messaging.StateExpired,
	} {
		got := gen.MessageStatus(messaging.ContractStatus(state))
		if !got.Valid() {
			t.Errorf("%s maps to %q, which is not a MessageStatus the contract declares",
				state, got)
		}
	}
	// The distinction the log gained. A refusal and a failed delivery lead to
	// different fixes, and reporting both as "failed" collapses them.
	if got := messaging.ContractStatus(messaging.StateRejected); got != "rejected" {
		t.Errorf("rejected maps to %q, want rejected — the log can spell a refusal now", got)
	}
	if got := messaging.ContractStatus(messaging.StateUndelivered); got != "failed" {
		t.Errorf("undelivered maps to %q, want failed", got)
	}
}

// The scope catalogue and the route table are two hand-written lists that the
// contract now closes with an enum. Both directions matter: a catalogue entry
// outside the enum is a scope we publish that the frontend cannot render, and
// a route demanding a scope outside the catalogue is policy no key could ever
// satisfy — a route quietly unreachable by any API key at all.
func TestKeyScopesAgreeWithTheContractEnum(t *testing.T) {
	for _, scope := range apiScopeCatalogue {
		if !scope.Key.Valid() {
			t.Errorf("catalogue publishes %q, which is not an ApiKeyScope", scope.Key)
		}
	}
	for route, scope := range keyRoutes {
		if scope == scopeCheckedByHandler {
			continue
		}
		if !gen.ApiKeyScope(scope).Valid() {
			t.Errorf("%s requires %q, which is not an ApiKeyScope", route, scope)
		}
		if _, ok := knownScopes([]gen.ApiKeyScope{gen.ApiKeyScope(scope)}); !ok {
			t.Errorf("%s requires %q, which the catalogue does not publish — no key can hold it",
				route, scope)
		}
	}
}
