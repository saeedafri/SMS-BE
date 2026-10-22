package sending

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/audience"
	"github.com/saeedafri/sms-be/internal/domain/compliance"
	"github.com/saeedafri/sms-be/internal/domain/messaging"
	"github.com/saeedafri/sms-be/internal/store"
)

// The {{link}} variable: one tracked short link per recipient per campaign,
// made as their message is built. BACKEND_REQUEST_link-variable.md, L3 to L6.

// LinkCodeLength is how long every code is, so a finished link has a constant
// length and a message holding one an exact segment count.
const LinkCodeLength = 8

// CheckLinkDestination refuses what a link must never point at, in words a
// customer can act on. One function for POST /v1/links, a campaign's link and
// the sender's re-check at send time, so one rule has one sentence.
//
// selfHosts are the hosts that are Relay itself: a link to them is a redirect
// loop a recipient cannot leave.
func CheckLinkDestination(raw, country string, selfHosts ...string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return "destination must be a URL of up to 2048 characters."
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Hostname() == "" || parsed.User != nil {
		return "destination must be an absolute http or https URL without credentials."
	}
	for _, host := range selfHosts {
		if host != "" && strings.EqualFold(parsed.Host, host) {
			return "destination cannot point at Relay itself."
		}
	}
	if regime, known := compliance.For(country); known {
		if result := regime.ValidateCtaURL(raw); !result.OK {
			return result.Reason
		}
	}
	return ""
}

// LinkHost is the host of a base URL, empty when there is none.
func LinkHost(base string) string {
	parsed, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// UsesLink says whether any string of a template holds {{link}}.
func UsesLink(template store.Template) bool {
	for _, slot := range templateSlots(template, templateText(template)) {
		if slot == audience.LinkSlot {
			return true
		}
	}
	return false
}

// linkStandIn is a finished link's exact length with no code in it yet. The
// estimate counts with it, and the fan-out builds and gates a message with it,
// so the segments and the price are the real ones before any link exists.
func linkStandIn(base string) string {
	return base + "/l/" + strings.Repeat("x", LinkCodeLength)
}

// withLinkFilled substitutes link for every {{link}} in a text and leaves
// every other slot alone. Empty link changes nothing.
func withLinkFilled(text, link string) string {
	if link == "" {
		return text
	}
	var out strings.Builder
	for _, segment := range audience.SplitBody(text) {
		switch {
		case !segment.IsSlot:
			out.WriteString(segment.Text)
		case segment.Text == audience.LinkSlot:
			out.WriteString(link)
		default:
			out.WriteString("{{" + segment.Text + "}}")
		}
	}
	return out.String()
}

// linkKey is who a link belongs to: the number when the contact has one, else
// the address. The same person on either leg, so the fallback finds the link the
// primary made.
func linkKey(msisdn string, contact store.Contact) string {
	if msisdn != "" {
		return msisdn
	}
	email, _ := audience.NormaliseEmail(contactEmail(contact))
	return email
}

// sendLinks finds or makes the link each of these people is sent, and returns
// its URL by person. Someone missing from the answer gets no link, and their
// message is refused as variable_unresolved.
//
// A link the customer pre-made is used whatever happens to the campaign's own
// destination. A new one is made only from a destination that is still
// acceptable NOW and has not expired: a rule can change, or a date pass,
// between the campaign being approved and its last instalment going out.
func (s *Service) sendLinks(ctx context.Context, identity store.Identity, country string,
	campaignID *uuid.UUID, spec *store.LinkSpec, people []string) (map[string]string, error) {

	if s.LinkBaseURL == "" || campaignID == nil || spec == nil || len(people) == 0 {
		return map[string]string{}, nil
	}
	mint := spec
	if CheckLinkDestination(spec.Destination, country, LinkHost(s.LinkBaseURL)) != "" ||
		(spec.ExpiresAt != nil && !spec.ExpiresAt.After(time.Now())) {
		mint = nil
	}
	links, err := store.CampaignLinks(ctx, s.DB, identity, *campaignID, people, mint)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(links))
	for person, link := range links {
		if link.ExpiresAt != nil && !link.ExpiresAt.After(time.Now()) {
			continue
		}
		out[person] = s.LinkBaseURL + "/l/" + link.Code
	}
	return out, nil
}

// unresolvedLink is the refusal for a message whose link could not be made.
var unresolvedLink = messaging.GateFailureCode(messaging.ErrVariableUnresolved)
