package api

import (
	"net/http"
)

// roleViewer can read everything its tenant can and change nothing.
const roleViewer = "viewer"

// A viewer may still manage its own sign-in: ending its session, changing its
// own password, and enrolling a second factor. Nothing else that writes.
var viewerMayWrite = map[string]bool{
	"POST /v1/auth/logout":             true,
	"POST /v1/auth/password":           true,
	"POST /v1/auth/mfa/enroll":         true,
	"POST /v1/auth/mfa/enroll/confirm": true,
	"POST /v1/auth/mfa/disable":        true,
	"DELETE /v1/sessions/{id}":         true,
}

// readOnlyViewers refuses every write from a viewer.
//
// One middleware rather than a check in each handler, for the reason
// rejectUnknownFields gives: the failure to guard against is the handler that
// forgot. There are 170 operations and a viewer role that depended on every
// one of them remembering would be a role that leaks the first time someone adds
// a route. Reads are allowed by method, so a new GET is visible to a viewer
// without anyone deciding it should be; a new write is refused without anyone
// remembering to refuse it.
func (s *Server) readOnlyViewers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if identity, ok := identityFrom(r.Context()); ok && identity.Role == roleViewer {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
			default:
				if !viewerMayWrite[routeKey(r)] {
					writeError(w, http.StatusForbidden, codeForbidden,
						"View-only members cannot make changes.")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
