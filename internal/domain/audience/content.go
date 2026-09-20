package audience

import (
	"encoding/json"
	"sort"
)

// A message is a document, not a string.
//
// An SMS keeps its text in one place, so walking that one string found every
// slot it had. An RCS message does not: the words a handset shows live in
// card.title, card.description and card.mediaUrl, and each suggestion carries
// its own text, url and phoneNumber. Reading a named few of those is how a
// {{token}} hiding in a button link survives the fill and reaches a handset
// literally — the frontend found exactly that bug in their own parser, which
// read three fields and missed the one in a link.
//
// So nothing here names a field. EVERY string in the document is walked, and
// the same walk both collects the slots and fills them, which is the property
// that matters: a collector and a filler that read different fields disagree
// about who can be personalised, and the disagreement only shows up on a
// handset.

// walkStrings rewrites every string anywhere in a decoded JSON document.
//
// Object keys are left alone. A key is a field name from the contract, not
// content a customer typed, and rewriting one would rename the field.
func walkStrings(value any, visit func(string) string) any {
	switch typed := value.(type) {
	case string:
		return visit(typed)
	case map[string]any:
		for key, child := range typed {
			typed[key] = walkStrings(child, visit)
		}
	case []any:
		for index, child := range typed {
			typed[index] = walkStrings(child, visit)
		}
	}
	return value
}

// SlotsInDocument names every slot used anywhere in a JSON message document.
//
// Sorted rather than in document order, because a JSON object has no order a
// Go map preserves — and an unstable list would make the same template report
// its missing variables differently on every call.
//
// A document that is not JSON names nothing. That is not a silent failure to
// paper over: the column is written by the API layer from the contract's own
// generated types, so bytes that will not decode are bytes no send path can
// read either, and the gate refuses the message for the reason it actually has.
func SlotsInDocument(document []byte) []string {
	decoded, ok := decodeDocument(document)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	walkStrings(decoded, func(text string) string {
		for _, name := range SlotsIn(text) {
			seen[name] = true
		}
		return text
	})
	return sortedNames(seen)
}

// FillDocument substitutes every slot in every string of a JSON document for
// one recipient, and names the slots that had no value.
//
// The document comes back with its shape untouched and its text filled. A slot
// with no value is left as its raw {{token}}, exactly as Fill leaves one in a
// body — and, exactly as with a body, a document with any missing slot must
// never be dispatched.
func FillDocument(document []byte, fields, mapping map[string]string) ([]byte, []string) {
	decoded, ok := decodeDocument(document)
	if !ok {
		return document, nil
	}
	missing := map[string]bool{}
	filled := walkStrings(decoded, func(text string) string {
		one := Fill(text, fields, mapping)
		for _, name := range one.Missing {
			missing[name] = true
		}
		return one.Text
	})
	encoded, err := json.Marshal(filled)
	if err != nil {
		return document, sortedNames(missing)
	}
	return encoded, sortedNames(missing)
}

// HasSlot reports whether a {{token}} survived into text that is about to be
// dispatched.
//
// It reads the rendered string rather than trusting the list of missing values
// the fill reported, which is the whole point: a bug in the fill is what this
// exists to catch, and a broken fill is exactly the thing that would also
// report nothing missing.
func HasSlot(text string) bool {
	return len(SlotsIn(text)) > 0
}

func decodeDocument(document []byte) (any, bool) {
	if len(document) == 0 {
		return nil, false
	}
	var decoded any
	if err := json.Unmarshal(document, &decoded); err != nil {
		return nil, false
	}
	return decoded, true
}

func sortedNames(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
