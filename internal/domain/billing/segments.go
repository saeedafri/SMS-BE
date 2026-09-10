package billing

import (
	"regexp"
	"strings"
)

// SMS segment arithmetic. These numbers are from GSM 03.38 and are not
// adjustable preferences: a 161-character GSM-7 message really is billed as two
// segments, and getting this wrong means every cost estimate and every charge
// is wrong by a multiple.
const (
	gsm7SingleLimit = 160 // one segment
	gsm7MultiLimit  = 153 // per segment once concatenated (7 chars go to the UDH)
	ucs2SingleLimit = 70
	ucs2MultiLimit  = 67
)

// gsm7Charset is the GSM 03.38 default alphabet plus its extension table.
// Anything outside it forces the whole message to UCS-2 — including a single
// smart quote pasted in from a word processor, which is the most common way a
// 160-character message silently becomes a 70-character one.
var gsm7Charset = buildGSM7Charset()

func buildGSM7Charset() map[rune]bool {
	const basic = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?" +
		"¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"
	// Extension characters occupy two septets each.
	const extension = "^{}\\[~]|€"

	set := make(map[rune]bool, len(basic)+len(extension))
	for _, r := range basic {
		set[r] = true
	}
	for _, r := range extension {
		set[r] = true
	}
	return set
}

// IsGSM7 reports whether every character can be encoded in the GSM-7 alphabet.
func IsGSM7(body string) bool {
	for _, r := range body {
		if !gsm7Charset[r] {
			return false
		}
	}
	return true
}

// gsm7Length counts septets, charging two for extension-table characters.
func gsm7Length(body string) int {
	const extension = "^{}\\[~]|€"
	length := 0
	for _, r := range body {
		if strings.ContainsRune(extension, r) {
			length += 2
		} else {
			length++
		}
	}
	return length
}

// SegmentCount returns how many SMS segments a body occupies.
//
// An empty body still costs one segment: the carrier bills for the submission,
// not for the characters.
func SegmentCount(body string) int {
	if IsGSM7(body) {
		length := gsm7Length(body)
		if length <= gsm7SingleLimit {
			return 1
		}
		return ceilDiv(length, gsm7MultiLimit)
	}

	// UCS-2 counts UTF-16 code units, so characters outside the Basic
	// Multilingual Plane — emoji, most notably — cost two each.
	length := 0
	for _, r := range body {
		if r > 0xFFFF {
			length += 2
		} else {
			length++
		}
	}
	if length <= ucs2SingleLimit {
		return 1
	}
	return ceilDiv(length, ucs2MultiLimit)
}

func ceilDiv(value, divisor int) int {
	return (value + divisor - 1) / divisor
}

// AssumedVariableChars is the width one {{variable}} is assumed to substitute
// to when quoting the upper bound of a segment range.
//
// Twenty, matching the frontend's ASSUMED_VARIABLE_CHARS, and the number
// matters less than the two sides agreeing on it: a different constant here
// would put a different range on the campaign wizard than on the template
// editor, for the same body.
const AssumedVariableChars = 20

// variableToken is the {{name}} syntax, defined once. A second copy would drift
// the day the syntax changes, and the drift would surface as a wrong price.
var variableToken = regexp.MustCompile(`\{\{\s*[A-Za-z0-9_]+\s*\}\}`)

// SegmentBounds returns how many segments one recipient's message can cost, as
// a range.
//
// A body carrying {{variables}} becomes as many different messages as there are
// recipients, and SMS bills per segment per message, so no single integer
// describes it. Counting the token as written is the one answer that is
// certainly wrong twice over: "{{first_name}}" is fourteen characters that
// never reach a handset, and its braces are GSM 03.38 extension characters
// charged at two septets each — eighteen septets of a message that will not
// contain one of them.
//
// BOTH ENDS ARE MEASURED ON SUBSTITUTED TEXT, which is what keeps the braces
// out of the arithmetic: every variable empty at the low end, every variable at
// the assumed width at the high end. A body with no variables returns the same
// number twice.
//
// The encoding is judged on the fixed text alone, because it is all that is
// known. A value carrying an emoji would flip the whole message to UCS-2 and
// halve its capacity at send time; nothing here can predict that, and
// pretending otherwise would be a worse lie than the range already is.
func SegmentBounds(body string) (int, int) {
	emptied := variableToken.ReplaceAllString(body, "")
	widened := variableToken.ReplaceAllString(body,
		strings.Repeat("X", AssumedVariableChars))

	low, high := SegmentCount(emptied), SegmentCount(widened)
	if low < 1 {
		low = 1
	}
	if high < low {
		high = low
	}
	return low, high
}
