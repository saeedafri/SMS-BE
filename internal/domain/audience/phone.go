// Package audience holds contact-identity rules: turning what a user typed
// into a canonical form we can store, dedupe and suppress against.
package audience

import (
	"regexp"
	"strings"
)

// numberPlan is the dial code and national-number length for a country.
//
// This mirrors ../SMS-UI/src/lib/contacts/phone.ts, which normalises the CSV
// preview the user approves before importing. If the two disagree, a row the
// preview showed as valid gets rejected on submit — or worse, stored under a
// different msisdn than the preview displayed, which breaks deduplication
// silently.
//
// Both are deliberately simple (dial code plus length) rather than full number
// plans. That is a known limitation, not an oversight: a wrong-but-consistent
// normalisation is recoverable, whereas two different normalisations are not.
type numberPlan struct {
	dial string
	nsn  int
}

var numberPlans = map[string]numberPlan{
	"IN": {dial: "91", nsn: 10},
	"US": {dial: "1", nsn: 10},
	"GB": {dial: "44", nsn: 10},
	"AE": {dial: "971", nsn: 9},
}

var nonDigits = regexp.MustCompile(`\D`)

// NormaliseMsisdn converts a raw phone string to E.164 for a known country, or
// returns false if it cannot be made valid.
func NormaliseMsisdn(raw, country string) (string, bool) {
	plan, known := numberPlans[strings.ToUpper(strings.TrimSpace(country))]
	if !known {
		return "", false
	}
	digits := nonDigits.ReplaceAllString(raw, "")
	if digits == "" {
		return "", false
	}

	// Already international, with or without a leading + or 00.
	if strings.HasPrefix(digits, plan.dial) && len(digits) == len(plan.dial)+plan.nsn {
		return "+" + digits, true
	}
	// National form carrying a trunk zero, e.g. India's "098…".
	if len(digits) == plan.nsn+1 && strings.HasPrefix(digits, "0") {
		digits = digits[1:]
	}
	if len(digits) == plan.nsn {
		return "+" + plan.dial + digits, true
	}
	return "", false
}

// NormaliseE164 validates an already-international number without knowing the
// country. The suppression list is global — someone who opts out in India must
// stay suppressed for a campaign sent from a US sender — so it cannot use the
// country-scoped rules above.
func NormaliseE164(raw string) (string, bool) {
	digits := nonDigits.ReplaceAllString(raw, "")
	digits = strings.TrimPrefix(digits, "00")
	if len(digits) < 8 || len(digits) > 15 {
		return "", false
	}
	return "+" + digits, true
}

var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s.]+(\.[^@\s.]+)+$`)

// NormaliseEmail lowercases and trims an address. Case-insensitivity matters
// for suppression: someone who opts out as Alice@example.com must stay
// suppressed when a later import spells it alice@example.com.
func NormaliseEmail(raw string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if !emailPattern.MatchString(value) {
		return "", false
	}
	return value, true
}
