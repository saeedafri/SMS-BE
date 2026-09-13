package main

import "testing"

func TestParseAmountNeverRounds(t *testing.T) {
	good := map[string]int64{"5000": 500000, "5000.5": 500050, "0.01": 1, "12.34": 1234}
	for in, want := range good {
		if got, err := parseAmount(in); err != nil || got != want {
			t.Errorf("parseAmount(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "-5", "1.234", "1e5", "5,000", "10000000000"} {
		if got, err := parseAmount(in); err == nil {
			t.Errorf("parseAmount(%q) = %d, want refused", in, got)
		}
	}
}
