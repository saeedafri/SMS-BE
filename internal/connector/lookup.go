package connector

import (
	"context"
	"regexp"
	"strconv"
)

// LineInfo is what a number lookup says about one number. Nil fields were not
// asked for or not answered.
type LineInfo struct {
	LineType          string // mobile, landline, voip or unknown
	Carrier, MCC, MNC *string
	Ported, Reachable *bool
}

// LineLookup answers what kind of line a number is (HLR/MNP). The vendor is
// not chosen yet (decision D-J); a deployment without one offers no lookup.
type LineLookup interface {
	Lookup(ctx context.Context, msisdn string, fields []string) (LineInfo, error)
}

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

// IsE164 says whether a number is in international format.
func IsE164(msisdn string) bool { return e164.MatchString(msisdn) }

// FakeLineLookup replays the frontend mock's answers, deterministic from the
// number's last digits: 0 landline, 1 voip, 9 unknown and unreachable, 7
// ported, anything else a mobile on one of India's four networks.
type FakeLineLookup struct{}

var fakeCarriers = []struct{ name, mcc, mnc string }{
	{"Reliance Jio", "404", "874"}, {"Bharti Airtel", "404", "10"},
	{"Vi (Vodafone Idea)", "404", "86"}, {"BSNL", "404", "34"},
}

func (FakeLineLookup) Lookup(_ context.Context, msisdn string, fields []string) (LineInfo, error) {
	if !IsE164(msisdn) {
		return LineInfo{LineType: "unknown"}, nil
	}
	last := msisdn[len(msisdn)-1]
	switch last {
	case '0':
		return LineInfo{LineType: "landline"}, nil
	case '1':
		return LineInfo{LineType: "voip"}, nil
	}
	want := func(field string) bool {
		if fields == nil {
			return true
		}
		for _, f := range fields {
			if f == field {
				return true
			}
		}
		return false
	}
	info := LineInfo{LineType: "mobile"}
	if last == '9' {
		info.LineType = "unknown"
	}
	digit, _ := strconv.Atoi(string(msisdn[len(msisdn)-2]))
	carrier := fakeCarriers[digit%len(fakeCarriers)]
	if want("carrier") {
		info.Carrier, info.MCC, info.MNC = &carrier.name, &carrier.mcc, &carrier.mnc
	}
	if want("ported") {
		ported := last == '7'
		info.Ported = &ported
	}
	if want("reachable") {
		reachable := last != '9'
		info.Reachable = &reachable
	}
	return info, nil
}
