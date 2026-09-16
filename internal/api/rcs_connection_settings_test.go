package api_test

import (
	"testing"

	"github.com/saeedafri/sms-be/internal/api"
)

// What an account needs before it can send, per vendor. Checked before a row is
// written and again before it is enabled, so an account that cannot work is
// refused where someone is looking rather than at the first customer message.
func TestWhatEachRCSVendorNeedsBeforeItCanSend(t *testing.T) {
	airtel := map[string]string{"baseUrl": "https://iq.airtel.test", "customerId": "c", "subAccountId": "s"}
	for name, tc := range map[string]struct {
		vendor   string
		settings map[string]string
		secrets  api.RCSSecrets
		want     string
	}{
		"jio needs nothing but its defaults": {"jio", nil, api.RCSSecrets{}, ""},
		"jio takes assistants later":         {"jio", nil, api.RCSSecrets{Assistants: map[string]string{"a": "b"}}, ""},
		"a jio assistant needs a secret":     {"jio", nil, api.RCSSecrets{Assistants: map[string]string{"a": ""}}, "every jio assistant needs an id and a secret key"},
		"airtel needs its token":             {"airtel", airtel, api.RCSSecrets{}, "airtel needs its auth token"},
		"airtel is complete":                 {"airtel", airtel, api.RCSSecrets{AuthToken: "dGVzdA=="}, ""},
		"airtel needs its account ids":       {"airtel", map[string]string{"baseUrl": "https://iq.airtel.test"}, api.RCSSecrets{AuthToken: "x"}, "setting customerId is required for airtel"},
		"vi needs a client id":               {"vi", map[string]string{"baseUrl": "u", "tokenUrl": "t"}, api.RCSSecrets{ClientSecret: "s"}, "setting clientId is required for vi"},
		"google needs its key":               {"google", nil, api.RCSSecrets{}, "google needs its service account key"},
		"an unknown vendor is refused":       {"reliance", nil, api.RCSSecrets{}, `unknown RCS vendor "reliance": use airtel, vi, jio or google`},
	} {
		if got := api.RCSConnectionProblem(tc.vendor, tc.settings, tc.secrets); got != tc.want {
			t.Errorf("%s = %q, want %q", name, got, tc.want)
		}
	}
}
