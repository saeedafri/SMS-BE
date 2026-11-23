package failures

import "testing"

func text(s string) *string { return &s }

var rules = []Rule{
	{ID: "std", Channel: text("SMS"), Source: "smpp_submit", MatchKind: "exact", MatchValue: "0x00000058",
		Layer: "carrier", Code: "route_throttled", Title: "standard"},
	{ID: "vendor", Source: "rcs_vendor", Vendor: text("trustsignal"), MatchKind: "exact",
		MatchValue: "0x00000058", Layer: "provider", Code: "vendor_code", Title: "vendor"},
	{ID: "route", Source: "smpp_submit", RouteID: text("route-1"), MatchKind: "exact",
		MatchValue: "0x00000058", Layer: "ours", Code: "route_code", Title: "route"},
	{ID: "family", Channel: text("SMS"), Source: "smpp_receipt", MatchKind: "prefix",
		MatchValue: "UNDELIV", Layer: "carrier", Code: "carrier_undelivered", Title: "family"},
	{ID: "gate", Source: "gate", MatchKind: "exact", MatchValue: "sender_not_approved",
		Layer: "sender_setup", Code: "sender_not_approved", Title: "refusal"},
}

// One case per rung of the resolution order; removing a rung fails exactly
// its own case.
func TestResolutionOrder(t *testing.T) {
	cases := []struct {
		name string
		ctx  Context
		want string
	}{
		{"route beats vendor and standard", Context{Channel: "SMS", Source: "smpp_submit",
			Raw: "0x00000058", RouteID: "route-1", Vendor: "trustsignal"}, "route"},
		{"vendor beats standard", Context{Channel: "SMS", Source: "smpp_submit",
			Raw: "0x00000058", Vendor: "trustsignal"}, "vendor"},
		{"standard on channel and source", Context{Channel: "SMS", Source: "smpp_submit",
			Raw: "0x00000058"}, "standard"},
		{"prefix on the source family", Context{Channel: "SMS", Source: "smpp_receipt",
			Raw: "UNDELIV:001"}, "family"},
		{"a channel-neutral refusal", Context{Channel: "RCS", Source: "gate",
			Raw: "sender_not_approved"}, "refusal"},
	}
	for _, tc := range cases {
		if got := Resolve(rules, tc.ctx); got.Title != tc.want {
			t.Errorf("%s: resolved %q (%s), want %q", tc.name, got.Title, got.Code, tc.want)
		}
	}
}

func TestAnUnknownCodeIsUnmappedAndKeepsTheRawCode(t *testing.T) {
	got := Resolve(rules, Context{Channel: "SMS", Source: "smpp_submit", Raw: "0x000000FE"})
	if !got.Unmapped() || got.Code != "unmapped" || got.ProviderCode != "0x000000FE" || got.Title == "" {
		t.Fatalf("unknown code = %+v, want unmapped with the raw code and a title", got)
	}
	// The same code on another channel's source is not the standard's to answer.
	if got := Resolve(rules, Context{Channel: "RCS", Source: "rcs_vendor", Raw: "0x00000058"}); !got.Unmapped() {
		t.Fatalf("an SMS rule answered an RCS code: %+v", got)
	}
}

func TestOurOwnRefusalCarriesNoProviderCode(t *testing.T) {
	if got := Resolve(rules, Context{Channel: "SMS", Source: "gate", Raw: "sender_not_approved"}); got.ProviderCode != "" {
		t.Fatalf("refusal providerCode = %q, want none", got.ProviderCode)
	}
	if got := Resolve(rules, Context{Channel: "SMS", Source: "smpp_submit", Raw: "0x00000058"}); got.ProviderCode != "0x00000058" {
		t.Fatalf("carrier failure providerCode = %q, want the raw code", got.ProviderCode)
	}
}

func TestContextSaysWhichSourceAFailureCameFrom(t *testing.T) {
	cases := []struct{ channel, status, source string }{
		{"SMS", "rejected", "gate"},
		{"SMS", "carrier_rejected", "smpp_submit"},
		{"SMS", "undelivered", "smpp_receipt"},
		{"SMS", "expired", "smpp_receipt"},
		{"RCS", "carrier_rejected", "rcs_vendor"},
		{"WHATSAPP", "undelivered", "whatsapp"},
		{"EMAIL", "undelivered", "email"},
	}
	for _, tc := range cases {
		got, failed := ContextFor(tc.channel, tc.status, "X", "", "")
		if !failed || got.Source != tc.source {
			t.Errorf("%s %s = %q (%v), want %q", tc.channel, tc.status, got.Source, failed, tc.source)
		}
	}
	if _, failed := ContextFor("SMS", "delivered", "", "", ""); failed {
		t.Error("a delivered message was given a failure")
	}
}
