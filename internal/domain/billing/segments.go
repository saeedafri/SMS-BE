package billing

import "strings"

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
