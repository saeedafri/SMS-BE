// Package media holds what the platform will accept as an uploaded file, per
// purpose.
//
// EVERY NUMBER HERE IS EXTRACTED FROM A CARRIER DOCUMENT, with the page it came
// from, because the frontend's provisional figures were generous by one to two
// orders of magnitude and a number nobody sourced becomes a file the carrier
// refuses after the customer has already exported it, sized it and uploaded it.
package media

import "fmt"

// Purpose is what an asset is for. Constraints differ per purpose — a logo is
// square and tiny, a hero is a wide banner — so the server validates against
// the stated purpose rather than accepting any image anywhere.
type Purpose string

const (
	PurposeAgentLogo            Purpose = "agent_logo"
	PurposeAgentHero            Purpose = "agent_hero"
	PurposeTemplateMedia        Purpose = "template_media"
	PurposeVerificationDocument Purpose = "verification_document"
)

// Rule is what one purpose accepts.
//
// Width and Height are EXACT when non-zero. Carriers state agent artwork as an
// exact pixel pair rather than a ratio, and a "close enough" upload is refused
// at agent submission days later with no way for the customer to see why.
type Rule struct {
	ContentTypes []string
	MaxBytes     int64
	Width        int
	Height       int
	// Source is the document and page the numbers came from, so the next person
	// to change one can check it rather than trusting this comment.
	Source string
}

// rules are the accepted limits per purpose.
//
// Where Airtel and Vi disagree the TIGHTER number wins, because an asset has to
// satisfy whichever carrier ends up serving it and we cannot know which at
// upload time. Each entry records both figures so the choice is auditable.
var rules = map[Purpose]Rule{
	// Airtel p6, "Key Assets Checklist": "Dimensions: Exactly 224 px x 224 px",
	// "Max File Size: 50 KB", "Supported Formats: JPEG, JPG, PNG".
	// Vi states nothing about agent brand assets in either document, so
	// Airtel's numbers stand unopposed rather than being an intersection.
	PurposeAgentLogo: {
		ContentTypes: []string{"image/png", "image/jpeg"},
		MaxBytes:     50 * 1024,
		Width:        224,
		Height:       224,
		Source:       "Airtel IQ RCS API Documentation v1.8, p6",
	},
	// Airtel p6: "Exactly 1440 px (width) x 448 px (height)", "Max File Size:
	// 200 KB". NOT to be confused with Vi's 1440x480 standalone card image
	// (Template Management p94) — different asset, and treating 1440-wide as
	// one shared spec would pass an image every agent submission refuses.
	PurposeAgentHero: {
		ContentTypes: []string{"image/png", "image/jpeg"},
		MaxBytes:     200 * 1024,
		Width:        1440,
		Height:       448,
		Source:       "Airtel IQ RCS API Documentation v1.8, p6",
	},
	// The intersection, and it is much tighter than it looks.
	//
	// Airtel p22 states a blanket "Image- 5mb / Video- 10mb" and no dimensions
	// at all. Vi's Template Management p94-95 is per surface and stricter:
	// standalone card image 2MB, CAROUSEL image 1MB, carousel video 5MB.
	//
	// 1 MB is the smallest image cap either carrier states for any surface a
	// template can use, and an asset uploaded for "template media" does not yet
	// know whether it will end up in a carousel. Refusing at 1 MB is the only
	// bound that cannot produce a carrier rejection later.
	PurposeTemplateMedia: {
		ContentTypes: []string{"image/png", "image/jpeg", "image/gif", "video/mp4"},
		MaxBytes:     1024 * 1024,
		Source:       "Vi Template Management API v17, p95 (carousel image, the tightest of four surfaces); Airtel p22 states 5mb with no per-surface breakdown",
	},
	// Neither carrier constrains this: it never reaches a carrier at all. It is
	// ours, read by an operator reviewing a brand claim, so the number is a
	// storage decision rather than a carrier one.
	PurposeVerificationDocument: {
		ContentTypes: []string{"application/pdf", "image/png", "image/jpeg"},
		MaxBytes:     10 * 1024 * 1024,
		Source:       "ours — no carrier consumes this file",
	},
}

func RuleFor(purpose Purpose) (Rule, bool) {
	rule, ok := rules[purpose]
	return rule, ok
}

// CheckType refuses a content type the purpose does not accept, naming what it
// got and what it wanted.
func (r Rule) CheckType(contentType string) error {
	for _, accepted := range r.ContentTypes {
		if accepted == contentType {
			return nil
		}
	}
	return fmt.Errorf("%s is not an accepted type here; this purpose takes %s",
		quoteOrNone(contentType), joinWithOr(r.ContentTypes))
}

// CheckSize refuses an oversized file, naming both numbers.
//
// The customer has to go and re-export the file, so the message has to carry
// enough to do it with. "Invalid image" costs them a support ticket; "1.8 MB,
// the limit is 50 KB" costs them ninety seconds.
func (r Rule) CheckSize(bytes int64) error {
	if bytes <= r.MaxBytes {
		return nil
	}
	return fmt.Errorf("the file is %s and the limit for this purpose is %s",
		humanBytes(bytes), humanBytes(r.MaxBytes))
}

// CheckDimensions refuses artwork that is not the exact size the carrier
// demands. Zero width means the purpose does not constrain dimensions.
func (r Rule) CheckDimensions(width, height int) error {
	if r.Width == 0 && r.Height == 0 {
		return nil
	}
	if width == r.Width && height == r.Height {
		return nil
	}
	return fmt.Errorf("the image is %d x %d and this purpose needs exactly %d x %d",
		width, height, r.Width, r.Height)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.0f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

func quoteOrNone(value string) string {
	if value == "" {
		return "a file with no content type"
	}
	return `"` + value + `"`
}

func joinWithOr(values []string) string {
	switch len(values) {
	case 0:
		return "nothing"
	case 1:
		return values[0]
	}
	out := ""
	for i, v := range values[:len(values)-1] {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out + " or " + values[len(values)-1]
}
