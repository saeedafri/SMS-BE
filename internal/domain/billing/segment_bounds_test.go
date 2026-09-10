package billing_test

import (
	"strings"
	"testing"

	"github.com/saeedafri/sms-be/internal/domain/billing"
)

// A templated body is quoted as a range measured on SUBSTITUTED text.
//
// The old answer counted the body as written and added one when it saw "{{".
// That is wrong twice over and the second way is the expensive one:
//
//  1. "{{first_name}}" is fourteen characters no handset ever receives, and its
//     braces are GSM 03.38 extension characters charged at two septets each —
//     eighteen septets billed for text that does not exist.
//  2. "+1" is a guess. A body sitting far enough under the boundary with
//     several long variables tips by two segments, not one, so the quote was
//     under the eventual charge on exactly the campaigns where it mattered.
//
// Each case below is built to a septet count on purpose rather than to a
// pleasing string, because the boundary is the only thing being tested.
func TestSegmentBoundsMeasuresSubstitutedTextAtBothEnds(t *testing.T) {
	const variable = "{{first_name}}" // 14 written chars, 18 septets as written

	for _, c := range []struct {
		name     string
		body     string
		min, max int
		why      string
	}{
		{
			name: "no variables: one number, twice",
			body: strings.Repeat("a", 100),
			min:  1, max: 1,
			why: "a body with nothing to substitute has no range",
		},
		{
			name: "the written token is not the bound",
			// 150 fixed + a variable. Written it is 168 septets and would count
			// as 2; emptied it is 150 and is 1.
			body: strings.Repeat("a", 150) + variable,
			min:  1, max: 2,
			why: "the low end must not pay for braces",
		},
		{
			name: "+1 was not enough",
			// 120 fixed + three variables. Emptied: 120 -> 1 segment.
			// Widened: 120 + 60 = 180 -> 2 segments. The old rule gave
			// segments(written)=1+... and could only ever move by one.
			body: strings.Repeat("a", 120) + variable + variable + variable,
			min:  1, max: 2,
			why: "three variables at the assumed width cross a boundary the written count hides",
		},
		{
			name: "a spread of two whole segments",
			// 300 fixed -> 2 segments emptied. Plus six variables at 20 =
			// 420 -> 3 segments. A single integer cannot describe this.
			body: strings.Repeat("a", 300) + strings.Repeat(variable, 6),
			min:  2, max: 3,
			why: "the range is the answer, not a number near it",
		},
		{
			name: "whitespace inside the braces is still a variable",
			body: strings.Repeat("a", 150) + "{{ first_name }}",
			min:  1, max: 2,
			why: "the token syntax tolerates padding and the maths must agree with it",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			low, high := billing.SegmentBounds(c.body)
			if low != c.min || high != c.max {
				t.Errorf("bounds = %d..%d, want %d..%d — %s", low, high, c.min, c.max, c.why)
			}
			if low > high {
				t.Errorf("the low bound %d exceeds the high bound %d", low, high)
			}
			// The low end can never be dearer than counting the raw body, and
			// the high end can never be cheaper. This holds for every body and
			// is what makes it a bound rather than an estimate.
			if written := billing.SegmentCount(c.body); low > written && high < written {
				t.Errorf("the raw count %d falls outside %d..%d", written, low, high)
			}
		})
	}
}

// A variable is never charged as its braces. Stated separately because it is
// the specific defect, and because it is invisible in a body far from a
// boundary — it only shows when the eighteen phantom septets are what tips it.
func TestAWrittenVariableIsNotChargedAsText(t *testing.T) {
	// 145 fixed + one variable: 145 + 18 written septets = 163, over the
	// 160-septet single-segment boundary. Emptied it is 145 and fits.
	body := strings.Repeat("a", 145) + "{{first_name}}"

	if written := billing.SegmentCount(body); written != 2 {
		t.Fatalf("the raw count is %d — this fixture no longer straddles the boundary", written)
	}
	low, _ := billing.SegmentBounds(body)
	if low != 1 {
		t.Errorf("the low bound is %d segments, want 1 — a recipient whose value is "+
			"empty receives 145 characters and must not be quoted two segments for "+
			"braces that never reach the handset", low)
	}
}
