// Package compliance holds the per-country regulatory regimes. Compliance is a
// data dimension behind a common interface, never branches in shared code: a
// new country is a new file in this package plus a registry entry, and no
// handler changes. That is the PRD's adaptability law, and it is the property
// most likely to erode under time pressure — so the interface deliberately
// gives handlers no way to ask "which country is this?".
package compliance

import (
	"fmt"
	"net/url"
	"strings"
)

type Tier string

const (
	TierEntity   Tier = "entity"
	TierSender   Tier = "sender"
	TierTemplate Tier = "template"
)

type FieldType string

const (
	FieldText   FieldType = "text"
	FieldEmail  FieldType = "email"
	FieldURL    FieldType = "url"
	FieldSelect FieldType = "select"
)

type Option struct {
	Value string
	Label string
}

type FieldSpec struct {
	Key      string
	Label    string
	Type     FieldType
	Required bool
	Options  []Option
}

// RegistrationObject is one thing a tenant registers with a regulator — a DLT
// principal entity, a TCR brand, and so on.
type RegistrationObject struct {
	Key   string
	Label string
	Tier  Tier
	// DependsOn names a sibling object that must be approved first. The US
	// campaign cannot be filed before its brand exists.
	DependsOn   string
	Remediation string
	Fields      []FieldSpec
}

// ValidationResult carries a reason so the UI can show the user something
// actionable rather than a generic rejection.
type ValidationResult struct {
	OK     bool
	Reason string
}

func valid() ValidationResult                { return ValidationResult{OK: true} }
func invalid(reason string) ValidationResult { return ValidationResult{Reason: reason} }

type Regime interface {
	Country() string
	Label() string
	Currency() string
	// Stub reports a regime that proves the pattern but carries no real
	// registration objects yet. It is deliberately distinct from "no such
	// regime": the two produce different, correct errors.
	Stub() bool
	RegistrationObjects() []RegistrationObject
	Object(key string) (RegistrationObject, bool)
	ValidateCtaURL(rawURL string) ValidationResult
	// ValidateHeader checks a sender header against the country's rule for one.
	//
	// India fixes the shape exactly — six alphanumerics, which is what DLT
	// issues — and that rule was written down in the regime's own remediation
	// text while nothing enforced it, so "a b!@#$%^&*()_+1234567890" was
	// accepted as a DLT header and sat pending review looking legitimate.
	ValidateHeader(header string) ValidationResult
	// RequiresRegistrationID reports whether this country's regulator issues an
	// identifier the customer must carry on records at that tier — India's DLT
	// principal-entity, header and content-template ids.
	//
	// It lives here rather than as `country == "IN"` inside the sender and
	// template handlers for the reason at the top of this file: a new country is
	// a file in this package, not a branch somewhere else. Two handlers each
	// carrying their own copy of the rule is exactly how they drift.
	//
	// Note what this does NOT say: nothing anywhere may GENERATE such an id.
	// The regulator issues it to the customer; we only ever store what they
	// hand us.
	RequiresRegistrationID(tier Tier) bool
	// RequiresRegisteredTemplate reports whether a submit to this country must
	// carry a content template the regulator has registered.
	//
	// India's operators do not judge whether a message looks reasonable. They
	// match its content against a template registered on DLT, by id, and drop
	// anything that does not match — all of it, not a throttled share. So a
	// send with no template, or with text that is not an instantiation of the
	// one it names, cannot be delivered on a real Indian route no matter what
	// we do with it afterwards.
	//
	// Accepting, charging for and reporting such a message as sent is a worse
	// failure than refusing it, because it is invisible until a customer
	// complains that nothing arrived.
	//
	// A property of the regime, not a country branch on the submit path: adding
	// the UAE later is an entry in this package.
	RequiresRegisteredTemplate() bool
}

var registry = map[string]Regime{
	"IN": india{},
	"US": unitedStates{},
	"GB": stub{country: "GB", label: "United Kingdom (MMA)", currency: "GBP"},
	"AE": stub{country: "AE", label: "United Arab Emirates", currency: "AED"},
}

// For resolves a country's regime. The boolean distinguishes "we do not
// operate there" from a stub regime that exists but registers nothing.
func For(country string) (Regime, bool) {
	regime, ok := registry[strings.ToUpper(strings.TrimSpace(country))]
	return regime, ok
}

// Countries lists every regime we support, for validation messages.
func Countries() []string {
	return []string{"IN", "US", "GB", "AE"}
}

// MissingRequired returns the keys of required fields absent from a
// submission, in the object's own field order so the UI can highlight the
// first one. Whitespace is not a value.
func MissingRequired(object RegistrationObject, fields map[string]any) []string {
	var missing []string
	for _, field := range object.Fields {
		if !field.Required {
			continue
		}
		value, present := fields[field.Key]
		if !present || strings.TrimSpace(fmt.Sprintf("%v", value)) == "" {
			missing = append(missing, field.Key)
		}
	}
	return missing
}

// requireAbsoluteURL is the floor every regime shares: a CTA has to be a real
// absolute http(s) URL before any country-specific rule is worth applying.
func requireAbsoluteURL(rawURL string) (*url.URL, ValidationResult) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, invalid("Enter a full, valid URL (https://…).")
	}
	return parsed, valid()
}
