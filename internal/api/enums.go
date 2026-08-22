package api

import "strings"

// Contract enums, enforced at the edge.
//
// oapi-codegen renders an OpenAPI enum as a plain Go string alias, not a closed
// type — `ChannelId("TELEPATHY")` compiles and the generated server does not
// reject it. So every handler that passed request.Body.<Enum> straight through
// was trusting a value nothing had checked, and the first thing that noticed was
// Postgres, one layer too late. Probing the deployment with one bad value per
// enum found both halves of that:
//
//   - Seven endpoints answered 500 — the CHECK constraint held, so no bad data
//     was written, but the caller got "an unexpected error occurred" for what is
//     an ordinary validation mistake, and the error reached the logs as an
//     unhandled operation.
//   - POST /v1/sender-ids answered 201 and PERSISTED channel "TELEPATHY". That
//     column has no CHECK constraint behind it, so nothing caught it at all.
//
// The second is why this lives here rather than being fixed with more
// constraints: a constraint protects the table it is on, and only the tables
// that happen to have one. Rejecting the value at the boundary protects every
// table it would have reached, and returns the 422 the contract already
// documents.
//
// Values below are copied from the generated contract types, which are the
// authority. Where the database also constrains a column the two agree.
var (
	validChannels     = []string{"SMS", "RCS", "WHATSAPP", "EMAIL", "VOICE"}
	validCurrencies   = []string{"INR", "USD", "GBP", "AED"}
	validEnvironments = []string{"live", "test"}
	validFrequencies  = []string{"daily", "weekly", "monthly"}
	validCardBrands   = []string{"visa", "mastercard", "amex"}
	validRoles        = []string{"owner", "admin", "member"}
	validCountries    = []string{"IN", "US", "GB", "AE"}
	validStandings    = []string{"registered", "grey"}
	validCarriers     = []string{"JIO", "AIRTEL", "VI", "BSNL", "VERIZON", "ATT",
		"TMOBILE", "EE", "O2", "VODAFONE_UK", "THREE", "ETISALAT", "DU"}
)

// oneOf reports whether value is one of allowed, comparing exactly.
//
// Case-sensitive on purpose. The contract fixes the case of every one of these
// ("SMS", "live"), the database CHECK constraints compare exactly, and quietly
// accepting "sms" here would write a value that reads correctly in an API
// response but matches nothing in a WHERE clause.
func oneOf(value string, allowed []string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

// enumMessage builds the 422 body for a rejected enum, naming what was allowed.
//
// The list is included because the alternative — "invalid channel" — makes the
// caller guess, and the allowed set is public information already: it is in the
// contract they generated their client from.
func enumMessage(field string, allowed []string) string {
	return field + " must be one of: " + strings.Join(allowed, ", ") + "."
}
