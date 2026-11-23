package compliance

import (
	"testing"
	"time"
)

func TestQuietHoursAreTheHoursOfSilence(t *testing.T) {
	night := QuietHours{From: "22:00", To: "06:00", Zone: "UTC"}
	at := func(clock string) time.Time {
		parsed, _ := time.Parse("2006-01-02 15:04", "2026-11-23 "+clock)
		return parsed
	}
	for clock, silent := range map[string]bool{"21:59": false, "22:00": true, "23:30": true,
		"00:10": true, "05:59": true, "06:00": false, "12:00": false} {
		if got := night.Contains(at(clock)); got != silent {
			t.Errorf("%s silent = %v, want %v", clock, got, silent)
		}
	}
	if got := night.NextOpening(at("23:30")); !got.Equal(at("06:00").Add(24 * time.Hour)) {
		t.Errorf("opening after 23:30 = %v, want 06:00 next day", got)
	}
	if got := night.NextOpening(at("12:00")); !got.Equal(at("12:00")) {
		t.Errorf("outside the window the launch moved to %v", got)
	}
}

func TestQuietHoursProblems(t *testing.T) {
	for _, q := range []QuietHours{{"22:00", "22:00", "UTC"}, {"25:00", "06:00", "UTC"},
		{"22:00", "06:00", "Mars/Olympus"}, {"22:00", "06:00", ""}} {
		if q.Problem() == "" {
			t.Errorf("%+v accepted", q)
		}
	}
	if problem := (QuietHours{"21:00", "08:00", "America/New_York"}).Problem(); problem != "" {
		t.Errorf("a good window refused: %s", problem)
	}
}

// India's promotional hours open at 10:00 IST; quiet hours of 21:00 to 09:00
// IST open earlier. A launch at 23:00 IST waits for the later of the two.
func TestLaunchMovesUntilNeitherRuleMovesIt(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+1800)
	start := time.Date(2026, 11, 23, 23, 0, 0, 0, ist)
	quiet := &QuietHours{From: "21:00", To: "09:00", Zone: "Asia/Kolkata"}
	want := time.Date(2026, 11, 24, 10, 0, 0, 0, ist)
	if got := LaunchAt("IN", true, quiet, start); !got.Equal(want) {
		t.Errorf("launch = %v, want %v", got.In(ist), want)
	}
	if got := LaunchAt("IN", false, quiet, start); !got.Equal(time.Date(2026, 11, 24, 9, 0, 0, 0, ist)) {
		t.Errorf("transactional launch = %v, want 09:00 IST", got.In(ist))
	}
}
