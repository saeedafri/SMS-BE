package compliance_test

import (
	"testing"
	"time"

	"github.com/saeedafri/sms-be/internal/domain/compliance"
)

func TestIndiaPromotionalWindowIsTenToNineIST(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+1800)
	cases := []struct {
		at   time.Time
		want bool
	}{
		{time.Date(2026, 9, 13, 9, 59, 0, 0, ist), false},
		{time.Date(2026, 9, 13, 10, 0, 0, 0, ist), true},
		{time.Date(2026, 9, 13, 20, 59, 0, 0, ist), true},
		{time.Date(2026, 9, 13, 21, 0, 0, 0, ist), false},
		// 04:00 UTC is 09:30 IST: a UTC reading would wrongly allow it.
		{time.Date(2026, 9, 13, 4, 0, 0, 0, time.UTC), false},
	}
	for _, tc := range cases {
		if got := compliance.PromotionalAllowedAt("IN", tc.at); got != tc.want {
			t.Errorf("IN at %s = %v, want %v", tc.at, got, tc.want)
		}
	}
	if !compliance.PromotionalAllowedAt("US", time.Date(2026, 9, 13, 3, 0, 0, 0, ist)) {
		t.Error("a country without the rule refused promotional traffic")
	}

	night := time.Date(2026, 9, 13, 22, 30, 0, 0, ist)
	if got, want := compliance.NextPromotionalOpening("IN", night),
		time.Date(2026, 9, 14, 10, 0, 0, 0, ist); !got.Equal(want) {
		t.Errorf("next opening after 22:30 = %s, want %s", got, want)
	}
	dawn := time.Date(2026, 9, 13, 6, 0, 0, 0, ist)
	if got, want := compliance.NextPromotionalOpening("IN", dawn),
		time.Date(2026, 9, 13, 10, 0, 0, 0, ist); !got.Equal(want) {
		t.Errorf("next opening after 06:00 = %s, want %s", got, want)
	}
}
