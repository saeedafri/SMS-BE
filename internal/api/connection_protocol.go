package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"

	"github.com/linxGnu/gosmpp/pdu"

	"github.com/saeedafri/sms-be/internal/connector"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

var (
	protocolKeys = []string{"dltEntityTlv", "dltTemplateTlv", "dltChainTlv", "sourceTon",
		"sourceNpi", "destTon", "destNpi", "registeredDelivery", "dltTelemarketerChain"}
	validNpis        = []int{0, 1, 3, 4, 6, 8, 9, 10, 14, 18}
	telemarketerID   = regexp.MustCompile(`^[0-9]{19}$`)
	errProtocolShape = "protocol must be an object or null."
)

// mergeProtocol applies a request's protocol to the stored overrides, as the
// contract's merge patch: absent leaves them, null resets them all, and inside
// an object a value sets a key and null resets it. It returns the new overrides,
// or the field a refusal names and why. Nothing is written by this: the whole
// merged result is validated before anything is stored.
func mergeProtocol(stored []byte, raw json.RawMessage, present bool, platformChain []string) (
	[]byte, string, string) {

	overrides := map[string]json.RawMessage{}
	if len(stored) > 0 {
		if err := json.Unmarshal(stored, &overrides); err != nil {
			return nil, "protocol", "The stored protocol could not be read."
		}
	}
	if present {
		switch {
		case bytes.Equal(bytes.TrimSpace(raw), []byte("null")):
			overrides = map[string]json.RawMessage{}
		default:
			var patch map[string]json.RawMessage
			if err := json.Unmarshal(raw, &patch); err != nil {
				return nil, "protocol", errProtocolShape
			}
			for key, value := range patch {
				if !slices.Contains(protocolKeys, key) {
					return nil, key, fmt.Sprintf("protocol.%s is not a protocol value.", key)
				}
				if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					delete(overrides, key)
					continue
				}
				overrides[key] = value
			}
		}
	}
	if field, message := validateProtocol(overrides, platformChain); field != "" {
		return nil, field, message
	}
	encoded, err := json.Marshal(overrides)
	if err != nil {
		return nil, "protocol", err.Error()
	}
	return encoded, "", ""
}

// validateProtocol checks the effective values an override set produces. The
// tags are compared after merging with the defaults, so resetting one onto a
// tag another override already uses is a collision, not a reset.
func validateProtocol(overrides map[string]json.RawMessage, platformChain []string) (string, string) {
	numbers := map[string]int{}
	for _, key := range protocolKeys[:8] {
		value, set := overrides[key]
		if !set {
			continue
		}
		var n int
		if err := json.Unmarshal(value, &n); err != nil {
			return key, fmt.Sprintf("protocol.%s must be an integer.", key)
		}
		numbers[key] = n
	}
	effective := effectiveProtocol(overrides, platformChain)
	for key, n := range numbers {
		switch key {
		case "dltEntityTlv", "dltTemplateTlv", "dltChainTlv":
			if n < 5120 || n > 16383 {
				return key, fmt.Sprintf("protocol.%s must be between 5120 and 16383.", key)
			}
		case "sourceTon", "destTon":
			if n < 0 || n > 6 {
				return key, fmt.Sprintf("protocol.%s must be between 0 and 6.", key)
			}
		case "sourceNpi", "destNpi":
			if !slices.Contains(validNpis, n) {
				return key, fmt.Sprintf("protocol.%s must be one of 0, 1, 3, 4, 6, 8, 9, 10, 14, 18.", key)
			}
		case "registeredDelivery":
			if n < 0 || n > 31 {
				return key, "protocol.registeredDelivery must be between 0 and 31."
			}
		}
	}
	tags := map[string]int{"dltEntityTlv": effective.DltEntityTlv,
		"dltTemplateTlv": effective.DltTemplateTlv, "dltChainTlv": effective.DltChainTlv}
	for _, key := range []string{"dltEntityTlv", "dltTemplateTlv", "dltChainTlv"} {
		for _, other := range []string{"dltEntityTlv", "dltTemplateTlv", "dltChainTlv"} {
			if key != other && tags[key] == tags[other] {
				// Name the field this request is responsible for when it is one.
				field := key
				if _, set := overrides[key]; !set {
					field = other
				}
				if _, set := overrides[field]; !set {
					field = key
				}
				return field, fmt.Sprintf("protocol.%s collides with %s: both would be %d.",
					key, other, tags[key])
			}
		}
	}
	if value, set := overrides["dltTelemarketerChain"]; set {
		var chain []string
		if err := json.Unmarshal(value, &chain); err != nil {
			return "dltTelemarketerChain", "protocol.dltTelemarketerChain must be a list of ids."
		}
		switch {
		case len(chain) < 1 || len(chain) > 5:
			return "dltTelemarketerChain", "protocol.dltTelemarketerChain must have 1 to 5 entries."
		case len(platformChain) == 0:
			return "dltTelemarketerChain", "This deployment has no DLT_TM_CHAIN, so no chain can end in its telemarketer id."
		case chain[len(chain)-1] != platformChain[len(platformChain)-1]:
			return "dltTelemarketerChain", fmt.Sprintf(
				"protocol.dltTelemarketerChain must end with the platform's telemarketer id %s.",
				platformChain[len(platformChain)-1])
		}
		for _, id := range chain {
			if !telemarketerID.MatchString(id) {
				return "dltTelemarketerChain", "protocol.dltTelemarketerChain entries are 19-digit ids."
			}
		}
	}
	return "", ""
}

// effectiveProtocol is the connection's overrides over the platform defaults:
// every value, never null.
func effectiveProtocol(overrides map[string]json.RawMessage, platformChain []string) gen.SmppProtocol {
	defaults := connector.DefaultSMPPProtocol(platformChain)
	value := func(key string, fallback int) int {
		var n int
		if raw, set := overrides[key]; set && json.Unmarshal(raw, &n) == nil {
			return n
		}
		return fallback
	}
	chain := append([]string{}, platformChain...)
	if raw, set := overrides["dltTelemarketerChain"]; set {
		var override []string
		if json.Unmarshal(raw, &override) == nil {
			chain = override
		}
	}
	return gen.SmppProtocol{
		DltEntityTlv:         value("dltEntityTlv", int(defaults.EntityTag)),
		DltTemplateTlv:       value("dltTemplateTlv", int(defaults.TemplateTag)),
		DltChainTlv:          value("dltChainTlv", int(defaults.ChainTag)),
		SourceTon:            value("sourceTon", int(defaults.SourceTon)),
		SourceNpi:            gen.SmppNpi(value("sourceNpi", int(defaults.SourceNpi))),
		DestTon:              value("destTon", int(defaults.DestTon)),
		DestNpi:              gen.SmppNpi(value("destNpi", int(defaults.DestNpi))),
		RegisteredDelivery:   value("registeredDelivery", int(defaults.RegisteredDelivery)),
		DltTelemarketerChain: chain,
	}
}

// storedProtocol reads a connection's stored overrides.
func storedProtocol(stored []byte) map[string]json.RawMessage {
	overrides := map[string]json.RawMessage{}
	_ = json.Unmarshal(stored, &overrides)
	return overrides
}

// wireProtocol is what a bind puts on the wire for these values.
func wireProtocol(p gen.SmppProtocol) connector.SMPPProtocol {
	protocol := connector.DefaultSMPPProtocol(p.DltTelemarketerChain)
	protocol.EntityTag, protocol.TemplateTag, protocol.ChainTag =
		pdu.Tag(p.DltEntityTlv), pdu.Tag(p.DltTemplateTlv), pdu.Tag(p.DltChainTlv)
	protocol.SourceTon, protocol.SourceNpi = byte(p.SourceTon), byte(p.SourceNpi)
	protocol.DestTon, protocol.DestNpi = byte(p.DestTon), byte(p.DestNpi)
	protocol.RegisteredDelivery = byte(p.RegisteredDelivery)
	return protocol
}
