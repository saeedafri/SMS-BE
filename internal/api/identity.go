package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/saeedafri/sms-be/internal/domain/auth"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

type identityKey struct{}

// environmentKey carries the API key's environment, which decides its throttle.
type environmentKey struct{}

// keyEnvironment reports which environment the calling API key belongs to.
// Empty for a dashboard session.
func keyEnvironment(ctx context.Context) string {
	environment, _ := ctx.Value(environmentKey{}).(string)
	return environment
}

// scopesKey carries an API key's scopes. Absent for a dashboard session, which
// is authorised by role instead.
type scopesKey struct{}

// isAPIKey tells a minted key from a session token by its prefix, which
// store.generateSecret writes: sk_live_… or sk_test_….
func isAPIKey(token string) bool {
	return strings.HasPrefix(token, "sk_live_") || strings.HasPrefix(token, "sk_test_")
}

// scopesFrom returns the scopes of the API key that authenticated this request,
// and whether the caller is a key at all. A dashboard session returns false, and
// callers must decide for themselves whether a session may do the thing — a
// session is not "a key with every scope".
func scopesFrom(ctx context.Context) ([]string, bool) {
	scopes, ok := ctx.Value(scopesKey{}).([]string)
	return scopes, ok
}

// authenticate resolves a bearer token to an Identity and puts it in the
// request context. It is deliberately permissive: an absent or bad token is
// simply no identity, because /v1/auth/login and friends must stay reachable.
// Handlers that require a caller ask for one with requireIdentity, so the
// authorisation decision sits with the operation rather than the router.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok || s.DB == nil {
			next.ServeHTTP(w, r)
			return
		}
		hash := auth.HashToken(token)

		// Operator sessions are resolved from their OWN table, and only for
		// /v1/operator paths. Two separate checks rather than one combined
		// lookup, because a tenant token must never satisfy an operator route
		// and an operator token must never scope a tenant query — the moment
		// those share a code path, one missing branch becomes a cross-tenant
		// leak.
		if strings.HasPrefix(r.URL.Path, "/v1/operator") {
			operator, operatorErr := store.ResolveOperatorSession(r.Context(), s.DB, hash)
			if operatorErr == nil {
				ctx := context.WithValue(r.Context(), operatorKey{}, operator)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			// Falls through deliberately: /v1/operator/login is under this
			// prefix and must stay reachable without a session.
			next.ServeHTTP(w, r)
			return
		}

		// An API key, not a session token.
		//
		// Keys were mintable from the moment the dashboard shipped and
		// authenticated nothing: store.ResolveAPIKey had no callers at all, so
		// every sk_live_ key a customer pasted into their code was decoration.
		//
		// Accepted ONLY for the send endpoint, deliberately. A key carries
		// scopes rather than a role, and no other handler checks them — letting
		// a key through the general path would mean a read:messages key could
		// read the team roster and the billing history too. Widening this means
		// enforcing scopes at each endpoint that opts in, not here.
		if isAPIKey(token) {
			if r.Method == http.MethodPost && r.URL.Path == "/v1/messages" {
				keyIdentity, scopes, environment, keyErr := store.ResolveAPIKey(
					r.Context(), s.DB, token)
				if keyErr == nil {
					ctx := context.WithValue(r.Context(), identityKey{}, keyIdentity)
					ctx = context.WithValue(ctx, scopesKey{}, scopes)
					ctx = context.WithValue(ctx, environmentKey{}, environment)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}

		identity, err := store.ResolveSession(r.Context(), s.DB, hash)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		identity, err = store.LoadIdentityDetail(r.Context(), s.DB, identity)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), identityKey{}, identity)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}

// identityFrom returns the caller resolved by the authenticate middleware.
func identityFrom(ctx context.Context) (store.Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(store.Identity)
	return identity, ok
}

// errUnauthenticated and errForbidden are sentinel errors handlers return when
// they cannot produce their operation's typed error response. Operations whose
// contract lists 401/403 return the typed response directly instead.
var (
	errUnauthenticated = errors.New("unauthenticated")
	errForbidden       = errors.New("forbidden")
	// errInvalidFilter covers a bad enum on an operation whose contract
	// declares only a 200. Returning 200 with unfiltered results instead would
	// answer a different question than the one asked.
	errInvalidFilter = errors.New("invalid filter value")
)

// canManageSettings reports whether a role may change account, billing,
// compliance or developer settings. The frontend's middleware gates the same
// areas on GET /v1/me's role, so these 403s have to be real — the UI trusts
// them to decide whether a page even executes.
func canManageSettings(role string) bool {
	return role == "owner" || role == "admin"
}

func errorBody(code, message string) gen.Error {
	var body gen.Error
	body.Error.Code = code
	body.Error.Message = message
	return body
}

const (
	codeUnauthenticated = "unauthenticated"
	codeForbidden       = "forbidden"
	codeValidation      = "validation_failed"
	codeConflict        = "conflict"
	codeNotFound        = "not_found"
)
