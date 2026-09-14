package api_test

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"testing"
)

// P2-3. Contact.phoneSuppressed and emailSuppressed were declared and never set,
// so the audience screen could not show who has opted out.
func TestAContactShowsWhetherItsPhoneAndEmailAreSuppressed(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	ctx := context.Background()
	suffix := rand.Intn(100000)
	optedOut := fmt.Sprintf("+9198765%05d", suffix)
	reachable := fmt.Sprintf("+9198764%05d", suffix)
	email := fmt.Sprintf("bounced-%d@example.test", suffix)
	for _, row := range [][2]any{{optedOut, email}, {reachable, nil}} {
		if _, err := h.admin.Exec(ctx, `INSERT INTO contacts (tenant_id, msisdn, email, country)
			VALUES ($1, $2, $3, 'IN')`, acct.TenantID, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	if res := h.do(http.MethodPost, "/v1/suppressions", acct.Token, map[string]any{
		"msisdns": []string{optedOut}, "emails": []string{email}, "reason": "manual",
	}); res.Code >= 300 {
		t.Fatalf("suppress = %d %s", res.Code, res.Body)
	}

	var page struct {
		Contacts []struct {
			Msisdn          string `json:"msisdn"`
			PhoneSuppressed *bool  `json:"phoneSuppressed"`
			EmailSuppressed *bool  `json:"emailSuppressed"`
		} `json:"contacts"`
	}
	h.do(http.MethodGet, "/v1/contacts", acct.Token, nil).decode(t, &page)
	seen := 0
	for _, c := range page.Contacts {
		switch c.Msisdn {
		case optedOut:
			seen++
			if c.PhoneSuppressed == nil || !*c.PhoneSuppressed || c.EmailSuppressed == nil || !*c.EmailSuppressed {
				t.Errorf("suppressed contact reads phone=%v email=%v, want true and true", c.PhoneSuppressed, c.EmailSuppressed)
			}
		case reachable:
			seen++
			if c.PhoneSuppressed == nil || *c.PhoneSuppressed {
				t.Errorf("reachable contact reads phoneSuppressed=%v, want false", c.PhoneSuppressed)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("found %d of the 2 contacts", seen)
	}
}
