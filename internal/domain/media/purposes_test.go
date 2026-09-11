package media_test

import (
	"strings"
	"testing"

	"github.com/saeedafri/sms-be/internal/domain/media"
)

// A rich card takes either an image or a video and the carrier allows them
// different amounts of room — Vi p94, 2 MB for an image and 10 MB for a video.
//
// The branch is worth a test because getting it wrong is invisible in the
// direction that matters: a flat 2 MB refuses a legitimate card video at a
// fifth of its allowance while telling the customer a limit that is true of
// images and false of the file they are holding.
func TestACardVideoGetsTheVideoAllowanceAndAnImageDoesNot(t *testing.T) {
	rule, ok := media.RuleFor(media.PurposeTemplateMedia)
	if !ok {
		t.Fatal("template_media has no rule")
	}

	const mb = 1024 * 1024
	cases := []struct {
		name        string
		contentType string
		bytes       int64
		wantRefused bool
	}{
		{"an image inside the image limit", "image/png", 2 * mb, false},
		{"an image over the image limit", "image/png", 2*mb + 1, true},
		// The one the flat limit got wrong: comfortably over the image cap and
		// comfortably inside the video one.
		{"a video over the image limit but inside its own", "video/mp4", 6 * mb, false},
		{"a video at its limit", "video/mp4", 10 * mb, false},
		{"a video over its own limit", "video/mp4", 10*mb + 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rule.CheckSize(tc.contentType, tc.bytes)
			if tc.wantRefused && err == nil {
				t.Fatalf("%d bytes of %s was accepted", tc.bytes, tc.contentType)
			}
			if !tc.wantRefused && err != nil {
				t.Fatalf("%d bytes of %s was refused: %v", tc.bytes, tc.contentType, err)
			}
			// A refusal has to name the limit it applied, or a customer holding
			// a 6 MB video cannot tell which of the two numbers they hit.
			if err != nil && !strings.Contains(err.Error(), "limit for this purpose") {
				t.Errorf("refusal does not name the limit: %v", err)
			}
		})
	}
}

// A purpose with no separate video allowance uses its one limit for everything.
// Without the MaxVideoBytes > 0 guard, a zero would read as "videos may be
// zero bytes" and refuse every one of them.
func TestAPurposeWithNoVideoAllowanceUsesItsOneLimit(t *testing.T) {
	rule, ok := media.RuleFor(media.PurposeAgentLogo)
	if !ok {
		t.Fatal("agent_logo has no rule")
	}
	if err := rule.CheckSize("video/mp4", 40*1024); err != nil {
		t.Fatalf("40 KB under a 50 KB limit was refused: %v", err)
	}
	if err := rule.CheckSize("video/mp4", 60*1024); err == nil {
		t.Fatal("60 KB over a 50 KB limit was accepted")
	}
}
