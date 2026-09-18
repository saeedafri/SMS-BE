package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/auth"
	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// P1-5. With dev endpoints on, MFA enrolment handed EVERY account the published
// DevTOTPSecret, so anyone who knew it could pass a real customer's second
// factor. Only a fixture address may get it.
func TestARealAccountNeverGetsThePublishedMFASecret(t *testing.T) {
	t.Parallel()
	h := newHarness(t) // dev endpoints on, as on the live server today
	acct := h.newAccount("owner")
	if _, err := h.admin.Exec(context.Background(),
		`UPDATE users SET email = $2 WHERE id = $1`, acct.UserID, "real-"+uuid.NewString()+"@relaysms.in"); err != nil {
		t.Fatal(err)
	}

	var enrollment gen.MfaEnrollment
	h.do(http.MethodPost, "/v1/auth/mfa/enroll", acct.Token, nil).decode(t, &enrollment)
	if enrollment.Secret == "" {
		t.Fatal("no secret issued")
	}
	if enrollment.Secret == auth.DevTOTPSecret {
		t.Fatal("a real address was enrolled with the published dev TOTP secret")
	}
}

// The browser suite still needs the fixed secret for its fixture accounts.
func TestAFixtureAccountStillGetsTheDevMFASecret(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner") // user-…@example.test
	var enrollment gen.MfaEnrollment
	h.do(http.MethodPost, "/v1/auth/mfa/enroll", acct.Token, nil).decode(t, &enrollment)
	if enrollment.Secret != auth.DevTOTPSecret {
		t.Fatalf("fixture account secret = %q, want the dev secret", enrollment.Secret)
	}
}
