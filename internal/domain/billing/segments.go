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

	set := make(map[rune]bool, len(basic)+len(gsm7Extension))
	for _, r := range basic {
		set[r] = true
	}
	for _, r := range gsm7Extension {
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

// gsm7Extension is the GSM 03.38 extension table: each character in it
// occupies two septets.
const gsm7Extension = "^{}\\[~]|€"

// SegmentCount returns how many SMS segments a body occupies.
//
// An empty body still costs one segment: the carrier bills for the submission,
// not for the characters.
func SegmentCount(body string) int {
	parts, _ := Segments(body)
	return len(parts)
}

// Segments splits a body exactly as it goes over the wire: the text of each
// segment, and whether the message is GSM-7 (otherwise UCS-2).
//
// It is the one split. The SMPP client sends these parts and billing counts
// them, so what a customer is charged and what an operator receives cannot
// disagree. They did: the library's own splitter sent a 160-character message
// as two segments we billed as one, and 81 euro signs as one we billed as two.
//
// A segment boundary never falls inside a character. An extension character
// takes two septets and a character outside the Basic Multilingual Plane two
// UTF-16 units, and splitting either would put half a character on each handset
// fragment — so a part can end a unit short, and a body can take one segment
// more than dividing its length would suggest.
func Segments(body string) (parts []string, gsm7 bool) {
	gsm7 = IsGSM7(body)
	width := func(r rune) int {
		if gsm7 && strings.ContainsRune(gsm7Extension, r) || !gsm7 && r > 0xFFFF {
			return 2
		}
		return 1
	}
	single, multi := ucs2SingleLimit, ucs2MultiLimit
	if gsm7 {
		single, multi = gsm7SingleLimit, gsm7MultiLimit
	}

	total := 0
	for _, r := range body {
		total += width(r)
	}
	if total <= single {
		return []string{body}, gsm7
	}
	var part strings.Builder
	used := 0
	for _, r := range body {
		if used+width(r) > multi {
			parts = append(parts, part.String())
			part.Reset()
			used = 0
		}
		part.WriteRune(r)
		used += width(r)
	}
	return append(parts, part.String()), gsm7
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
