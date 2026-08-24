package connector

import "strings"

// Registry picks the carrier for a channel.
//
// Relay ran on one connector for every channel until RCS arrived, and that was
// right while the only carrier was the sandbox. It stops being right the moment
// one channel has a real gateway and the others do not: an SMS handed to
// Airtel's RCS endpoint is not a degraded send, it is a 400 for every message.
//
// Keyed by channel rather than by country because that is the axis the carriers
// actually differ on today. Country matters too — an Indian RCS agent cannot
// send to a UK handset — but that is the routes table's job, and duplicating it
// here would give two places to disagree about the same corridor.
type Registry struct {
	// Default takes everything with no specific carrier, which is the sandbox
	// on any deployment without commercial agreements.
	Default Connector

	// ByChannel is keyed by the channel name in upper case, as the senders
	// table stores it.
	ByChannel map[string]Connector
}

// For returns the carrier for a channel, falling back to the default.
//
// Never returns nil for a non-empty registry: a nil connector reaching the send
// path is a panic in the middle of a batch that has already had money held
// against it.
func (r Registry) For(channel string) Connector {
	if carrier, ok := r.ByChannel[strings.ToUpper(channel)]; ok && carrier != nil {
		return carrier
	}
	return r.Default
}

// RCSTemplateRegistrarFor returns the carrier's template registry, if it has
// one. The second return is false when RCS has no carrier configured at all —
// distinct from a carrier that HAS a template API and refused, which surfaces
// as ErrTemplateRegistrationManual instead.
func (r Registry) RCSTemplateRegistrarFor(channel string) (RCSTemplateRegistrar, bool) {
	registrar, ok := r.For(channel).(RCSTemplateRegistrar)
	return registrar, ok
}

// Dedicated reports the carrier configured specifically for a channel, and
// whether there is one.
//
// Distinct from For, which falls back to Default: the caller that records WHICH
// carrier carried a message needs to know the difference. A channel served by
// the default sandbox took whatever path the routes table chose; a channel with
// its own gateway went there regardless of what the routes table says.
func (r Registry) Dedicated(channel string) (Connector, bool) {
	carrier, ok := r.ByChannel[strings.ToUpper(channel)]
	return carrier, ok && carrier != nil
}
