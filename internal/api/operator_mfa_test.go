package api_test

import (
	"net/http"
	"testing"

	"github.com/saeedafri/sms-be/internal/domain/auth"
)

// An operator account sees every customer on the platform, and until this
// existed it was defended by a password alone — one that had been a constant in
// this repository. Customers have had a second factor since Stage 1.
func TestOperatorMfaGatesTheConsole(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()

	// Enrolment is staged, not enabled: an operator who opens this screen and
	// walks away must still be able to sign in.
	enroll := h.do(http.MethodPost, "/v1/operator/mfa/enroll", operator, nil)
	if enroll.Code != http.StatusOK {
		t.Fatalf("enroll = %d\n%s", enroll.Code, enroll.Body)
	}
	var enrolment struct {
		Secret     string `json:"secret"`
		OtpauthURI string `json:"otpauthUri"`
		QrSvg      string `json:"qrSvg"`
	}
	enroll.decode(t, &enrolment)
	if enrolment.Secret == "" || enrolment.QrSvg == "" {
		t.Fatal("enrolment returned nothing to scan")
	}
	if stillIn := h.do(http.MethodPost, "/v1/operator/login", "", map[string]any{
		"email": harnessOperatorEmail, "password": harnessOperatorPassword,
	}); stillIn.Code != http.StatusOK || kindOf(t, stillIn) != "session" {
		t.Fatalf("staged enrolment already gated the login: %d %s",
			stillIn.Code, stillIn.Body)
	}

	// The dev bypass code, the same one the tenant flow accepts under
	// ENABLE_DEV_ENDPOINTS. A real TOTP cannot be computed in a test without
	// reimplementing the algorithm the code under test uses.
	confirm := h.do(http.MethodPost, "/v1/operator/mfa/confirm", operator,
		map[string]any{"code": devTOTPCode})
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm = %d\n%s", confirm.Code, confirm.Body)
	}
	var codes struct {
		RecoveryCodes []string `json:"recoveryCodes"`
	}
	confirm.decode(t, &codes)
	if len(codes.RecoveryCodes) == 0 {
		t.Fatal("no recovery codes — an operator who loses their phone needs a database edit")
	}

	// The password alone is no longer a session.
	challenged := h.do(http.MethodPost, "/v1/operator/login", "", map[string]any{
		"email": harnessOperatorEmail, "password": harnessOperatorPassword,
	})
	if challenged.Code != http.StatusOK {
		t.Fatalf("login = %d\n%s", challenged.Code, challenged.Body)
	}
	if kind := kindOf(t, challenged); kind != "mfa_challenge" {
		t.Fatalf("login returned %q with MFA enabled — the password alone got in", kind)
	}
	var challenge struct {
		Challenge struct {
			ChallengeToken string `json:"challengeToken"`
		} `json:"challenge"`
	}
	challenged.decode(t, &challenge)
	if challenge.Challenge.ChallengeToken == "" {
		t.Fatal("no challenge token")
	}

	// A wrong code does not get in.
	wrong := h.do(http.MethodPost, "/v1/operator/login/mfa", "", map[string]any{
		"challengeToken": challenge.Challenge.ChallengeToken,
		"code":           "000000", "method": "totp",
	})
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong code = %d, want 401\n%s", wrong.Code, wrong.Body)
	}

	// And the challenge it just spent cannot be replayed with the right code.
	replayed := h.do(http.MethodPost, "/v1/operator/login/mfa", "", map[string]any{
		"challengeToken": challenge.Challenge.ChallengeToken,
		"code":           devTOTPCode, "method": "totp",
	})
	if replayed.Code != http.StatusUnauthorized {
		t.Fatalf("a spent challenge = %d, want 401\n%s", replayed.Code, replayed.Body)
	}

	// A fresh challenge with the right code does.
	fresh := h.do(http.MethodPost, "/v1/operator/login", "", map[string]any{
		"email": harnessOperatorEmail, "password": harnessOperatorPassword,
	})
	fresh.decode(t, &challenge)
	verified := h.do(http.MethodPost, "/v1/operator/login/mfa", "", map[string]any{
		"challengeToken": challenge.Challenge.ChallengeToken,
		"code":           devTOTPCode, "method": "totp",
	})
	if verified.Code != http.StatusOK {
		t.Fatalf("verify = %d\n%s", verified.Code, verified.Body)
	}
	var session struct {
		Token string `json:"token"`
	}
	verified.decode(t, &session)
	if session.Token == "" {
		t.Fatal("verification returned no session")
	}
	if me := h.do(http.MethodGet, "/v1/operator/me", session.Token, nil); me.Code != http.StatusOK {
		t.Fatalf("the session from the second factor does not work: %d\n%s", me.Code, me.Body)
	}

	// Turning it off needs a current code — that is the first thing an attacker
	// holding a stolen session would try.
	if noCode := h.do(http.MethodPost, "/v1/operator/mfa/disable", session.Token,
		map[string]any{"code": "000000"}); noCode.Code != http.StatusUnauthorized {
		t.Fatalf("disable without a valid code = %d, want 401\n%s", noCode.Code, noCode.Body)
	}
	if off := h.do(http.MethodPost, "/v1/operator/mfa/disable", session.Token,
		map[string]any{"code": devTOTPCode}); off.Code != http.StatusNoContent {
		t.Fatalf("disable = %d\n%s", off.Code, off.Body)
	}
	back := h.do(http.MethodPost, "/v1/operator/login", "", map[string]any{
		"email": harnessOperatorEmail, "password": harnessOperatorPassword,
	})
	if kind := kindOf(t, back); kind != "session" {
		t.Fatalf("login is %q after disabling MFA, want session", kind)
	}
}

// A recovery code is what an operator who has lost their phone uses, and it
// must work exactly once.
func TestAnOperatorRecoveryCodeWorksOnce(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	h.do(http.MethodPost, "/v1/operator/mfa/enroll", operator, nil)
	confirm := h.do(http.MethodPost, "/v1/operator/mfa/confirm", operator,
		map[string]any{"code": devTOTPCode})
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm = %d\n%s", confirm.Code, confirm.Body)
	}
	var codes struct {
		RecoveryCodes []string `json:"recoveryCodes"`
	}
	confirm.decode(t, &codes)
	t.Cleanup(func() {
		// Leave the fixture as it was found; every other operator spec signs in
		// with a password alone.
		session := h.operatorSessionWithRecovery(codes.RecoveryCodes[len(codes.RecoveryCodes)-1])
		h.do(http.MethodPost, "/v1/operator/mfa/disable", session,
			map[string]any{"code": devTOTPCode})
	})

	first := h.operatorLoginChallenge()
	used := h.do(http.MethodPost, "/v1/operator/login/mfa", "", map[string]any{
		"challengeToken": first, "code": codes.RecoveryCodes[0], "method": "recovery_code",
	})
	if used.Code != http.StatusOK {
		t.Fatalf("recovery code = %d\n%s", used.Code, used.Body)
	}

	second := h.operatorLoginChallenge()
	reused := h.do(http.MethodPost, "/v1/operator/login/mfa", "", map[string]any{
		"challengeToken": second, "code": codes.RecoveryCodes[0], "method": "recovery_code",
	})
	if reused.Code != http.StatusUnauthorized {
		t.Fatalf("the same recovery code worked twice: %d\n%s", reused.Code, reused.Body)
	}
}

// devTOTPCode is the code the dev bypass accepts for the fixed dev secret, and
// for nothing else — see auth.VerifyTOTPWithDevBypass. Enrolment under
// ENABLE_DEV_ENDPOINTS issues that secret, so this is how a test presents a
// valid code without reimplementing TOTP.
const devTOTPCode = auth.DevTOTPCode

// kindOf reads the discriminator off an operator login result.
func kindOf(t *testing.T, res response) string {
	t.Helper()
	var body struct {
		Kind string `json:"kind"`
	}
	res.decode(t, &body)
	return body.Kind
}

// operatorLoginChallenge signs in with the password and returns the challenge
// token the second factor must answer.
func (h *harness) operatorLoginChallenge() string {
	h.t.Helper()
	res := h.do(http.MethodPost, "/v1/operator/login", "", map[string]any{
		"email": harnessOperatorEmail, "password": harnessOperatorPassword,
	})
	var body struct {
		Challenge struct {
			ChallengeToken string `json:"challengeToken"`
		} `json:"challenge"`
	}
	res.decode(h.t, &body)
	if body.Challenge.ChallengeToken == "" {
		h.t.Fatalf("expected an MFA challenge, got %s", res.Body)
	}
	return body.Challenge.ChallengeToken
}

// operatorSessionWithRecovery completes a challenge with a recovery code and
// returns the session token.
func (h *harness) operatorSessionWithRecovery(code string) string {
	h.t.Helper()
	res := h.do(http.MethodPost, "/v1/operator/login/mfa", "", map[string]any{
		"challengeToken": h.operatorLoginChallenge(), "code": code, "method": "recovery_code",
	})
	var body struct {
		Token string `json:"token"`
	}
	res.decode(h.t, &body)
	return body.Token
}

// The shipped operator console predates the discriminated union: it casts the
// login body straight to AuthSession and reads .token. When the union landed
// without this mirror, that read produced undefined, the console stored an
// empty cookie, and every request after sign-in came back unauthenticated —
// staff were locked out of the console entirely and it looked like a password
// problem. Deleting either mirror below reproduces that outage.
func TestOperatorLoginStaysReadableByAFlatAuthSessionClient(t *testing.T) {
	h := newHarness(t)
	h.operatorToken() // seeds the account with MFA off

	res := h.do(http.MethodPost, "/v1/operator/login", "", map[string]any{
		"email": harnessOperatorEmail, "password": harnessOperatorPassword,
	})
	if res.Code != http.StatusOK {
		t.Fatalf("login = %d\n%s", res.Code, res.Body)
	}

	// Exactly what the console does: no knowledge of kind or session.
	var flat struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	res.decode(t, &flat)
	if flat.Token == "" || flat.ExpiresAt == "" {
		t.Fatalf("a flat AuthSession client reads nothing usable: %s", res.Body)
	}
	if me := h.do(http.MethodGet, "/v1/operator/me", flat.Token, nil); me.Code != http.StatusOK {
		t.Fatalf("the top-level token does not authenticate: %d\n%s", me.Code, me.Body)
	}

	// The mirror must not disagree with the value it mirrors.
	var union struct {
		Session struct {
			Token string `json:"token"`
		} `json:"session"`
	}
	res.decode(t, &union)
	if union.Session.Token != flat.Token {
		t.Fatalf("mirror disagrees with session.token: %q vs %q",
			flat.Token, union.Session.Token)
	}
}
