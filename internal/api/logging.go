package api

import (
	"bytes"
	"context"
	"encoding/json"
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
			// The first bytes of every response are kept so a refusal's own
			// error code and message land on its log line: "422" alone says a
			// request failed, not why.
			body := &cappedBuffer{limit: 2048}
			wrapped.Tee(body)
			started := time.Now()

			// Authentication runs after this middleware on a derived context,
			// so it reports the caller back through this holder.
			fields := &callerFields{}
			r = r.WithContext(context.WithValue(r.Context(), callerKey{}, fields))

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
				"client_ip", clientIP(r),
				"user_agent", truncate(r.UserAgent(), 200),
			}
			if r.URL.RawQuery != "" {
				attrs = append(attrs, "query", truncate(r.URL.RawQuery, 500))
			}
			// Tenant on every line. During an incident the first question is
			// almost always "is this one customer or everyone", and without
			// this it cannot be answered from the logs at all.
			if fields.auth != "" {
				attrs = append(attrs, "auth", fields.auth)
			}
			if fields.tenantID != "" {
				attrs = append(attrs, "tenant_id", fields.tenantID)
			}
			if fields.userID != "" {
				attrs = append(attrs, "user_id", fields.userID)
			}
			if fields.operator != "" {
				attrs = append(attrs, "operator", fields.operator)
			}
			if status >= 400 {
				var envelope struct {
					Error struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if json.Unmarshal(body.Bytes(), &envelope) == nil && envelope.Error.Code != "" {
					attrs = append(attrs, "error_code", envelope.Error.Code,
						"error_message", truncate(envelope.Error.Message, 500))
				}
			}
			if fields.internalError != "" {
				attrs = append(attrs, "internal_error", truncate(fields.internalError, 1000))
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

// callerFields is what authentication and error handling learn about a request
// after the logger has already started it.
type callerFields struct {
	auth, tenantID, userID, operator, internalError string
}

type callerKey struct{}

func noteCaller(ctx context.Context, auth, tenantID, userID, operator string) {
	if fields, ok := ctx.Value(callerKey{}).(*callerFields); ok {
		fields.auth, fields.tenantID, fields.userID, fields.operator = auth, tenantID, userID, operator
	}
}

func noteInternalError(ctx context.Context, err error) {
	if fields, ok := ctx.Value(callerKey{}).(*callerFields); ok && err != nil {
		fields.internalError = err.Error()
	}
}

// cappedBuffer keeps the first limit bytes written to it and discards the rest.
type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.Len(); room > 0 {
		if len(p) > room {
			b.Buffer.Write(p[:room])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
