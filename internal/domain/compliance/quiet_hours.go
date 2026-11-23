package compliance

import (
	"fmt"
	"regexp"
	"time"
	// Zone names must resolve on any machine, with or without a zoneinfo
	// package installed.
	_ "time/tzdata"
)

// QuietHours is a campaign's own daily silence, in one IANA zone. From later
// than To crosses midnight (21:00 to 08:00). A launch inside it is held until To.
type QuietHours struct {
	From string `json:"from"`
	To   string `json:"to"`
	Zone string `json:"zone"`
}

var clockPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// Problem says what is wrong with the window, or "" when it is usable.
func (q QuietHours) Problem() string {
	switch {
	case !clockPattern.MatchString(q.From):
		return "quietHours from must be a time as HH:MM."
	case !clockPattern.MatchString(q.To):
		return "quietHours to must be a time as HH:MM."
	case q.From == q.To:
		return "quietHours from and to must differ."
	}
	if _, err := time.LoadLocation(q.Zone); err != nil || q.Zone == "" {
		return "quietHours zone must be an IANA zone name."
	}
	return ""
}

func minutesOf(clock string) int {
	var hours, minutes int
	fmt.Sscanf(clock, "%d:%d", &hours, &minutes)
	return hours*60 + minutes
}

func (q QuietHours) localMinutes(at time.Time) int {
	zone, err := time.LoadLocation(q.Zone)
	if err != nil {
		zone = time.UTC
	}
	local := at.In(zone)
	return local.Hour()*60 + local.Minute()
}

// Contains says whether at falls inside the silence.
func (q QuietHours) Contains(at time.Time) bool {
	m, from, to := q.localMinutes(at), minutesOf(q.From), minutesOf(q.To)
	if from < to {
		return m >= from && m < to
	}
	return m >= from || m < to
}

// NextOpening is when the silence ends, or at itself when it is not silent.
func (q QuietHours) NextOpening(at time.Time) time.Time {
	if !q.Contains(at) {
		return at
	}
	delta := (minutesOf(q.To) - q.localMinutes(at) + 1440) % 1440
	return at.Truncate(time.Minute).Add(time.Duration(delta) * time.Minute)
}

// LaunchAt is when a launch asked for at start really goes: moved by the
// destination's promotional hours and by the quiet hours in turn until
// neither moves it. Two passes settle any pair of daily windows; three is a
// margin.
func LaunchAt(country string, promotional bool, quiet *QuietHours, start time.Time) time.Time {
	at := start
	for pass := 0; pass < 3; pass++ {
		if promotional && !PromotionalAllowedAt(country, at) {
			at = NextPromotionalOpening(country, at)
		}
		if quiet != nil {
			at = quiet.NextOpening(at)
		}
	}
	return at
}
