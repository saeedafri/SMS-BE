package api

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/saeedafri/sms-be/internal/domain/auth"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

const (
	emailVerificationLifetime = 24 * time.Hour
	passwordResetLifetime     = time.Hour
)

// deliverToken emails the link, and logs the token either way.
//
// The token is deliberately NOT returned in the response, even behind a
// development flag. These endpoints answer 204 by contract, and a reset token
// in an API response would let anyone who knows an email address take the
// account over — exactly the attack this flow exists to prevent. A flag that
// dangerous is not worth the local convenience when a log line does the same
// job, which is why the log line stays even now that mail really goes out.
//
// A send failure is logged, never returned. The token is already stored and
// valid, so the caller's request genuinely succeeded; failing it would tell
// somebody their password reset did not work while a working link sat in the
// database. It would also turn a Resend outage into a broken sign-up flow.
//
// The context is deliberately NOT the request's. These run inline in handlers
// that answer 204 immediately, and on the password-reset path the handler
// returns the same 204 whether or not the address exists — so cancellation
// when the client hangs up would silently drop the mail.
func (s *Server) deliverToken(kind, email, token string) {
	if s.Logger != nil {
		s.Logger.Info("account token issued", "kind", kind, "email", email, "token", token)
	}
	subject, html, ok := accountEmail(kind, s.appBaseURL(), token)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 15*time.Second)
	defer cancel()
	if err := s.Mail.Send(ctx, email, subject, html); err != nil && s.Logger != nil {
		s.Logger.Error("account email failed", "kind", kind, "email", email, "error", err)
	}
}

// appBaseURL falls back to the frontend's development origin so a deployment
// that forgets to set it still produces a usable link locally rather than one
// beginning "/verify-email", which no mail client can follow.
func (s *Server) appBaseURL() string {
	if s.AppBaseURL != "" {
		return strings.TrimRight(s.AppBaseURL, "/")
	}
	return "http://localhost:3000"
}

func (s *Server) ResendVerificationEmail(ctx context.Context, _ gen.ResendVerificationEmailRequestObject) (gen.ResendVerificationEmailResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.ResendVerificationEmail401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	raw, hash, err := auth.NewToken()
	if err != nil {
		return nil, err
	}
	if s.EnableDevEndpoints {
		// Fixed token in dev, matching DEV_VERIFY_TOKEN in
		// ../SMS-UI/src/lib/auth/session-config.ts — the "Verify now (dev)"
		// link that stands in for the emailed one. Same reasoning and the same
		// gate as devPasswordResetToken above: still armed only by a real
		// resend, still stored hashed against the one user who asked for it.
		raw = devEmailVerificationToken
		hash = auth.HashToken(raw)
	}
	if err := store.CreateEmailVerification(ctx, s.DB, hash, identity.UserID,
		time.Now().Add(emailVerificationLifetime)); err != nil {
		return nil, err
	}
	s.deliverToken("email_verification", identity.Email, raw)
	return gen.ResendVerificationEmail204Response{}, nil
}

func (s *Server) ConfirmVerificationEmail(ctx context.Context, request gen.ConfirmVerificationEmailRequestObject) (gen.ConfirmVerificationEmailResponseObject, error) {
	token := strings.TrimSpace(request.Body.Token)
	if token == "" {
		return gen.ConfirmVerificationEmail422JSONResponse(
			errorBody(codeValidation, "A token is required.")), nil
	}
	err := store.ConsumeEmailVerification(ctx, s.DB, auth.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		// One response for expired, already-used and never-existed. Telling
		// them apart would confirm which tokens were once real.
		return gen.ConfirmVerificationEmail400JSONResponse(errorBody("invalid_token",
			"That link is invalid or has expired. Request a new one.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.ConfirmVerificationEmail204Response{}, nil
}

// devEmailVerificationToken is the email-verification token issued when dev
// endpoints are on. See devPasswordResetToken for the reasoning.
const devEmailVerificationToken = "dev-verify-token"

// devPasswordResetToken is the reset token issued when dev endpoints are on.
// It matches DEV_RESET_TOKEN in ../SMS-UI/src/lib/auth/session-config.ts, which
// is what the dev-only "Open reset link" shortcut on the forgot screen points
// at.
const devPasswordResetToken = "dev-reset-token"

// RequestPasswordReset always answers 204, whether or not the address exists.
// The contract's own description says so: revealing which addresses have
// accounts turns this form into an enumeration oracle.
func (s *Server) RequestPasswordReset(ctx context.Context, request gen.RequestPasswordResetRequestObject) (gen.RequestPasswordResetResponseObject, error) {
	email := strings.ToLower(strings.TrimSpace(string(request.Body.Email)))

	userID, err := store.FindUserIDByEmail(ctx, s.DB, email)
	if errors.Is(err, store.ErrNotFound) {
		return gen.RequestPasswordReset204Response{}, nil
	}
	if err != nil {
		return nil, err
	}
	raw, hash, err := auth.NewToken()
	if err != nil {
		return nil, err
	}
	if s.EnableDevEndpoints {
		// A fixed token when dev endpoints are on, so an automated test can
		// follow the reset link without reading an inbox it has no access to.
		//
		// This is the same narrow bargain as auth.DevTOTPSecret, and it is safe
		// for the same reason plus one more: the token is still ARMED only by a
		// genuine reset request, and still stored hashed against the one user
		// who made it. Knowing the string does nothing on its own — visiting
		// the reset page with it before anyone has asked for a reset fails
		// exactly as an unknown token does, which is itself a test we run.
		//
		// Unreachable unless ENABLE_DEV_ENDPOINTS is set, which defaults to off
		// and refuses to start on an unrecognised value.
		raw = devPasswordResetToken
		hash = auth.HashToken(raw)
	}
	if err := store.CreatePasswordReset(ctx, s.DB, hash, userID,
		time.Now().Add(passwordResetLifetime)); err != nil {
		return nil, err
	}
	s.deliverToken("password_reset", email, raw)
	return gen.RequestPasswordReset204Response{}, nil
}

func (s *Server) ResetPassword(ctx context.Context, request gen.ResetPasswordRequestObject) (gen.ResetPasswordResponseObject, error) {
	token := strings.TrimSpace(request.Body.Token)
	if token == "" {
		return gen.ResetPassword422JSONResponse(
			errorBody(codeValidation, "A token is required.")), nil
	}
	if len(request.Body.Password) < minPasswordLen {
		return gen.ResetPassword422JSONResponse(
			errorBody(codeValidation, "Password must be at least 8 characters.")), nil
	}
	hash, err := auth.HashPassword(request.Body.Password)
	if err != nil {
		return nil, err
	}
	err = store.ConsumePasswordReset(ctx, s.DB, auth.HashToken(token), hash)
	if errors.Is(err, store.ErrNotFound) {
		return gen.ResetPassword422JSONResponse(errorBody(codeValidation,
			"That reset link is invalid or has expired. Request a new one.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.ResetPassword204Response{}, nil
}

func (s *Server) ChangePassword(ctx context.Context, request gen.ChangePasswordRequestObject) (gen.ChangePasswordResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.ChangePassword401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if len(request.Body.NewPassword) < minPasswordLen {
		return gen.ChangePassword422JSONResponse(
			errorBody(codeValidation, "Password must be at least 8 characters.")), nil
	}
	credentials, err := store.LoadUserForMfa(ctx, s.DB, identity.UserID)
	if err != nil {
		return nil, err
	}
	// Requiring the current password is what stops a stolen session from
	// becoming permanent account takeover.
	if !auth.VerifyPassword(credentials.PasswordHash, request.Body.CurrentPassword) {
		// 401, not 403. The contract assigns each a distinct meaning here —
		// 401 "current password is incorrect", 403 "member role has no access
		// to settings" — and the screen renders them differently: 401 marks the
		// current-password field, 403 is a whole-form refusal. Answering 403
		// for a wrong password put a permissions error on a screen the user has
		// every right to be on, and left the field they actually mistyped
		// unmarked.
		return gen.ChangePassword401JSONResponse(
			errorBody(codeUnauthenticated, "Your current password is incorrect.")), nil
	}
	hash, err := auth.HashPassword(request.Body.NewPassword)
	if err != nil {
		return nil, err
	}
	if err := store.SetPassword(ctx, s.DB, identity.UserID, hash); err != nil {
		return nil, err
	}
	return gen.ChangePassword204Response{}, nil
}

func (s *Server) EnrollMfa(ctx context.Context, _ gen.EnrollMfaRequestObject) (gen.EnrollMfaResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		// This operation declares only a 200 in the contract, so an
		// unauthenticated call surfaces through the generic error path rather
		// than a typed response.
		return nil, errUnauthenticated
	}
	secret, otpauthURI, err := auth.NewTOTPSecret(identity.Email, s.EnableDevEndpoints)
	if err != nil {
		return nil, err
	}
	qrSVG, err := auth.QRCodeSVG(otpauthURI)
	if err != nil {
		return nil, err
	}
	// Staged, not enabled: MFA turns on only once a code proves the user
	// actually scanned this. Enabling here would lock out anyone who abandoned
	// the flow halfway.
	if err := store.StageMfaSecret(ctx, s.DB, identity.UserID, secret); err != nil {
		return nil, err
	}
	return gen.EnrollMfa200JSONResponse(gen.MfaEnrollment{
		Secret: secret, OtpauthUri: otpauthURI, QrSvg: qrSVG,
	}), nil
}

func (s *Server) ConfirmMfaEnrollment(ctx context.Context, request gen.ConfirmMfaEnrollmentRequestObject) (gen.ConfirmMfaEnrollmentResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.ConfirmMfaEnrollment401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	credentials, err := store.LoadUserForMfa(ctx, s.DB, identity.UserID)
	if err != nil {
		return nil, err
	}
	if credentials.MFAEnabled {
		return gen.ConfirmMfaEnrollment409JSONResponse(
			errorBody(codeConflict, "Two-factor authentication is already enabled.")), nil
	}
	if credentials.MFASecret == "" {
		return gen.ConfirmMfaEnrollment409JSONResponse(
			errorBody(codeConflict, "Start enrolment before confirming it.")), nil
	}
	if !auth.VerifyTOTPWithDevBypass(credentials.MFASecret, request.Body.Code, s.EnableDevEndpoints) {
		return gen.ConfirmMfaEnrollment401JSONResponse(
			errorBody(codeUnauthenticated, "That code is not valid. Try the current one.")), nil
	}
	codes, hashes, err := auth.NewRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if err := store.EnableMfa(ctx, s.DB, identity.UserID, hashes); err != nil {
		return nil, err
	}
	// Recorded on confirmation, not on EnrollMfa: enrolment is staged until a
	// code proves the user scanned it, and someone who opened the screen and
	// walked away has not turned anything on.
	s.recordActivity(ctx, identity, store.ActivityMFAEnroll,
		"Enabled two-factor authentication")
	// These are shown once and never again — only their hashes are stored.
	return gen.ConfirmMfaEnrollment200JSONResponse(gen.MfaRecoveryCodes{
		RecoveryCodes: codes,
	}), nil
}

func (s *Server) DisableMfa(ctx context.Context, request gen.DisableMfaRequestObject) (gen.DisableMfaResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.DisableMfa401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	credentials, err := store.LoadUserForMfa(ctx, s.DB, identity.UserID)
	if err != nil {
		return nil, err
	}
	if !credentials.MFAEnabled {
		return gen.DisableMfa204Response{}, nil
	}
	// Turning MFA off is exactly what an attacker holding a stolen session
	// would try first, so it costs a current code.
	if !auth.VerifyTOTPWithDevBypass(credentials.MFASecret, request.Body.Code, s.EnableDevEndpoints) {
		return gen.DisableMfa401JSONResponse(
			errorBody(codeUnauthenticated, "That code is not valid.")), nil
	}
	if err := store.DisableMfa(ctx, s.DB, identity.UserID); err != nil {
		return nil, err
	}
	s.recordActivity(ctx, identity, store.ActivityMFADisable,
		"Disabled two-factor authentication")
	return gen.DisableMfa204Response{}, nil
}

// VerifyMfaChallenge completes the login that Login deliberately withheld a
// session from.
func (s *Server) VerifyMfaChallenge(ctx context.Context, request gen.VerifyMfaChallengeRequestObject) (gen.VerifyMfaChallengeResponseObject, error) {
	challengeToken := strings.TrimSpace(request.Body.ChallengeToken)
	if challengeToken == "" {
		return gen.VerifyMfaChallenge401JSONResponse(
			errorBody(codeUnauthenticated, "That challenge is not valid.")), nil
	}

	userID, err := store.ConsumeMfaChallenge(ctx, s.DB, auth.HashToken(challengeToken))
	if errors.Is(err, store.ErrNotFound) {
		// 410 Gone rather than 401: the contract distinguishes an expired or
		// spent challenge from a wrong code, and the UI sends the user back to
		// the login form for the former.
		return gen.VerifyMfaChallenge410JSONResponse(errorBody("challenge_expired",
			"That sign-in attempt has expired. Start again.")), nil
	}
	if err != nil {
		return nil, err
	}

	credentials, err := store.LoadUserForMfa(ctx, s.DB, userID)
	if err != nil {
		return nil, err
	}

	verified := false
	switch request.Body.Method {
	case "recovery_code":
		codeHash := auth.HashToken(auth.NormaliseRecoveryCode(request.Body.Code))
		err := store.ConsumeRecoveryCode(ctx, s.DB, userID, codeHash)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		verified = err == nil
	default:
		verified = auth.VerifyTOTPWithDevBypass(credentials.MFASecret, request.Body.Code, s.EnableDevEndpoints)
	}
	if !verified {
		return gen.VerifyMfaChallenge401JSONResponse(
			errorBody(codeUnauthenticated, "That code is not valid.")), nil
	}

	membership, err := store.FindMembership(ctx, s.DB, userID)
	if err != nil {
		return nil, err
	}
	session, err := s.issueSession(ctx, membership.TenantID, userID)
	if err != nil {
		return nil, err
	}
	return gen.VerifyMfaChallenge200JSONResponse(session), nil
}
