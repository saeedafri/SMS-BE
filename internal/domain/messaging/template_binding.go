package messaging

import "strings"

// Template binding, the specifically Indian gate.
//
// An Indian operator does not read a message and decide whether it looks like
// spam. It matches the content against a template registered on DLT and drops
// everything that does not match. So "the body must be a legal instantiation of
// the registered template" is not a style rule — it is the difference between a
// message that arrives and one that is dropped after we charged for it.
//
// A legal instantiation means: take the registered body, treat every {{name}}
// as a wildcard, and require the submitted text to carry the remaining fixed
// segments, in order, anchored at both ends.
//
// Anchored is the whole point. A substring check passes for a message that
// merely opens with the template's first words and then says something else
// entirely, which is a different message and is exactly what the operator will
// drop.
func MatchesTemplate(templateBody, submitted string) bool {
	segments := fixedSegments(templateBody)

	// A template with no variables is matched exactly.
	if len(segments) == 1 {
		return submitted == segments[0]
	}

	position := 0
	for i, segment := range segments {
		if segment == "" {
			// A variable at the very start or end. Nothing fixed to anchor on
			// at that edge, which is correct: {{code}} is your OTP may begin
			// with anything.
			continue
		}
		if i == 0 {
			if !strings.HasPrefix(submitted, segment) {
				return false
			}
			position = len(segment)
			continue
		}
		next := strings.Index(submitted[position:], segment)
		if next < 0 {
			return false
		}
		position += next + len(segment)
	}

	if last := segments[len(segments)-1]; last != "" && !strings.HasSuffix(submitted, last) {
		return false
	}
	return true
}

// fixedSegments splits a template body on its {{variables}}, returning the
// literal text between them. An unclosed {{ is treated as literal text, because
// it is: the customer wrote it, the regulator approved it, and guessing that
// they meant a variable would silently widen what the template matches.
func fixedSegments(body string) []string {
	segments := []string{}
	remainder := body
	for {
		open := strings.Index(remainder, "{{")
		if open < 0 {
			segments = append(segments, remainder)
			return segments
		}
		close := strings.Index(remainder[open:], "}}")
		if close < 0 {
			segments = append(segments, remainder)
			return segments
		}
		segments = append(segments, remainder[:open])
		remainder = remainder[open+close+len("}}"):]
	}
}
