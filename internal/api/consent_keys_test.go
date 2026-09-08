package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// An opt-in recorded under a key that is not a channel is refused, not stored.
//
// Stored verbatim it produces a contact who explicitly said yes and can never
// be reached: the audience rule asks for `consent ->> 'SMS'`, so a key of any
// other spelling matches nothing, for the life of the record, in silence. The
// campaign quotes 0 recipients, sends 0, and reports itself sent — there is no
// error at any point and nothing on any screen to see. Measured on the demo
// tenant before this was written: 2,560 of 2,564 contacts held an opt-in under
// the key "sms" and not one of them was reachable.
//
// The mixed case is the one worth asserting: a body that names four real
// channels and one bad one must be refused whole, or the caller is told nothing
// went wrong while a fifth of what it sent was silently unusable.
func TestAnUnknownConsentKeyIsRefusedRatherThanStored(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	refused := map[string]map[string]string{
		"lowercase, the spelling that reached production": {"sms": "opted_in"},
		"a channel we do not have":                        {"TELEGRAM": "opted_in"},
		"one bad key among good ones": {
			"SMS": "opted_in", "RCS": "opted_in", "sms": "opted_in",
		},
	}
	for name, basis := range refused {
		t.Run(name, func(t *testing.T) {
			res := h.do(http.MethodPost, "/v1/contacts/import", acct.Token, map[string]any{
				"newListName":    "consent " + name,
				"defaultCountry": "IN",
				"consentBasis":   basis,
				"rows":           []map[string]any{{"msisdn": "+919876500900"}},
			})
			if res.Code != http.StatusUnprocessableEntity {
				t.Fatalf("= %d, want 422 (body %s)", res.Code, res.Body)
			}
			// The message must name the offending key. A 422 alone proves
			// nothing here: this handler refuses several other things first,
			// and the first draft of this test was green against the COUNTRY
			// guard rather than the consent one.
			if code := res.errorCode(t); code != "validation_failed" {
				t.Errorf("error code = %q, want validation_failed", code)
			}
			if !strings.Contains(string(res.Body), "is not a channel") {
				t.Errorf("refused for the wrong reason: %s", res.Body)
			}
		})
	}

	// And the enum itself still imports, or the guard would be a denial of the
	// feature rather than of the bug.
	res := h.do(http.MethodPost, "/v1/contacts/import", acct.Token, map[string]any{
		"newListName":    "consent accepted",
		"defaultCountry": "IN",
		"consentBasis": map[string]string{
			"SMS": "opted_in", "RCS": "opted_in", "WHATSAPP": "opted_in",
			"EMAIL": "opted_in", "VOICE": "opted_in",
		},
		"rows": []map[string]any{{"msisdn": "+919876500901"}},
	})
	if res.Code != http.StatusOK && res.Code != http.StatusCreated {
		t.Fatalf("every channel in the enum = %d, want a success (body %s)", res.Code, res.Body)
	}

	// Stored under the key the audience rule actually asks for.
	var consent string
	if err := h.admin.QueryRow(context.Background(),
		`SELECT consent::text FROM contacts WHERE tenant_id = $1 AND msisdn = $2`,
		acct.TenantID, "+919876500901").Scan(&consent); err != nil {
		t.Fatalf("read back consent: %v", err)
	}
	var stored map[string]string
	if err := json.Unmarshal([]byte(consent), &stored); err != nil {
		t.Fatalf("decode consent: %v", err)
	}
	if stored["SMS"] != "opted_in" {
		t.Errorf("stored consent = %v, want SMS opted_in under the exact channel key", stored)
	}
}
