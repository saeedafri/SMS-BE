package audience

import "strings"

// Columns, slots, and the one rule that joins them.
//
// A contact's fields map is keyed by the CUSTOMER'S own column names, exactly
// as their spreadsheet spells them — "First Name", "customer_name", "Order ID".
// A template's slots are spelled the way a template can spell them:
// {{firstName}}. The two are joined by a list's variable mapping, never by
// hoping they match.
//
// This is the mirror of the frontend's src/lib/contacts/fields.ts. The campaign
// wizard shows a customer the real message for a real contact before they send,
// computed there with this rule; the fan-out then sends with this rule. If the
// two ever differ, the customer is shown one message and a handset receives
// another, which is the one failure neither side can detect on its own. Any
// change here is a change there.

// ResolveField is the value a slot resolves to for one contact, and whether it
// resolved at all.
//
// The mapping wins. When it names a column the contact does not have, the slot
// does NOT fall back to a column spelled like the slot: falling back would
// substitute different data than the mapping says it will, silently, which is
// worse than resolving to nothing and being skipped for it.
//
// Whitespace-only is no value. That is the whole point of the rule: an empty
// value used to be substituted as an empty string and delivered "Dear ," — a
// grammatically plausible sentence with a hole in it, no error anywhere, and
// the operator matching it against the registered DLT template none the wiser.
// A value that is not really there is treated as not there.
//
// The value returned is TRIMMED, and that is what must be substituted. The
// preview trims, so the send has to trim, or the two disagree by whitespace —
// which on a segment boundary is a different number of segments and a different
// price.
func ResolveField(fields, mapping map[string]string, slot string) (string, bool) {
	key := slot
	if mapped, named := mapping[slot]; named {
		key = mapped
	}
	value := strings.TrimSpace(fields[key])
	if value == "" {
		return "", false
	}
	return value, true
}

// Personalised is one recipient's message, and what stopped it being complete.
type Personalised struct {
	// Text is what the handset would receive. A slot with no value is left as
	// its raw {{token}} rather than blanked — but a message with any Missing
	// slot must never be SENT, so this is what a preview shows and what a
	// refusal quotes, not what goes to a carrier.
	Text string
	// Missing names the slots this contact has no value for, in the order the
	// template uses them, each once.
	Missing []string
}

// Fill substitutes every slot in a body for one contact.
//
// Slots are found by walking the body rather than by walking the contact's
// fields, which is the direction that matters: walking the fields can only
// replace slots that happen to have a value, so a slot with no value is never
// visited and silently survives into the delivered text. That is exactly how
// "Dear {{firstName}}," reached 999 handsets in 1,000.
func Fill(body string, fields, mapping map[string]string) Personalised {
	out := Personalised{Missing: []string{}}
	var text strings.Builder
	seen := map[string]bool{}

	for _, segment := range SplitBody(body) {
		if !segment.IsSlot {
			text.WriteString(segment.Text)
			continue
		}
		value, resolved := ResolveField(fields, mapping, segment.Text)
		if !resolved {
			// Left raw, so a preview shows the customer the literal token their
			// recipient would have read.
			text.WriteString("{{" + segment.Text + "}}")
			if !seen[segment.Text] {
				seen[segment.Text] = true
				out.Missing = append(out.Missing, segment.Text)
			}
			continue
		}
		text.WriteString(value)
	}
	out.Text = text.String()
	return out
}

// Segment is one piece of a template body: literal text, or a slot's name.
type Segment struct {
	Text   string
	IsSlot bool
}

// SplitBody breaks a body into literals and {{slots}}.
//
// An unclosed {{ is literal text, because it is: the customer typed it, and
// guessing that they meant a slot would make a message disappear into a
// placeholder nobody can fill.
func SplitBody(body string) []Segment {
	segments := []Segment{}
	for {
		open := strings.Index(body, "{{")
		if open < 0 {
			break
		}
		close := strings.Index(body[open:], "}}")
		if close < 0 {
			break
		}
		name := strings.TrimSpace(body[open+2 : open+close])
		if name == "" {
			// "{{}}" names nothing, so it cannot be filled and is not a slot.
			segments = append(segments, Segment{Text: body[:open+close+2]})
			body = body[open+close+2:]
			continue
		}
		if open > 0 {
			segments = append(segments, Segment{Text: body[:open]})
		}
		segments = append(segments, Segment{Text: name, IsSlot: true})
		body = body[open+close+2:]
	}
	if body != "" {
		segments = append(segments, Segment{Text: body})
	}
	return segments
}

// SlotsIn names every slot a body uses, in order, each once. It is what tells a
// campaign which columns its audience has to carry.
func SlotsIn(body string) []string {
	slots := []string{}
	seen := map[string]bool{}
	for _, segment := range SplitBody(body) {
		if segment.IsSlot && !seen[segment.Text] {
			seen[segment.Text] = true
			slots = append(slots, segment.Text)
		}
	}
	return slots
}
