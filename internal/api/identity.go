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
		identity, err := store.ResolveSession(r.Context(), s.DB, auth.HashToken(token))
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
