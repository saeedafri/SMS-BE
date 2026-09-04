package api_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// Every country in the picker was accepted, so signing up as GB or AE produced
// a real tenant with a real owner — permanently, because there is no
// tenant-delete endpoint.
//
// The litter is the smaller problem. That tenant can never send: a regime with
// no registration objects has nothing to approve a sender against, so the
// customer finds out after onboarding instead of before signing up.
func TestSignupRefusesACountryWeDoNotOperateIn(t *testing.T) {
	h := newHarness(t)

	for _, country := range []string{"GB", "AE"} {
		t.Run(country, func(t *testing.T) {
			response := h.do(http.MethodPost, "/v1/auth/signup", "", map[string]any{
				"fullName": "Probe Person", "orgName": "API PROBE " + country,
				"email":    fmt.Sprintf("probe-%s-%s@example.test", country, uuid.NewString()[:8]),
				"password": "test-password-123", "country": country,
			})
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("signup in %s = %d, want 422 — it created a permanent "+
					"tenant that can never send\n%s", country, response.Code, response.Body)
			}
		})
	}
}

func TestSignupStillWorksWhereWeDoOperate(t *testing.T) {
	h := newHarness(t)
	response := h.do(http.MethodPost, "/v1/auth/signup", "", map[string]any{
		"fullName": "Real Person", "orgName": "Real Org",
		"email":    "real-person-" + uuid.NewString()[:8] + "@example.test",
		"password": "test-password-123", "country": "IN",
	})
	if response.Code != http.StatusCreated && response.Code != http.StatusOK {
		t.Fatalf("signup in IN = %d, want a session\n%s", response.Code, response.Body)
	}
}
