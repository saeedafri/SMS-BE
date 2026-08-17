package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/domain/billing"
	"github.com/saeedafri/sms-be/internal/mailer"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// Server satisfies the generated contract interface. Embedding Unimplemented
// means all 151 operations compile today and answer 501; each stage replaces
// operations by defining real methods on Server, which shadow the embedded
// stubs. Nothing else in the codebase changes when that happens.
type Server struct {
	Unimplemented
	DB     *pgxpool.Pool
	Redis  *redis.Client
	Logger *slog.Logger

	// EnableDevEndpoints mounts /v1/dev/*, the browser suite's state hooks.
	// Off unless the deployment opts in.
	EnableDevEndpoints bool

	// OperatorDB sees across tenants and is used ONLY by operator-console
	// handlers. Tenant handlers must keep using DB: that split is what stops a
	// mistake in one handler from becoming a cross-tenant leak.
	OperatorDB *pgxpool.Pool

	// AdminDB is the migration role, used by exactly one thing: the dev reset
	// hook, which rebuilds the demo tenant and therefore has to write across
	// tenants and briefly disable the ledger's append-only trigger. Nil unless
	// dev endpoints are enabled, so no request path can reach it in production.
	AdminDB *pgxpool.Pool

	// Connector is the carrier the send path submits to. Nil means no data
	// plane, so campaign and send endpoints refuse rather than silently
	// accepting messages nothing will ever deliver.
	Connector connector.Connector

	// Gateway captures payments. Nil means the manual gateway, which records a
	// capture without contacting anyone — correct for bank-transfer and
	// invoice-paid customers, and the seam a real provider slots into.
	Gateway billing.PaymentGateway

	// ClickHouse holds message logs. It is a self-healing pool rather than a
	// fixed connection: a dependency whose absence is meant to be a degraded
	// screen must not become a permanent outage because it was down at boot or
	// restarted later.
	ClickHouse *store.ClickHousePool

	// Metrics is the live picture of this process. Nil disables collection
	// entirely, which keeps tests from accumulating state between cases.
	Metrics *Metrics

	// Mail sends verification links, password resets and team invitations. Nil,
	// or configured without an API key, logs instead of sending — so a missing
	// credential degrades to the previous behaviour rather than breaking
	// sign-up.
	Mail *mailer.Mailer

	// AppBaseURL is the frontend origin used to build links in those emails.
	AppBaseURL string
}

// clickhouse dials on demand, so a restarted ClickHouse is picked up without
// restarting this process.
func (s *Server) clickhouse(ctx context.Context) (driver.Conn, error) {
	conn, err := s.ClickHouse.Conn(ctx)
	if err != nil {
		return nil, errClickHouseUnavailable
	}
	return conn, nil
}

// clickhouseFailed is called when a query errors. The driver pools connections
// internally, so a handle that already believes it is connected will keep
// handing out dead ones after the server restarts — dropping it here is what
// makes the NEXT request redial instead of failing forever.
func (s *Server) clickhouseFailed(err error) error {
	if err != nil {
		s.ClickHouse.Drop()
	}
	return err
}

func NewRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer)
	r.Use(requestLogger(s.Logger, s.Metrics))
	r.Use(s.authenticate)

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			"that method is not allowed on this endpoint")
	})

	r.Get("/healthz", s.healthz)
	// Liveness vs readiness are different questions: healthz says "this process
	// works", readyz says "send it traffic". A load balancer wants the second.
	r.Get("/readyz", s.readiness)
	// Live counters, timings and recent incidents. No tenant data, so no auth —
	// an observability endpoint that needs a working login is useless exactly
	// when login is what has broken.
	r.Get("/metrics", s.metrics)

	// Registered only when explicitly enabled. Mounting these unconditionally
	// and checking a flag inside each handler would leave the routes present in
	// production, where an endpoint that changes the caller's role or zeroes a
	// balance must not merely refuse — it must not exist.
	if s.EnableDevEndpoints {
		s.Logger.Warn("dev test hooks are ENABLED — never do this in production",
			"routes", "/v1/dev/*")
		s.mountDevRoutes(r)
	}

	handler := gen.NewStrictHandlerWithOptions(s, nil, gen.StrictHTTPServerOptions{
		// A request the generated binder rejects — bad JSON, a malformed
		// parameter, a field failing its format — is reported as 422
		// validation_failed rather than 400. The contract declares 400 on
		// exactly one of its 151 operations but 422 on 57, so 422 is the code
		// the frontend's error states are actually written against.
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusUnprocessableEntity, codeValidation, err.Error())
		},
		ResponseErrorHandlerFunc: writeOperationError,
	})
	// ErrorHandlerFunc covers a different failure than the strict handler's
	// RequestErrorHandlerFunc above: parameter binding happens in the router
	// layer, BEFORE the strict handler runs. Without this, a missing or
	// malformed query parameter returned 400 text/plain — not the contract's
	// Error envelope — on all 151 operations, and the frontend's error states
	// read error.code, so they would see an unparseable body.
	gen.HandlerWithOptions(handler, gen.ChiServerOptions{
		BaseRouter: r,
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusUnprocessableEntity, codeValidation, err.Error())
		},
	})
	return r
}

// healthz reports the process and each dependency it cannot serve without.
// It returns 503 when a dependency is down so a load balancer pulls this
// instance rather than sending it traffic it will fail.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	checks := map[string]string{}
	healthy := true

	if s.DB != nil {
		if err := s.DB.Ping(r.Context()); err != nil {
			checks["postgres"], healthy = "down", false
		} else {
			checks["postgres"] = "up"
		}
	}
	if s.Redis != nil {
		if err := s.Redis.Ping(r.Context()).Err(); err != nil {
			checks["redis"], healthy = "down", false
		} else {
			checks["redis"] = "up"
		}
	}
	// ClickHouse is reported but does NOT fail the check. It holds message
	// logs and analytics; every other domain works without it. Failing the
	// health check would make a load balancer pull a node that can still serve
	// authentication, campaigns, billing and compliance — a worse outage than
	// the one being reported.
	// Healthy() repairs the handle as a side effect, so the health endpoint is
	// also what NOTICES recovery — no operator action, no restart.
	if !s.ClickHouse.Configured() {
		checks["clickhouse"] = "not_configured"
	} else if s.ClickHouse.Healthy(r.Context()) {
		checks["clickhouse"] = "up"
	} else {
		checks["clickhouse"] = "down"
	}
	if s.Connector != nil {
		if health := s.Connector.Health(r.Context()); health.Healthy {
			checks["carrier"] = "up"
		} else {
			checks["carrier"] = "down"
		}
	}

	status, code := "ok", http.StatusOK
	if !healthy {
		status, code = "degraded", http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{"status": status, "checks": checks})
}

// writeOperationError renders every error leaving an operation as the
// contract's Error schema. The frontend's error states read error.code, so the
// envelope is part of the contract even for failures we consider internal.
func writeOperationError(w http.ResponseWriter, _ *http.Request, err error) {
	var notImpl notImplementedError
	var dependency dependencyUnmetError
	switch {
	case errors.As(err, &dependency):
		writeError(w, http.StatusConflict, "dependency_unmet", dependency.Error())
		return
	case errors.As(err, &notImpl):
		writeError(w, http.StatusNotImplemented, "not_implemented", notImpl.Error())
		return
	// Some operations are declared with only a 200 response even though the
	// contract marks them as requiring a bearer token — the security scheme
	// implies 401/403 rather than listing them. Handlers signal those cases
	// with these sentinels so they still get the right status and the standard
	// envelope, instead of collapsing into a 500.
	case errors.Is(err, errUnauthenticated):
		writeError(w, http.StatusUnauthorized, codeUnauthenticated,
			"Missing or invalid bearer token")
		return
	case errors.Is(err, errForbidden):
		writeError(w, http.StatusForbidden, codeForbidden,
			"Your role does not have access to this.")
		return
	case errors.Is(err, errInvalidFilter):
		writeError(w, http.StatusUnprocessableEntity, codeValidation, err.Error())
		return
	case errors.As(err, &dependency):
		// 409, not 422: the request is well formed and the operator is allowed
		// to make it — the world is simply not ready yet, and it will be once
		// the records verify.
		writeError(w, http.StatusConflict, "dependency_unmet", dependency.Error())
		return
	}
	// Deliberately not err.Error() in the body: internal failure detail belongs
	// in logs, not in a response a tenant can read. It must still reach the
	// logs though — an unexplained 500 is unactionable, and this envelope was
	// hiding the cause of every internal failure.
	slog.Error("unhandled operation error", "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error",
		"an unexpected error occurred")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
