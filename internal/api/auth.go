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

const (
	sessionLifetime = 24 * time.Hour
	minPasswordLen  = 8
)

// issueSession mints a token, stores its hash, and returns what the contract's
// AuthSession expects.
func (s *Server) issueSession(ctx context.Context, tenantID, userID uuid.UUID) (gen.AuthSession, error) {
	raw, hash, err := auth.NewToken()
	if err != nil {
		return gen.AuthSession{}, err
	}
	expiresAt := time.Now().Add(sessionLifetime)
	// Taken from the request rather than hardcoded, so the security screen can
	// show which device a session belongs to. Every row used to read
	// "Unknown · Unknown" with no address, which made the one thing that screen
	// exists for — spotting a session you do not recognise — impossible.
	client := clientInfoFrom(ctx)
	if _, err := store.CreateSession(ctx, s.DB, store.SessionRequest{
		TenantID: tenantID, UserID: userID, TokenHash: hash,
		Device: client.Device, Browser: client.Browser, IP: client.IP,
		ExpiresAt: expiresAt,
	}); err != nil {
		return gen.AuthSession{}, err
	}
	// Recorded here rather than in Login, because this is the single point every
	// path that mints a session goes through — password sign-in, completing an
	// MFA challenge, and signup. Recording it in Login alone would miss the
	// logins of exactly the accounts that took security seriously enough to
	// turn two-factor on.
	//
	// Logged and swallowed, never returned: a sign-in must not be refused
	// because its audit row could not be written.
	if err := store.RecordLogin(ctx, s.DB, tenantID, userID); err != nil {
		s.Logger.Warn("login not recorded in user activity",
			"tenant", tenantID, "error", err)
	}
	return gen.AuthSession{Token: raw, ExpiresAt: expiresAt}, nil
}

func (s *Server) Signup(ctx context.Context, request gen.SignupRequestObject) (gen.SignupResponseObject, error) {
	body := request.Body
	fullName := strings.TrimSpace(body.FullName)
	orgName := strings.TrimSpace(body.OrgName)
	email := strings.ToLower(strings.TrimSpace(string(body.Email)))

	switch {
	case fullName == "":
		return gen.Signup422JSONResponse(errorBody(codeValidation, "Full name is required.")), nil
	case orgName == "":
		return gen.Signup422JSONResponse(errorBody(codeValidation, "Organisation name is required.")), nil
	case !strings.Contains(email, "@"):
		return gen.Signup422JSONResponse(errorBody(codeValidation, "A valid email address is required.")), nil
	case len(body.Password) < minPasswordLen:
		return gen.Signup422JSONResponse(errorBody(codeValidation,
			"Password must be at least 8 characters.")), nil
	}

	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		return nil, err
	}
	tenantID, userID, err := store.CreateTenantWithOwner(ctx, s.DB, store.SignupRequest{
		FullName: fullName, Email: email, PasswordHash: hash,
		OrgName: orgName, Country: string(body.Country),
	})
	if errors.Is(err, store.ErrConflict) {
		return gen.Signup409JSONResponse(errorBody(codeConflict,
			"An account with that email address already exists.")), nil
	}
	if err != nil {
		return nil, err
	}

	// Email the verification link now, rather than waiting for the account to
	// ask for it.
	//
	// Signing up sent NOTHING before this: the only path that mailed a
	// verification link was the explicit /v1/auth/verify-email/resend, so a new
	// customer created an account, went to their inbox and found an empty one.
	// That reads as "your email is broken", and it was reported to us as
	// exactly that.
	//
	// Failure is logged, not returned. The account exists and the session is
	// already valid, so refusing the signup over an undelivered email would
	// throw away a working account — and the resend endpoint is still there.
	if err := s.sendVerificationEmail(ctx, userID, email); err != nil {
		s.Logger.Warn("verification email not sent at signup",
			"user", userID, "error", err)
	}

	session, err := s.issueSession(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	return gen.Signup200JSONResponse(session), nil
}

// Login answers with either a session or an MFA challenge, per the contract's
// discriminated union.
//
// Every failure path returns the identical 401 body, and the password is
// verified even when the email is unknown. Both matter: a different response,
// or a materially faster one, lets an attacker enumerate which addresses have
// accounts here.
func (s *Server) Login(ctx context.Context, request gen.LoginRequestObject) (gen.LoginResponseObject, error) {
	email := strings.ToLower(strings.TrimSpace(string(request.Body.Email)))
	unauthorized := gen.Login401JSONResponse(
		errorBody(codeUnauthenticated, "Incorrect email address or password."))

	credentials, err := store.FindCredentialsByEmail(ctx, s.DB, email)
	if errors.Is(err, store.ErrNotFound) {
		// Hash against a dummy value so an unknown address costs the same time
		// as a known one with a wrong password.
		auth.VerifyPassword(auth.DummyHash, request.Body.Password)
		return unauthorized, nil
	}
	if err != nil {
		return nil, err
	}
	if !auth.VerifyPassword(credentials.PasswordHash, request.Body.Password) {
		return unauthorized, nil
	}

	membership, err := store.FindMembership(ctx, s.DB, credentials.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return unauthorized, nil
	}
	if err != nil {
		return nil, err
	}

	var result gen.LoginResult

	if credentials.MFAEnabled {
		challenge, err := s.issueMfaChallenge(ctx, credentials.UserID)
		if err != nil {
			return nil, err
		}
		if err := result.FromLoginMfaChallengeResult(gen.LoginMfaChallengeResult{
			Kind: "mfa_challenge", Challenge: challenge,
		}); err != nil {
			return nil, err
		}
		return gen.Login200JSONResponse(result), nil
	}

	session, err := s.issueSession(ctx, membership.TenantID, credentials.UserID)
	if err != nil {
		return nil, err
	}
	if err := result.FromLoginSessionResult(gen.LoginSessionResult{
		Kind: "session", Session: session,
	}); err != nil {
		return nil, err
	}
	return gen.Login200JSONResponse(result), nil
}

// Logout revokes the caller's own session. It is idempotent: logging out
// without a session is a success, because the desired end state already holds.
func (s *Server) Logout(ctx context.Context, _ gen.LogoutRequestObject) (gen.LogoutResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.Logout204Response{}, nil
	}
	if err := store.RevokeSession(ctx, s.DB, identity, identity.SessionID); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return gen.Logout204Response{}, nil
}

func (s *Server) ListSessions(ctx context.Context, _ gen.ListSessionsRequestObject) (gen.ListSessionsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.ListSessions401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	rows, err := store.ListSessions(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.Session, 0, len(rows))
	for _, row := range rows {
		out = append(out, gen.Session{
			Id:           row.ID.String(),
			Device:       row.Device,
			Browser:      row.Browser,
			Location:     row.Location,
			IpAddress:    row.IPAddress,
			LastActiveAt: row.LastActiveAt,
			Current:      row.Current,
		})
	}
	return gen.ListSessions200JSONResponse(out), nil
}

func (s *Server) RevokeSession(ctx context.Context, request gen.RevokeSessionRequestObject) (gen.RevokeSessionResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.RevokeSession401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	sessionID, err := uuid.Parse(request.Id)
	if err != nil {
		return gen.RevokeSession404JSONResponse(
			errorBody(codeNotFound, "No such session.")), nil
	}
	err = store.RevokeSession(ctx, s.DB, identity, sessionID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.RevokeSession404JSONResponse(
			errorBody(codeNotFound, "No such session.")), nil
	}
	if err != nil {
		return nil, err
	}
	s.recordActivity(ctx, identity, store.ActivitySessionRevoke,
		"Signed out another device")
	return gen.RevokeSession204Response{}, nil
}
