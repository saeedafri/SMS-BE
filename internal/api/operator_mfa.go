package api

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/auth"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// Second factor for platform staff.
//
// An operator account is not scoped to a tenant — the whole point of the role is
// that it sees every customer — so it is the most valuable credential in the
// deployment, and until now it was a password and nothing else. Customers have
// had TOTP since Stage 1. Staff, whose seeded password was a constant in this
// repository, had less protection than the people they serve.
//
// The flow mirrors the tenant one deliberately, down to the wording, but shares
// none of its storage: separate tables, separate challenge tokens, separate
// sessions. A shared challenge row is one missing branch away from turning a
// customer's second factor into an operator session.

// issueOperatorMfaChallenge records a pending second-factor step. No session
// exists until the code is verified.
func (s *Server) issueOperatorMfaChallenge(ctx context.Context, operatorID uuid.UUID) (
	gen.MfaChallenge, error) {

	raw, hash, err := auth.NewToken()
	if err != nil {
		return gen.MfaChallenge{}, err
	}
	expiresAt := time.Now().Add(mfaChallengeLifetime)
	if err := store.CreateOperatorMfaChallenge(ctx, s.DB, hash, operatorID, expiresAt); err != nil {
		return gen.MfaChallenge{}, err
	}
	return gen.MfaChallenge{
		ChallengeToken: raw,
		Methods:        []gen.MfaChallengeMethods{"totp", "recovery_code"},
		ExpiresAt:      expiresAt,
	}, nil
}

func (s *Server) OperatorVerifyMfa(ctx context.Context, request gen.OperatorVerifyMfaRequestObject) (
	gen.OperatorVerifyMfaResponseObject, error) {

	unauthorized := gen.OperatorVerifyMfa401JSONResponse(
		errorBody(codeUnauthenticated, "That code is not valid."))

	token := strings.TrimSpace(request.Body.ChallengeToken)
	if token == "" {
		return unauthorized, nil
	}
	operatorID, err := store.ConsumeOperatorMfaChallenge(ctx, s.DB, auth.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		// A spent or expired challenge is reported the same as a wrong code.
		// The tenant flow distinguishes them with a 410 because its UI sends the
		// user back to the form; the operator console has one screen and no such
		// branch, and saying less to an unauthenticated caller costs nothing.
		return unauthorized, nil
	}
	if err != nil {
		return nil, err
	}

	state, err := store.LoadOperatorForMfa(ctx, s.DB, operatorID)
	if err != nil {
		return nil, err
	}

	verified := false
	switch request.Body.Method {
	case "recovery_code":
		codeHash := auth.HashToken(auth.NormaliseRecoveryCode(request.Body.Code))
		err := store.ConsumeOperatorRecoveryCode(ctx, s.DB, operatorID, codeHash)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		verified = err == nil
	default:
		verified = auth.VerifyTOTPWithDevBypass(state.Secret, request.Body.Code, s.EnableDevEndpoints)
	}
	if !verified {
		return unauthorized, nil
	}

	session, err := s.issueOperatorSession(ctx, operatorID)
	if err != nil {
		return nil, err
	}
	return gen.OperatorVerifyMfa200JSONResponse(session), nil
}

func (s *Server) OperatorEnrollMfa(ctx context.Context, _ gen.OperatorEnrollMfaRequestObject) (
	gen.OperatorEnrollMfaResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.OperatorEnrollMfa401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	secret, otpauthURI, err := auth.NewTOTPSecret(operator.Email, s.EnableDevEndpoints)
	if err != nil {
		return nil, err
	}
	qrSVG, err := auth.QRCodeSVG(otpauthURI)
	if err != nil {
		return nil, err
	}
	// Staged, not enabled. An operator who opens this screen and walks away must
	// still be able to sign in — and an operator locked out of the console is an
	// incident nobody can fix from the console.
	if err := store.StageOperatorMfaSecret(ctx, s.DB, operator.OperatorID, secret); err != nil {
		return nil, err
	}
	return gen.OperatorEnrollMfa200JSONResponse(gen.MfaEnrollment{
		Secret: secret, OtpauthUri: otpauthURI, QrSvg: qrSVG,
	}), nil
}

func (s *Server) OperatorConfirmMfa(ctx context.Context, request gen.OperatorConfirmMfaRequestObject) (
	gen.OperatorConfirmMfaResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.OperatorConfirmMfa401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	state, err := store.LoadOperatorForMfa(ctx, s.DB, operator.OperatorID)
	if err != nil {
		return nil, err
	}
	if state.Enabled {
		return gen.OperatorConfirmMfa409JSONResponse(
			errorBody(codeConflict, "Two-factor authentication is already enabled.")), nil
	}
	if state.Secret == "" {
		return gen.OperatorConfirmMfa409JSONResponse(
			errorBody(codeConflict, "Start enrolment before confirming it.")), nil
	}
	if !auth.VerifyTOTPWithDevBypass(state.Secret, request.Body.Code, s.EnableDevEndpoints) {
		return gen.OperatorConfirmMfa401JSONResponse(
			errorBody(codeUnauthenticated, "That code is not valid. Try the current one.")), nil
	}
	codes, hashes, err := auth.NewRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if err := store.EnableOperatorMfa(ctx, s.DB, operator.OperatorID, hashes); err != nil {
		return nil, err
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, "operator.mfa_enabled",
		nil, "", operator.OperatorID.String(), ""); err != nil {
		return nil, err
	}
	// Shown once and never again — only their hashes are stored.
	return gen.OperatorConfirmMfa200JSONResponse(gen.MfaRecoveryCodes{RecoveryCodes: codes}), nil
}

func (s *Server) OperatorDisableMfa(ctx context.Context, request gen.OperatorDisableMfaRequestObject) (
	gen.OperatorDisableMfaResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.OperatorDisableMfa401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	state, err := store.LoadOperatorForMfa(ctx, s.DB, operator.OperatorID)
	if err != nil {
		return nil, err
	}
	if !state.Enabled {
		return gen.OperatorDisableMfa409JSONResponse(
			errorBody(codeConflict, "Two-factor authentication is not enabled.")), nil
	}
	// A current code is required. Turning MFA off is exactly what an attacker
	// holding a stolen session would do first, and this session sees every
	// customer on the platform.
	if !auth.VerifyTOTPWithDevBypass(state.Secret, request.Body.Code, s.EnableDevEndpoints) {
		return gen.OperatorDisableMfa401JSONResponse(
			errorBody(codeUnauthenticated, "That code is not valid.")), nil
	}
	if err := store.DisableOperatorMfa(ctx, s.DB, operator.OperatorID); err != nil {
		return nil, err
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, "operator.mfa_disabled",
		nil, "", operator.OperatorID.String(), ""); err != nil {
		return nil, err
	}
	return gen.OperatorDisableMfa204Response{}, nil
}
