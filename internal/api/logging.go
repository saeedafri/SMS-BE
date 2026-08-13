package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// requestLogger emits one structured line per request and folds it into the
// live metrics.
//
// It is deliberately not chi's built-in Logger, which writes coloured plain
// text — this pairs with the JSON slog handler so logs stay machine-readable
// and carry the request ID that middleware.RequestID assigned, which is the
// only way to correlate a user's report with a line in the log.
//
// Log LEVEL carries meaning, so that filtering by level is a useful triage
// step rather than noise reduction:
//
//	ERROR  the server failed (5xx) — always someone's problem
//	WARN   the request was refused (4xx) or was abnormally slow
//	INFO   normal traffic
func requestLogger(logger *slog.Logger, metrics *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			started := time.Now()

			next.ServeHTTP(wrapped, r)

			duration := time.Since(started)
			status := wrapped.Status()
			requestID := middleware.GetReqID(r.Context())

			// The route pattern, not the raw path: /v1/campaigns/{id} rather
			// than a thousand distinct paths, one per campaign id. Without
			// this, per-route metrics are unusable and the log is unfilterable.
			route := r.URL.Path
			if pattern := chiRoutePattern(r); pattern != "" {
				route = pattern
			}

			metrics.Record(r.Method, route, status, duration, requestID, "")

			if logger == nil {
				return
			}
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"route", route,
				"status", status,
				"bytes", wrapped.BytesWritten(),
				"duration_ms", float64(duration.Microseconds()) / 1000,
				"request_id", requestID,
			}
			// Tenant on every line. During an incident the first question is
			// almost always "is this one customer or everyone", and without
			// this it cannot be answered from the logs at all.
			if identity, ok := identityFrom(r.Context()); ok {
				attrs = append(attrs, "tenant_id", identity.TenantID.String())
			}
			if duration >= slowRequestThreshold {
				attrs = append(attrs, "slow", true)
			}

			switch {
			case status >= 500:
				logger.Error("request failed", attrs...)
			case status >= 400, duration >= slowRequestThreshold:
				logger.Warn("request refused or slow", attrs...)
			default:
				logger.Info("request", attrs...)
			}
		})
	}
}

// chiRoutePattern returns the matched route template once routing has run.
func chiRoutePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		return rctx.RoutePattern()
	}
	return ""
}
