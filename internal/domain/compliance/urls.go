package compliance

import "regexp"

// urlPattern finds absolute http(s) URLs in message text.
//
// Deliberately loose on the trailing edge: a URL at the end of a sentence
// carries the full stop, and the regime's own check parses what it is handed,
// so a trailing character costs nothing. Being too strict here would be the
// expensive direction — a shortener that slipped through because the URL ended
// in a bracket is exactly the case this exists to catch.
var urlPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

// ExtractURLs returns every absolute URL in the text, in order.
//
// Message bodies are not templates: a customer can put a link straight into a
// one-off send, and that link is subject to the same country rules as one
// typed into the template editor.
func ExtractURLs(text string) []string {
	found := urlPattern.FindAllString(text, -1)
	if found == nil {
		return nil
	}
	// A trailing full stop or bracket is punctuation, not part of the URL.
	for i, raw := range found {
		found[i] = trimTrailingPunctuation(raw)
	}
	return found
}

func trimTrailingPunctuation(raw string) string {
	for len(raw) > 0 {
		switch raw[len(raw)-1] {
		case '.', ',', ')', ']', '}', ';', ':', '!', '?':
			raw = raw[:len(raw)-1]
		default:
			return raw
		}
	}
	return raw
}
