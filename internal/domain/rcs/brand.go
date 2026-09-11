// Package rcs holds the carriers' rules about an agent's brand, the ones a
// carrier applies at submission rather than at send.
//
// Both live here rather than only in the wizard because they refuse an agent
// DAYS after it was drafted, in a review queue the customer cannot see, with a
// carrier's own wording for a reason. A client-side check is the right place to
// tell someone quickly; it is the wrong place to decide.
package rcs

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// MaxDisplayNameLength is Airtel p6. It is not a database or a screen limit: an
// agent drafted with a longer name is refused at submission, and the name is
// what the handset draws.
const MaxDisplayNameLength = 40

// MinContrastAgainstWhite is Airtel p7. The handset draws the agent name on a
// white sheet, so a pale brand colour is unreadable and the carrier refuses it.
//
// 4.5:1 is WCAG AA for body text, which is what this is.
const MinContrastAgainstWhite = 4.5

// CheckDisplayName refuses a name no carrier will accept, naming both numbers
// so the customer can cut it to fit rather than guess.
func CheckDisplayName(name string) error {
	// Runes, not bytes. A name in Devanagari is well inside 40 characters and
	// well over 40 bytes, and refusing it would be a rule about our encoding
	// rather than about the carrier's.
	if length := len([]rune(name)); length > MaxDisplayNameLength {
		return fmt.Errorf("the display name is %d characters and the carrier allows %d",
			length, MaxDisplayNameLength)
	}
	return nil
}

// CheckPrimaryColor refuses a brand colour too pale to read on a handset.
//
// Empty is not a failure: the colour is optional, and an agent without one gets
// the carrier's default rather than a refusal.
func CheckPrimaryColor(color string) error {
	color = strings.TrimSpace(color)
	if color == "" {
		return nil
	}
	red, green, blue, ok := parseHexColor(color)
	if !ok {
		return fmt.Errorf("%q is not a colour; give it as #RRGGBB", color)
	}
	ratio := contrastAgainstWhite(red, green, blue)
	if ratio >= MinContrastAgainstWhite {
		return nil
	}
	return fmt.Errorf(
		"%s reaches only %.1f:1 against white and the carrier requires %.1f:1 — "+
			"the handset draws your agent name on a white sheet, so a pale colour "+
			"cannot be read", strings.ToUpper(color), ratio, MinContrastAgainstWhite)
}

func parseHexColor(color string) (red, green, blue int, ok bool) {
	if !strings.HasPrefix(color, "#") || len(color) != 7 {
		return 0, 0, 0, false
	}
	values := make([]int, 3)
	for i := range values {
		parsed, err := strconv.ParseInt(color[1+i*2:3+i*2], 16, 32)
		if err != nil {
			return 0, 0, 0, false
		}
		values[i] = int(parsed)
	}
	return values[0], values[1], values[2], true
}

// contrastAgainstWhite is the WCAG 2.x ratio, which is what "4.5:1" means
// everywhere it is written down — including in Airtel's own document.
//
// White's relative luminance is exactly 1, so the general (L1+0.05)/(L2+0.05)
// collapses to 1.05 over the colour's own luminance plus 0.05.
func contrastAgainstWhite(red, green, blue int) float64 {
	luminance := 0.2126*channel(red) + 0.7152*channel(green) + 0.0722*channel(blue)
	return 1.05 / (luminance + 0.05)
}

// channel linearises one sRGB component. The piecewise form is the standard's,
// not an approximation of it: a plain gamma curve is wrong near black, which is
// exactly the end of the range a contrast check has to be right about.
func channel(value int) float64 {
	scaled := float64(value) / 255
	if scaled <= 0.03928 {
		return scaled / 12.92
	}
	return math.Pow((scaled+0.055)/1.055, 2.4)
}
