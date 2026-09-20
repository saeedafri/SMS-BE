package sending

import (
	"encoding/json"
	"strings"

	"github.com/saeedafri/sms-be/internal/domain/audience"
	"github.com/saeedafri/sms-be/internal/store"
)

// renderedMessage is one recipient's whole message, filled.
//
// Whole is the operative word. Until this existed the fan-out read
// template.Body, an RCS template leaves that null on purpose, so SlotsIn("")
// found nothing, nobody was skipped, and every RCS recipient was dispatched to
// — the card's own {{first_name}} never looked at. One walk over the entire
// message now collects and fills in the same pass, so the set of slots the
// skip decides on and the set the send substitutes cannot drift apart.
type renderedMessage struct {
	// body is the text this recipient would be sent, slots substituted.
	body string
	// content is the channel's rich document with every string in it filled.
	// The card is not transmitted — the carrier holds the registered template
	// and renders it from variables — but it is filled anyway, because it is
	// where the text for the operators that hold nothing comes from, and
	// because the dispatch invariant reads it.
	content []byte
	// variables are the template's declared slots resolved for this recipient,
	// by the same rule that filled the body. Resolved through the list's own
	// mapping rather than read straight out of the contact's columns: a list
	// whose spreadsheet says "First Name" and a template that says
	// {{firstName}} are joined by the mapping, and reading the raw map sent
	// Airtel an empty value for every mapped slot while our own log showed the
	// name.
	variables map[string]string
	// missing names every slot, anywhere in the message, this recipient has no
	// value for.
	missing []string
}

// renderMessage fills every string in a message for one recipient.
//
// fields are the values available — a contact's own columns for a campaign, or
// the variables a single send named — and mapping joins the customer's column
// names to the template's slots. A caller that already speaks slot names passes
// no mapping.
func renderMessage(template store.Template, body string, fields, mapping map[string]string) renderedMessage {
	out := renderedMessage{variables: map[string]string{}}
	seen := map[string]bool{}
	note := func(names []string) {
		for _, name := range names {
			if !seen[name] {
				seen[name] = true
				out.missing = append(out.missing, name)
			}
		}
	}

	filled := audience.Fill(body, fields, mapping)
	out.body = filled.Text
	note(filled.Missing)

	content, missingInContent := audience.FillDocument(richContent(template), fields, mapping)
	out.content = content
	note(missingInContent)

	// The slots the template DECLARES, which is what a carrier-held template
	// renders from. Usually a subset of what the walk above already found, and
	// deliberately checked anyway: a carrier template can declare a variable
	// that appears in no string we hold, and an unfilled one is a hole on the
	// handset exactly the same way.
	for _, name := range template.Variables {
		value, resolved := audience.ResolveField(fields, mapping, name)
		if !resolved {
			note([]string{name})
			continue
		}
		out.variables[name] = value
	}
	return out
}

// unresolved reports whether a {{token}} survived anywhere into what is about
// to be dispatched.
//
// It reads the rendered strings, not the missing list beside them. The two
// should always agree; when they do not, the fill is broken, and a fill broken
// enough to leave a token behind is broken enough to report nothing missing.
// This is the check that still holds in that case, which is the only case it
// is for.
func (r renderedMessage) unresolved() bool {
	if audience.HasSlot(r.body) || audience.HasSlot(string(r.content)) {
		return true
	}
	for _, value := range r.variables {
		if audience.HasSlot(value) {
			return true
		}
	}
	return false
}

// text is what an RCS submission carries for the operators that hold no
// template of their own.
//
// Airtel and Vi hold the registered template and render it themselves, so a
// campaign sends them no body. Google, and Jio — which reviews the assistant
// rather than the message — are handed nothing to show and refuse the send
// body_required. So the template's own text is rendered here, out of the
// ALREADY FILLED document, and only when the caller supplied no body of its
// own.
//
// Billing is untouched on purpose: the cost was priced from what the caller
// sent, and rendering text for a carrier is not a reason to charge for more
// segments.
func (r renderedMessage) text(channel string) string {
	if channel == "RCS" && strings.TrimSpace(r.body) == "" {
		return documentText(r.content)
	}
	return r.body
}

// richContent is the document whose strings this walk fills: RCS only.
//
// A template carries exactly one of three — rcs_content, wa_content or
// email_content — and the other two are deliberately not walked here. Nothing
// downstream transmits a filled WhatsApp or email document: their send paths
// carry the body, and what their content declares is already on
// template.Variables, parsed when the template was created. Walking them would
// compute a second copy of the same answer and hand two code paths the chance
// to disagree about it.
func richContent(template store.Template) []byte {
	return template.RCSContent
}

// documentText is the top-level text of a rich document, empty when it has
// none. A card-only RCS template has none, which is a real shape: the carriers
// that hold the template render the card themselves and need no text.
func documentText(document []byte) string {
	if len(document) == 0 {
		return ""
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(document, &content); err != nil {
		return ""
	}
	return content.Text
}

// templateSlots names every slot a template uses, anywhere in it: its body, its
// rich document, and the list it declares to a carrier. It is what tells a
// campaign which columns its audience has to carry.
func templateSlots(template store.Template, body string) []string {
	seen := map[string]bool{}
	names := []string{}
	for _, group := range [][]string{
		audience.SlotsIn(body),
		audience.SlotsInDocument(richContent(template)),
		template.Variables,
	} {
		for _, name := range group {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	return names
}

// templateText is the template's own plain text, empty when it has none. An
// RCS template leaves it null on purpose and keeps its words in a card.
func templateText(template store.Template) string {
	if template.Body == nil {
		return ""
	}
	return *template.Body
}
