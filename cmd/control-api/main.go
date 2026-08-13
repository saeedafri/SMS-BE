// Command control-api serves the dashboard and operator-console contract —
// the 151 operations defined in ../SMS-UI/openapi.json.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/saeedafri/sms-be/internal/api"
	"github.com/saeedafri/sms-be/internal/platform/config"
	"github.com/saeedafri/sms-be/internal/platform/telemetry"
	"github.com/saeedafri/sms-be/internal/store"
)

func main() {
	if err := run(); err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := telemetry.NewLogger(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	rdb, err := store.OpenRedis(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()

	server := &http.Server{
		Addr:              cfg.ControlAPIAddr,
		Handler:           api.NewRouter(&api.Server{DB: pool, Redis: rdb, Logger: logger}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		logger.Info("control-api listening", "addr", cfg.ControlAPIAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}
