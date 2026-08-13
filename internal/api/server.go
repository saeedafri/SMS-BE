package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

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
}

func NewRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer)
	r.Use(requestLogger(s.Logger))
	r.Use(s.authenticate)

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			"that method is not allowed on this endpoint")
	})

	r.Get("/healthz", s.healthz)

	handler := gen.NewStrictHandlerWithOptions(s, nil, gen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		},
		ResponseErrorHandlerFunc: writeOperationError,
	})
	gen.HandlerFromMux(handler, r)
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
	if errors.As(err, &notImpl) {
		writeError(w, http.StatusNotImplemented, "not_implemented", notImpl.Error())
		return
	}
	// Deliberately not err.Error(): internal failure detail belongs in logs,
	// not in a response body a tenant can read.
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
