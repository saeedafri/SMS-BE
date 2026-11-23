// Package failures turns a raw failure code into a plain explanation: who owned
// the cause, what it means, what to do, and whether retrying helps.
//
// The rules live in the failure_reasons table and are resolved at read time,
// so an operator's edit changes every past message it matches on the next read.
// This package is the resolution itself, with no I/O.
package failures

import "strings"

// Rule is one row of the dictionary.
type Rule struct {
	ID string
	// Channel is nil for a rule that applies on any channel (our own refusals).
	Channel         *string
	Source          string
	RouteID, Vendor *string
	MatchKind       string // "exact" or "prefix"
	MatchValue      string
	Layer           string
	Code            string
	Title           string
	Detail, Fix     *string
	FixKind         *string
	Retryable       bool
	AutoRetried     bool
	CustomerVisible bool
}

// Context is the question one failed or refused message asks.
type Context struct {
	Channel string
	Source  string
	// Raw is the code exactly as received; empty when the failure carried none.
	Raw     string
	RouteID string
	Vendor  string
}

// Failure is the answer. Layer is empty for a code no rule explains.
type Failure struct {
	Layer       string
	Code        string
	Title       string
	Detail, Fix *string
	FixKind     *string
	Retryable   bool
	AutoRetried bool
	// ProviderCode is the raw code kept for the reader; empty for our own
	// refusals, whose code is already the answer.
	ProviderCode string
	Source       string
}

// Unmapped reports whether no rule explained the failure.
func (f Failure) Unmapped() bool { return f.Layer == "" }

func (r Rule) matches(raw string) bool {
	if r.MatchKind == "prefix" {
		return strings.HasPrefix(raw, r.MatchValue)
	}
	return raw == r.MatchValue
}

func (r Rule) inScope(c Context) bool {
	return (r.Channel == nil || *r.Channel == c.Channel) && r.Source == c.Source
}

// Resolve answers a failure from the rules. First hit wins, in this order:
// exact on route and code; exact on vendor and code; exact on channel, source
// and code; prefix on the source family. With no hit the failure is unmapped,
// and the raw code is kept so it can be counted and given a rule.
func Resolve(rules []Rule, c Context) Failure {
	if c.Raw != "" {
		rungs := []func(Rule) bool{
			func(r Rule) bool {
				return r.RouteID != nil && *r.RouteID == c.RouteID && r.MatchKind == "exact"
			},
			func(r Rule) bool {
				return r.Vendor != nil && *r.Vendor == c.Vendor && r.MatchKind == "exact"
			},
			func(r Rule) bool {
				return r.RouteID == nil && r.Vendor == nil && r.MatchKind == "exact" && r.inScope(c)
			},
			func(r Rule) bool {
				return r.RouteID == nil && r.Vendor == nil && r.MatchKind == "prefix" && r.inScope(c)
			},
		}
		for _, rung := range rungs {
			for _, rule := range rules {
				if rung(rule) && rule.matches(c.Raw) {
					return answer(rule, c)
				}
			}
		}
	}
	fix, support := "Contact support and quote the code shown.", "support"
	return Failure{Code: "unmapped", Title: "We could not work out why this message failed",
		Fix: &fix, FixKind: &support, ProviderCode: c.Raw, Source: c.Source}
}

func answer(rule Rule, c Context) Failure {
	failure := Failure{Layer: rule.Layer, Code: rule.Code, Title: rule.Title,
		Detail: rule.Detail, Fix: rule.Fix, FixKind: rule.FixKind,
		Retryable: rule.Retryable, AutoRetried: rule.AutoRetried, Source: c.Source}
	if c.Source != "gate" {
		failure.ProviderCode = c.Raw
	}
	return failure
}

// ContextFor is the question a stored message asks, or false when it did not
// fail. status is the internal state; channel is the one that carried it.
func ContextFor(channel, status, errorCode, routeID, carrier string) (Context, bool) {
	c := Context{Channel: channel, Raw: errorCode, RouteID: routeID,
		Vendor: strings.ToLower(carrier)}
	switch status {
	case "rejected":
		c.Source = "gate"
		return c, true
	case "carrier_rejected", "undelivered", "expired":
	default:
		return Context{}, false
	}
	switch channel {
	case "SMS":
		c.Source = "smpp_receipt"
		if status == "carrier_rejected" {
			c.Source = "smpp_submit"
		}
	case "RCS":
		c.Source = "rcs_vendor"
	case "WHATSAPP":
		c.Source = "whatsapp"
	case "EMAIL":
		c.Source = "email"
	default:
		c.Source = "voice"
	}
	return c, true
}
