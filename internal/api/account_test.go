package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/saeedafri/sms-be/internal/domain/auth"
	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func currentTOTP(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret, time.Now().UTC(), totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: 6,
	})
	if err != nil {
		t.Fatalf("generate totp: %v", err)
	}
	return code
}

func TestEmailVerificationRoundTrip(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	h.setEmailVerified(acct.UserID, false)

	if res := h.do(http.MethodPost, "/v1/auth/verify-email/resend", acct.Token, nil); res.Code != http.StatusNoContent {
		t.Fatalf("resend: status = %d, want 204; body = %s", res.Code, res.Body)
	}
	token := h.lastIssuedToken(t, "email_verification")

	res := h.do(http.MethodPost, "/v1/auth/verify-email/confirm", "",
		map[string]string{"token": token})
	if res.Code != http.StatusNoContent {
		t.Fatalf("confirm: status = %d, want 204; body = %s", res.Code, res.Body)
	}

	me := h.do(http.MethodGet, "/v1/me", acct.Token, nil)
	var body gen.Me
	me.decode(t, &body)
	if !body.EmailVerified {
		t.Fatal("emailVerified is still false after confirming")
	}
}

// A verification link must work once. Replaying it — from a mail client
// prefetching the URL, say — must not silently succeed again.
func TestEmailVerificationTokenIsSingleUse(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	h.setEmailVerified(acct.UserID, false)

	h.do(http.MethodPost, "/v1/auth/verify-email/resend", acct.Token, nil)
	token := h.lastIssuedToken(t, "email_verification")

	if res := h.do(http.MethodPost, "/v1/auth/verify-email/confirm", "",
		map[string]string{"token": token}); res.Code != http.StatusNoContent {
		t.Fatalf("first confirm: status = %d, want 204", res.Code)
	}
	res := h.do(http.MethodPost, "/v1/auth/verify-email/confirm", "",
		map[string]string{"token": token})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("second confirm: status = %d, want 400; body = %s", res.Code, res.Body)
	}
}

func TestEmailVerificationRejectsUnknownAndBlankTokens(t *testing.T) {
	h := newHarness(t)

	if res := h.do(http.MethodPost, "/v1/auth/verify-email/confirm", "",
		map[string]string{"token": ""}); res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("blank token: status = %d, want 422", res.Code)
	}
	if res := h.do(http.MethodPost, "/v1/auth/verify-email/confirm", "",
		map[string]string{"token": "not-a-real-token"}); res.Code != http.StatusBadRequest {
		t.Fatalf("unknown token: status = %d, want 400", res.Code)
	}
}

// The contract's own description calls this out: the response must not reveal
// whether the address has an account.
func TestForgotPasswordDoesNotRevealWhetherAnAccountExists(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	known := h.do(http.MethodPost, "/v1/auth/password/forgot", "",
		map[string]string{"email": acct.Email})
	unknown := h.do(http.MethodPost, "/v1/auth/password/forgot", "",
		map[string]string{"email": "definitely-nobody@example.test"})

	if known.Code != http.StatusNoContent || unknown.Code != http.StatusNoContent {
		t.Fatalf("statuses differ: known = %d, unknown = %d; both should be 204",
			known.Code, unknown.Code)
	}
	if string(known.Body) != string(unknown.Body) {
		t.Fatalf("bodies differ and so leak account existence: %q vs %q",
			known.Body, unknown.Body)
	}
}

// Someone resetting a password because they suspect compromise expects the
// attacker to be logged out. Leaving sessions alive would defeat the reset.
func TestPasswordResetRevokesEveryExistingSession(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	if res := h.do(http.MethodPost, "/v1/auth/password/forgot", "",
		map[string]string{"email": acct.Email}); res.Code != http.StatusNoContent {
		t.Fatalf("forgot: status = %d", res.Code)
	}
	token := h.lastIssuedToken(t, "password_reset")

	res := h.do(http.MethodPost, "/v1/auth/password/reset", "",
		map[string]string{"token": token, "password": "a-brand-new-password"})
	if res.Code != http.StatusNoContent {
		t.Fatalf("reset: status = %d, want 204; body = %s", res.Code, res.Body)
	}

	if check := h.do(http.MethodGet, "/v1/me", acct.Token, nil); check.Code != http.StatusUnauthorized {
		t.Fatalf("the pre-reset session still works: status = %d", check.Code)
	}

	login := h.do(http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email": acct.Email, "password": "a-brand-new-password",
	})
	if login.Code != http.StatusOK {
		t.Fatalf("login with the new password: status = %d; body = %s", login.Code, login.Body)
	}
	old := h.do(http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email": acct.Email, "password": "test-password-123",
	})
	if old.Code != http.StatusUnauthorized {
		t.Fatalf("the old password still works: status = %d", old.Code)
	}
}

func TestPasswordResetTokenIsSingleUse(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	h.do(http.MethodPost, "/v1/auth/password/forgot", "", map[string]string{"email": acct.Email})
	token := h.lastIssuedToken(t, "password_reset")

	if res := h.do(http.MethodPost, "/v1/auth/password/reset", "",
		map[string]string{"token": token, "password": "first-new-password"}); res.Code != http.StatusNoContent {
		t.Fatalf("first reset: status = %d", res.Code)
	}
	res := h.do(http.MethodPost, "/v1/auth/password/reset", "",
		map[string]string{"token": token, "password": "second-new-password"})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("second reset: status = %d, want 422", res.Code)
	}
}

func TestPasswordResetRejectsShortPasswords(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	h.do(http.MethodPost, "/v1/auth/password/forgot", "", map[string]string{"email": acct.Email})
	token := h.lastIssuedToken(t, "password_reset")

	res := h.do(http.MethodPost, "/v1/auth/password/reset", "",
		map[string]string{"token": token, "password": "short"})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.Code)
	}
}

// Requiring the current password is what stops a stolen session from becoming
// permanent account takeover.
func TestChangePasswordRequiresTheCurrentPassword(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	wrong := h.do(http.MethodPatch, "/v1/auth/password", acct.Token, map[string]string{
		"currentPassword": "not-the-current-password", "newPassword": "a-new-password-here",
	})
	if wrong.Code != http.StatusForbidden {
		t.Fatalf("wrong current password: status = %d, want 403; body = %s", wrong.Code, wrong.Body)
	}

	right := h.do(http.MethodPatch, "/v1/auth/password", acct.Token, map[string]string{
		"currentPassword": "test-password-123", "newPassword": "a-new-password-here",
	})
	if right.Code != http.StatusNoContent {
		t.Fatalf("correct current password: status = %d, want 204; body = %s", right.Code, right.Body)
	}

	login := h.do(http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email": acct.Email, "password": "a-new-password-here",
	})
	if login.Code != http.StatusOK {
		t.Fatalf("login with the changed password: status = %d", login.Code)
	}
}

func TestChangePasswordRejectsShortPasswordsAndAnonymousCallers(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	short := h.do(http.MethodPatch, "/v1/auth/password", acct.Token, map[string]string{
		"currentPassword": "test-password-123", "newPassword": "abc",
	})
	if short.Code != http.StatusUnprocessableEntity {
		t.Fatalf("short password: status = %d, want 422", short.Code)
	}
	anon := h.do(http.MethodPatch, "/v1/auth/password", "", map[string]string{
		"currentPassword": "x", "newPassword": "a-long-enough-password",
	})
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: status = %d, want 401", anon.Code)
	}
}

// Enrolment must not switch MFA on until a code proves the user really scanned
// the secret — otherwise abandoning the flow halfway locks them out.
func TestMfaEnrolmentDoesNotEnableUntilConfirmed(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPost, "/v1/auth/mfa/enroll", acct.Token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("enroll: status = %d, want 200; body = %s", res.Code, res.Body)
	}
	var enrollment gen.MfaEnrollment
	res.decode(t, &enrollment)
	if enrollment.Secret == "" || enrollment.OtpauthUri == "" || enrollment.QrSvg == "" {
		t.Fatalf("enrolment is missing required fields: %+v", enrollment)
	}
	if !strings.HasPrefix(enrollment.OtpauthUri, "otpauth://totp/") {
		t.Errorf("otpauthUri = %q, want an otpauth://totp/ URI", enrollment.OtpauthUri)
	}
	if !strings.Contains(enrollment.QrSvg, "<svg") {
		t.Errorf("qrSvg does not look like SVG: %.80s", enrollment.QrSvg)
	}

	me := h.do(http.MethodGet, "/v1/me", acct.Token, nil)
	var body gen.Me
	me.decode(t, &body)
	if body.MfaEnabled {
		t.Fatal("mfaEnabled is true before the enrolment was confirmed")
	}

	// A wrong code must not enable it either.
	bad := h.do(http.MethodPost, "/v1/auth/mfa/enroll/confirm", acct.Token,
		map[string]string{"code": "000000"})
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("wrong code: status = %d, want 401; body = %s", bad.Code, bad.Body)
	}

	good := h.do(http.MethodPost, "/v1/auth/mfa/enroll/confirm", acct.Token,
		map[string]string{"code": currentTOTP(t, enrollment.Secret)})
	if good.Code != http.StatusOK {
		t.Fatalf("correct code: status = %d, want 200; body = %s", good.Code, good.Body)
	}
	var codes gen.MfaRecoveryCodes
	good.decode(t, &codes)
	if len(codes.RecoveryCodes) == 0 {
		t.Fatal("no recovery codes returned")
	}

	me = h.do(http.MethodGet, "/v1/me", acct.Token, nil)
	me.decode(t, &body)
	if !body.MfaEnabled {
		t.Fatal("mfaEnabled is false after confirming enrolment")
	}
}

func TestMfaLoginIssuesAChallengeAndThenASession(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	secret := h.enableMfa(t, acct)

	login := h.do(http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email": acct.Email, "password": "test-password-123",
	})
	if login.Code != http.StatusOK {
		t.Fatalf("login: status = %d; body = %s", login.Code, login.Body)
	}
	var challengeResult struct {
		Kind      string `json:"kind"`
		Challenge struct {
			ChallengeToken string   `json:"challengeToken"`
			Methods        []string `json:"methods"`
		} `json:"challenge"`
		Session *struct{} `json:"session"`
	}
	login.decode(t, &challengeResult)

	if challengeResult.Kind != "mfa_challenge" {
		t.Fatalf("kind = %q, want mfa_challenge", challengeResult.Kind)
	}
	if challengeResult.Session != nil {
		t.Fatal("login issued a session despite MFA being enabled")
	}
	if challengeResult.Challenge.ChallengeToken == "" {
		t.Fatal("no challenge token issued")
	}

	// A wrong code must not complete the challenge.
	bad := h.do(http.MethodPost, "/v1/auth/mfa/challenge", "", map[string]string{
		"challengeToken": challengeResult.Challenge.ChallengeToken,
		"code":           "000000", "method": "totp",
	})
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("wrong code: status = %d, want 401; body = %s", bad.Code, bad.Body)
	}

	// The challenge is single-use, so a fresh login is needed after that miss.
	login = h.do(http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email": acct.Email, "password": "test-password-123",
	})
	login.decode(t, &challengeResult)

	good := h.do(http.MethodPost, "/v1/auth/mfa/challenge", "", map[string]string{
		"challengeToken": challengeResult.Challenge.ChallengeToken,
		"code":           currentTOTP(t, secret), "method": "totp",
	})
	if good.Code != http.StatusOK {
		t.Fatalf("correct code: status = %d, want 200; body = %s", good.Code, good.Body)
	}
	var session gen.AuthSession
	good.decode(t, &session)
	if session.Token == "" {
		t.Fatal("challenge verification returned no token")
	}
	if check := h.do(http.MethodGet, "/v1/me", session.Token, nil); check.Code != http.StatusOK {
		t.Fatalf("the issued token does not authenticate: status = %d", check.Code)
	}
}

// A spent challenge must be gone, not merely rejected — the contract uses 410
// so the UI knows to send the user back to the login form.
func TestMfaChallengeIsSingleUse(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	secret := h.enableMfa(t, acct)

	login := h.do(http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email": acct.Email, "password": "test-password-123",
	})
	var result struct {
		Challenge struct {
			ChallengeToken string `json:"challengeToken"`
		} `json:"challenge"`
	}
	login.decode(t, &result)

	first := h.do(http.MethodPost, "/v1/auth/mfa/challenge", "", map[string]string{
		"challengeToken": result.Challenge.ChallengeToken,
		"code":           currentTOTP(t, secret), "method": "totp",
	})
	if first.Code != http.StatusOK {
		t.Fatalf("first use: status = %d", first.Code)
	}
	second := h.do(http.MethodPost, "/v1/auth/mfa/challenge", "", map[string]string{
		"challengeToken": result.Challenge.ChallengeToken,
		"code":           currentTOTP(t, secret), "method": "totp",
	})
	if second.Code != http.StatusGone {
		t.Fatalf("second use: status = %d, want 410; body = %s", second.Code, second.Body)
	}
}

func TestRecoveryCodeWorksOnceAndOnlyOnce(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPost, "/v1/auth/mfa/enroll", acct.Token, nil)
	var enrollment gen.MfaEnrollment
	res.decode(t, &enrollment)
	confirm := h.do(http.MethodPost, "/v1/auth/mfa/enroll/confirm", acct.Token,
		map[string]string{"code": currentTOTP(t, enrollment.Secret)})
	var codes gen.MfaRecoveryCodes
	confirm.decode(t, &codes)
	recovery := codes.RecoveryCodes[0]

	challenge := func() string {
		t.Helper()
		login := h.do(http.MethodPost, "/v1/auth/login", "", map[string]string{
			"email": acct.Email, "password": "test-password-123",
		})
		var result struct {
			Challenge struct {
				ChallengeToken string `json:"challengeToken"`
			} `json:"challenge"`
		}
		login.decode(t, &result)
		return result.Challenge.ChallengeToken
	}

	first := h.do(http.MethodPost, "/v1/auth/mfa/challenge", "", map[string]string{
		"challengeToken": challenge(), "code": recovery, "method": "recovery_code",
	})
	if first.Code != http.StatusOK {
		t.Fatalf("first use of a recovery code: status = %d; body = %s", first.Code, first.Body)
	}
	second := h.do(http.MethodPost, "/v1/auth/mfa/challenge", "", map[string]string{
		"challengeToken": challenge(), "code": recovery, "method": "recovery_code",
	})
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("reusing a spent recovery code: status = %d, want 401", second.Code)
	}
}

// Disabling MFA is the first thing an attacker with a stolen session would
// try, so it costs a current code.
func TestDisableMfaRequiresAValidCode(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	secret := h.enableMfa(t, acct)

	bad := h.do(http.MethodPost, "/v1/auth/mfa/disable", acct.Token,
		map[string]string{"code": "000000"})
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("wrong code: status = %d, want 401; body = %s", bad.Code, bad.Body)
	}

	good := h.do(http.MethodPost, "/v1/auth/mfa/disable", acct.Token,
		map[string]string{"code": currentTOTP(t, secret)})
	if good.Code != http.StatusNoContent {
		t.Fatalf("correct code: status = %d, want 204; body = %s", good.Code, good.Body)
	}

	me := h.do(http.MethodGet, "/v1/me", acct.Token, nil)
	var body gen.Me
	me.decode(t, &body)
	if body.MfaEnabled {
		t.Fatal("mfaEnabled is still true after disabling")
	}

	// And login must go back to issuing a session directly.
	login := h.do(http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email": acct.Email, "password": "test-password-123",
	})
	var result struct {
		Kind string `json:"kind"`
	}
	login.decode(t, &result)
	if result.Kind != "session" {
		t.Fatalf("kind = %q after disabling MFA, want session", result.Kind)
	}
}

// The dev bypass must stay shut for a real secret even when dev mode is on.
// This is the guard that keeps a test-only convenience from becoming a way into
// any account on a developer's machine.
func TestDevTOTPBypassOnlyUnlocksTheDevSecret(t *testing.T) {
	real, _, err := auth.NewTOTPSecret("me@example.test", false)
	if err != nil {
		t.Fatalf("NewTOTPSecret: %v", err)
	}
	if auth.VerifyTOTPWithDevBypass(real, auth.DevTOTPCode, true) {
		t.Fatal("the dev code unlocked a real secret with dev mode on")
	}
	if auth.VerifyTOTPWithDevBypass(auth.DevTOTPSecret, auth.DevTOTPCode, false) {
		t.Fatal("the dev code was accepted with dev mode off")
	}
	if !auth.VerifyTOTPWithDevBypass(auth.DevTOTPSecret, auth.DevTOTPCode, true) {
		t.Fatal("the dev code was rejected for the dev secret with dev mode on")
	}
	// A real code for the dev secret still works — the bypass is additive.
	if !auth.VerifyTOTPWithDevBypass(auth.DevTOTPSecret, currentTOTP(t, auth.DevTOTPSecret), true) {
		t.Fatal("a genuine code for the dev secret was rejected")
	}
}

func TestTOTPRejectsCodesFromADifferentSecret(t *testing.T) {
	other, _, err := auth.NewTOTPSecret("someone@example.test", false)
	if err != nil {
		t.Fatalf("NewTOTPSecret: %v", err)
	}
	mine, _, err := auth.NewTOTPSecret("me@example.test", false)
	if err != nil {
		t.Fatalf("NewTOTPSecret: %v", err)
	}
	if auth.VerifyTOTP(mine, currentTOTP(t, other)) {
		t.Fatal("a code from a different secret was accepted")
	}
	if !auth.VerifyTOTP(mine, currentTOTP(t, mine)) {
		t.Fatal("a code from the matching secret was rejected")
	}
}
