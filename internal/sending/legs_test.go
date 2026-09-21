package sending

import (
	"testing"

	"github.com/saeedafri/sms-be/internal/store"
)

// chooseLeg is the one decision the fan-out and the estimate both ask, so its
// rules are pinned here without a database.
func TestChooseLeg(t *testing.T) {
	card := `{"kind":"card","card":{"title":"Meet {{product}}"}}`
	rcs := legSpec{channel: "RCS", template: store.Template{RCSContent: []byte(card)}}
	plain := "We have something new at Acme."
	sms := &legSpec{channel: "SMS", template: store.Template{Body: &plain}}
	named := "Hi {{first_name}}"
	smsNeedsName := &legSpec{channel: "SMS", template: store.Template{Body: &named}}

	both := map[string]string{"RCS": "opted_in", "SMS": "opted_in"}
	rcsOnly := map[string]string{"RCS": "opted_in"}
	withProduct := map[string]string{"product": "Widget"}

	contact := func(fields, consent map[string]string) store.Contact {
		return store.Contact{Msisdn: "+919800000001", Fields: fields, Consent: consent}
	}

	cases := []struct {
		name      string
		fallback  *legSpec
		contact   store.Contact
		reachable bool
		want      legChoice
	}{
		{"T1 fillable and reachable goes by the primary", sms,
			contact(withProduct, both), true, legChoice{}},
		{"T2 unfillable primary falls back", sms,
			contact(nil, both), true, legChoice{onFallback: true, reason: reasonUnfillable}},
		{"T3 an unreachable handset falls back", sms,
			contact(withProduct, both), false, legChoice{onFallback: true, reason: reasonNotReachable}},
		{"T4 neither leg fillable is skipped under the primary's slot", smsNeedsName,
			contact(nil, both), true,
			legChoice{skipped: true, reason: reasonUnfillable, missing: []string{"product"}}},
		{"T5 no fallback configured is skipped exactly as before", nil,
			contact(nil, both), true,
			legChoice{skipped: true, reason: reasonUnfillable, missing: []string{"product"}}},
		{"T6 no consent on the fallback channel is skipped, never sent", sms,
			contact(nil, rcsOnly), true,
			legChoice{skipped: true, reason: reasonUnfillable, missing: []string{"product"}}},
		{"an unreachable handset with no usable fallback still gets the primary", nil,
			contact(withProduct, both), false, legChoice{}},
		{"T7 a suppressed contact is excluded from every leg", sms,
			store.Contact{Msisdn: "+919800000001", Consent: both, PhoneSuppressed: true}, true,
			legChoice{excluded: true}},
		{"consent only on the fallback channel is outside the audience", sms,
			contact(withProduct, map[string]string{"SMS": "opted_in"}), true,
			legChoice{excluded: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chooseLeg(rcs, tc.fallback, tc.contact, tc.reachable)
			if got.onFallback != tc.want.onFallback || got.skipped != tc.want.skipped ||
				got.excluded != tc.want.excluded ||
				got.reason != tc.want.reason || len(got.missing) != len(tc.want.missing) {
				t.Fatalf("chooseLeg = %+v, want %+v", got, tc.want)
			}
		})
	}
}
