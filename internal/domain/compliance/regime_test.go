package compliance_test

import (
	"testing"

	"github.com/saeedafri/sms-be/internal/domain/compliance"
)

func TestIndiaHasItsThreeRegistrationObjects(t *testing.T) {
	regime, ok := compliance.For("IN")
	if !ok {
		t.Fatal("no regime registered for IN")
	}
	if regime.Stub() {
		t.Error("India is marked a stub; it is a reference regime")
	}

	var keys []string
	for _, object := range regime.RegistrationObjects() {
		keys = append(keys, object.Key)
	}
	want := []string{"pe_rtm_entity", "dlt_header", "dlt_template"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys = %v, want %v", keys, want)
		}
	}
}

// The US campaign cannot be filed before its brand exists. That ordering is
// data on the object, not a branch in a handler.
func TestUSCampaignDependsOnBrand(t *testing.T) {
	regime, ok := compliance.For("US")
	if !ok {
		t.Fatal("no regime registered for US")
	}
	object, ok := regime.Object("tcr_campaign")
	if !ok {
		t.Fatal("tcr_campaign is not registered for US")
	}
	if object.DependsOn != "tcr_brand" {
		t.Fatalf("tcr_campaign.DependsOn = %q, want tcr_brand", object.DependsOn)
	}
	if brand, ok := regime.Object("tcr_brand"); !ok || brand.DependsOn != "" {
		t.Fatal("tcr_brand should exist and depend on nothing")
	}
}

// GB and AE exist to prove the pattern generalises. They must be present and
// answerable, not missing — a missing regime and a stub regime produce
// different, correct errors.
func TestStubRegimesArePresentButHaveNoObjects(t *testing.T) {
	for _, country := range []string{"GB", "AE"} {
		regime, ok := compliance.For(country)
		if !ok {
			t.Fatalf("no regime registered for %s", country)
		}
		if !regime.Stub() {
			t.Errorf("%s should be a stub", country)
		}
		if len(regime.RegistrationObjects()) != 0 {
			t.Errorf("%s is a stub but declares registration objects", country)
		}
	}
}

func TestUnknownCountryHasNoRegime(t *testing.T) {
	if _, ok := compliance.For("ZZ"); ok {
		t.Fatal("ZZ should not resolve to a regime")
	}
	if _, ok := compliance.For(""); ok {
		t.Fatal("an empty country should not resolve to a regime")
	}
}

func TestEveryRegistrationObjectDeclaresUsableFields(t *testing.T) {
	for _, country := range []string{"IN", "US", "GB", "AE"} {
		regime, _ := compliance.For(country)
		for _, object := range regime.RegistrationObjects() {
			if object.Label == "" || object.Tier == "" || object.Remediation == "" {
				t.Errorf("%s/%s is missing label, tier or remediation", country, object.Key)
			}
			if len(object.Fields) == 0 {
				t.Errorf("%s/%s declares no fields", country, object.Key)
			}
			for _, field := range object.Fields {
				if field.Key == "" || field.Label == "" || field.Type == "" {
					t.Errorf("%s/%s has an incomplete field: %+v", country, object.Key, field)
				}
				if field.Type == "select" && len(field.Options) == 0 {
					t.Errorf("%s/%s field %s is a select with no options",
						country, object.Key, field.Key)
				}
			}
		}
	}
}

// India's DLT rules have disallowed public URL shorteners since Oct 2024: the
// full URL has to be CTA-whitelisted. This is the authoritative check — the
// frontend validates too, but a client-side rule is a hint, not a control.
func TestIndiaRejectsShortenedCtaUrls(t *testing.T) {
	regime, _ := compliance.For("IN")

	rejected := []string{
		"https://bit.ly/abc",
		"https://www.bit.ly/abc",
		"http://tinyurl.com/xyz",
		"https://t.co/abc",
		"https://goo.gl/abc",
		"https://ow.ly/abc",
		"https://links.bit.ly/abc",
		"not-a-url",
		"",
	}
	for _, url := range rejected {
		if result := regime.ValidateCtaURL(url); result.OK {
			t.Errorf("India accepted %q, want rejected", url)
		} else if result.Reason == "" {
			t.Errorf("India rejected %q with no reason", url)
		}
	}

	accepted := []string{
		"https://acme.example/offer",
		"https://shop.acme.example/sale?utm=sms",
	}
	for _, url := range accepted {
		if result := regime.ValidateCtaURL(url); !result.OK {
			t.Errorf("India rejected %q (%s), want accepted", url, result.Reason)
		}
	}
}

// 10DLC carries no shortener rule, so the US regime must not inherit India's.
// If it did, the registry would be branching on a shared default rather than
// letting each country own its rules.
func TestUSAllowsShortenersButStillRequiresAValidURL(t *testing.T) {
	regime, _ := compliance.For("US")

	if result := regime.ValidateCtaURL("https://bit.ly/abc"); !result.OK {
		t.Errorf("US rejected a shortener (%s); 10DLC has no such rule", result.Reason)
	}
	if result := regime.ValidateCtaURL("still-not-a-url"); result.OK {
		t.Error("US accepted a malformed URL")
	}
}

func TestObjectLookupFailsForAnUnknownKey(t *testing.T) {
	regime, _ := compliance.For("IN")
	if _, ok := regime.Object("not_a_real_object"); ok {
		t.Fatal("an unknown object key resolved")
	}
}

// A required field missing from the submission has to be reported by name so
// the UI can point at the right input.
func TestValidateFieldsNamesEveryMissingRequiredField(t *testing.T) {
	regime, _ := compliance.For("IN")
	object, _ := regime.Object("pe_rtm_entity")

	missing := compliance.MissingRequired(object, map[string]any{
		"legalName": "Acme Pvt Ltd",
		"pan":       "   ", // whitespace is not a value
	})
	if len(missing) == 0 {
		t.Fatal("no missing fields reported for an incomplete submission")
	}
	for _, key := range []string{"pan", "entityType", "contactEmail"} {
		if !contains(missing, key) {
			t.Errorf("missing = %v, expected it to include %q", missing, key)
		}
	}
	if contains(missing, "legalName") {
		t.Errorf("missing = %v, should not include the supplied legalName", missing)
	}

	complete := compliance.MissingRequired(object, map[string]any{
		"legalName": "Acme Pvt Ltd", "pan": "ABCDE1234F",
		"entityType": "private_ltd", "contactEmail": "compliance@acme.example",
	})
	if len(complete) != 0 {
		t.Fatalf("complete submission reported missing: %v", complete)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
