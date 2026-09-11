package rcs_test

import (
	"strings"
	"testing"

	"github.com/saeedafri/sms-be/internal/domain/rcs"
)

// The contrast numbers are the published WCAG ones for these colours. They are
// the check on the luminance maths, which is the part of this that is easy to
// get subtly wrong — a plain gamma curve instead of the piecewise sRGB one
// passes every mid-tone and fails near black, which is the end of the range
// this rule exists to catch.
func TestAPaleBrandColourIsRefusedAndAStrongOneIsNot(t *testing.T) {
	cases := []struct {
		name    string
		color   string
		refused bool
	}{
		{"black", "#000000", false},
		{"a strong brand red", "#C62828", false},
		{"pure blue, 8.6:1", "#0000FF", false},
		// 4.54:1 — just over the line, and the reason the boundary is tested
		// from both sides rather than at some comfortable distance.
		{"grey at 4.54:1", "#767676", false},
		{"grey at 4.47:1, just under", "#777777", true},
		{"pure yellow, 1.07:1", "#FFFF00", true},
		{"white on white", "#FFFFFF", true},
		{"a pale mint", "#B2F2BB", true},
		// Optional. An agent without a colour gets the carrier's default rather
		// than a refusal.
		{"no colour at all", "", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := rcs.CheckPrimaryColor(testCase.color)
			if testCase.refused && err == nil {
				t.Fatalf("%s was accepted", testCase.color)
			}
			if !testCase.refused && err != nil {
				t.Fatalf("%s was refused: %v", testCase.color, err)
			}
			// The customer has to pick another colour, so the refusal has to
			// say how far off this one is. "Invalid colour" costs a support
			// ticket.
			if err != nil && !strings.Contains(err.Error(), ":1") {
				t.Errorf("the refusal does not name the ratio: %v", err)
			}
		})
	}
}

func TestAColourThatIsNotAColourSaysSo(t *testing.T) {
	for _, bad := range []string{"red", "#FFF", "#GGGGGG", "0000FF", "#0000FFF"} {
		err := rcs.CheckPrimaryColor(bad)
		if err == nil {
			t.Errorf("%q was accepted as a colour", bad)
			continue
		}
		if !strings.Contains(err.Error(), "#RRGGBB") {
			t.Errorf("%q refused without saying what the format is: %v", bad, err)
		}
	}
}

// Characters, not bytes. A name in Devanagari is inside the carrier's limit and
// well over 40 bytes, and counting bytes would make this a rule about our
// encoding rather than about Airtel's.
func TestTheNameLimitCountsCharactersNotBytes(t *testing.T) {
	devanagari := strings.Repeat("न", 40)
	if len(devanagari) <= rcs.MaxDisplayNameLength {
		t.Fatalf("fixture is %d bytes; it no longer separates the two rules", len(devanagari))
	}
	if err := rcs.CheckDisplayName(devanagari); err != nil {
		t.Errorf("a 40-character Devanagari name was refused: %v", err)
	}
	if err := rcs.CheckDisplayName(strings.Repeat("a", 41)); err == nil {
		t.Error("a 41-character name was accepted")
	}
}
