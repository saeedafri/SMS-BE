package billing_test

import (
	"strings"
	"testing"

	"github.com/saeedafri/sms-be/internal/domain/billing"
)

// Every cost estimate and every charge is a multiple of this number, so the
// boundaries are tested exactly rather than approximately.
func TestSegmentCountAtGSM7Boundaries(t *testing.T) {
	cases := []struct {
		name   string
		length int
		want   int
	}{
		{"empty still costs one", 0, 1},
		{"one character", 1, 1},
		{"exactly one segment", 160, 1},
		{"one over tips into two", 161, 2},
		{"exactly two segments", 306, 2}, // 153 * 2
		{"one over tips into three", 307, 3},
		{"exactly three segments", 459, 3}, // 153 * 3
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Repeat("a", tc.length)
			if got := billing.SegmentCount(body); got != tc.want {
				t.Fatalf("SegmentCount(%d GSM-7 chars) = %d, want %d", tc.length, got, tc.want)
			}
		})
	}
}

func TestSegmentCountAtUCS2Boundaries(t *testing.T) {
	cases := []struct {
		name   string
		length int
		want   int
	}{
		{"one character", 1, 1},
		{"exactly one segment", 70, 1},
		{"one over tips into two", 71, 2},
		{"exactly two segments", 134, 2}, // 67 * 2
		{"one over tips into three", 135, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Devanagari is outside GSM-7, so the whole body is UCS-2.
			body := strings.Repeat("क", tc.length)
			if got := billing.SegmentCount(body); got != tc.want {
				t.Fatalf("SegmentCount(%d UCS-2 chars) = %d, want %d", tc.length, got, tc.want)
			}
		})
	}
}

// The expensive surprise: one character outside GSM-7 re-encodes the whole
// message, cutting capacity from 160 to 70. A smart quote pasted from a word
// processor is the usual culprit, and it more than doubles the bill.
func TestASingleNonGSM7CharacterReEncodesTheWholeMessage(t *testing.T) {
	plain := strings.Repeat("a", 160)
	if got := billing.SegmentCount(plain); got != 1 {
		t.Fatalf("160 plain characters = %d segments, want 1", got)
	}

	withSmartQuote := strings.Repeat("a", 159) + "’" // right single quotation mark
	if billing.IsGSM7(withSmartQuote) {
		t.Fatal("a smart quote was treated as GSM-7")
	}
	if got := billing.SegmentCount(withSmartQuote); got != 3 {
		t.Fatalf("160 characters including a smart quote = %d segments, want 3", got)
	}
}

// GSM-7 extension characters occupy two septets, so 80 of them fill a segment.
func TestExtensionCharactersCostTwoSeptets(t *testing.T) {
	if !billing.IsGSM7("€") {
		t.Fatal("the euro sign should be GSM-7 (extension table)")
	}
	if got := billing.SegmentCount(strings.Repeat("€", 80)); got != 1 {
		t.Fatalf("80 euro signs = %d segments, want 1", got)
	}
	if got := billing.SegmentCount(strings.Repeat("€", 81)); got != 2 {
		t.Fatalf("81 euro signs = %d segments, want 2", got)
	}
}

// Emoji are outside the Basic Multilingual Plane and cost two UTF-16 code
// units each, so 35 of them fill a UCS-2 segment rather than 70.
func TestEmojiCostTwoUnitsEach(t *testing.T) {
	if got := billing.SegmentCount(strings.Repeat("😀", 35)); got != 1 {
		t.Fatalf("35 emoji = %d segments, want 1", got)
	}
	if got := billing.SegmentCount(strings.Repeat("😀", 36)); got != 2 {
		t.Fatalf("36 emoji = %d segments, want 2", got)
	}
}

func TestIsGSM7RecognisesCommonAlphabets(t *testing.T) {
	gsm7 := []string{"", "Hello, world!", "Order #1234 ships today.", "£10 off", "a@b.c"}
	for _, body := range gsm7 {
		if !billing.IsGSM7(body) {
			t.Errorf("IsGSM7(%q) = false, want true", body)
		}
	}
	ucs2 := []string{"नमस्ते", "你好", "😀", "café’s"}
	for _, body := range ucs2 {
		if billing.IsGSM7(body) {
			t.Errorf("IsGSM7(%q) = true, want false", body)
		}
	}
}
