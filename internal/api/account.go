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

// deliverToken stands in for sending an email; real delivery is wired up in a
// later stage. Until then the token goes to the log, which is enough to
// complete the flow locally.
//
// It is deliberately NOT returned in the response, even behind a development
// flag. These endpoints answer 204 by contract, and a reset token in an API
// response would let anyone who knows an email address take the account over —
// exactly the attack this flow exists to prevent. A flag that dangerous is not
// worth the local convenience when a log line does the same job.
func (s *Server) deliverToken(kind, email, token string) {
	if s.Logger != nil {
		s.Logger.Info("account token issued", "kind", kind, "email", email, "token", token)
	}
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
		return gen.ChangePassword403JSONResponse(
			errorBody(codeForbidden, "Your current password is incorrect.")), nil
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
	secret, otpauthURI, err := auth.NewTOTPSecret(identity.Email)
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
	if !auth.VerifyTOTP(credentials.MFASecret, request.Body.Code) {
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
	if !auth.VerifyTOTP(credentials.MFASecret, request.Body.Code) {
		return gen.DisableMfa401JSONResponse(
			errorBody(codeUnauthenticated, "That code is not valid.")), nil
	}
	if err := store.DisableMfa(ctx, s.DB, identity.UserID); err != nil {
		return nil, err
	}
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
		verified = auth.VerifyTOTP(credentials.MFASecret, request.Body.Code)
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
