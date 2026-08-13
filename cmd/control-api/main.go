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

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saeedafri/sms-be/internal/api"
	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/platform/config"
	"github.com/saeedafri/sms-be/internal/platform/resilience"
	"github.com/saeedafri/sms-be/internal/platform/telemetry"
	"github.com/saeedafri/sms-be/internal/sending"
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

	// Retry the database at startup rather than exiting. On a restart the
	// process often comes up before Postgres is accepting connections, and
	// crash-looping until an orchestrator gives up is worse than waiting.
	var pool *pgxpool.Pool
	if err := resilience.Retry(ctx, 5, 500*time.Millisecond, logger, "connect postgres",
		func() error {
			var openErr error
			pool, openErr = store.Open(ctx, cfg.DatabaseURL)
			return openErr
		}); err != nil {
		return err
	}
	defer pool.Close()

	rdb, err := store.OpenRedis(ctx, cfg.RedisURL)
	if err != nil {
		// Redis backs rate limiting only. Refusing to boot without it would
		// take down messaging, billing and compliance to protect a counter.
		logger.Warn("redis unavailable; rate limiting degraded", "error", err)
	}
	if rdb != nil {
		defer rdb.Close()
	}

	// ClickHouse is optional at startup: the control plane serves every domain
	// except message logs without it, and refusing to boot would take the whole
	// dashboard down over one screen.
	var clickhouse driver.Conn
	if cfg.ClickHouseURL != "" {
		clickhouse, err = store.OpenClickHouse(ctx, cfg.ClickHouseURL)
		if err != nil {
			logger.Warn("clickhouse unavailable; message logs will error", "error", err)
		} else {
			defer clickhouse.Close()
		}
	}

	// The reconciler releases money held against messages a carrier accepted
	// and then never reported on. Without it a lost delivery report means a
	// tenant is charged forever for a message nobody can prove arrived.
	//
	// ponytail: runs in-process on a ticker, which is correct for one replica.
	// A second replica would double-sweep — harmless, since expiry is
	// idempotent, but move this to the River queue before scaling out.
	sandbox := connector.NewSandbox(0)
	metrics := api.NewMetrics()

	if clickhouse != nil {
		// Applies the sandbox carrier's delivery reports. A real carrier POSTs
		// these to an ingest endpoint; the sandbox queues them in-process, so
		// without this a message sits at "sent" forever and the delivered vs
		// undelivered distinction — the whole product — stays invisible.
		drainer := &sending.Service{DB: pool, ClickHouse: clickhouse, Connector: sandbox}
		go resilience.Supervise(ctx, "delivery-drainer", 2*time.Second, logger,
			func(name string) { metrics.RecordIncident("worker_panic", name) },
			func(ctx context.Context) error {
				applied, err := drainer.DrainSandboxReports(ctx)
				if err == nil && applied > 0 {
					logger.Info("applied delivery reports", "count", applied)
				}
				return err
			})

		// Releases money held against messages a carrier accepted and never
		// reported on. Without it a lost report means a tenant is charged
		// forever for a message nobody can prove arrived.
		reconciler := &sending.Service{DB: pool, ClickHouse: clickhouse}
		go resilience.Supervise(ctx, "reconciler", 15*time.Minute, logger,
			func(name string) { metrics.RecordIncident("worker_panic", name) },
			func(ctx context.Context) error {
				expired, err := reconciler.Reconcile(ctx, sending.DefaultValidityWindow, 1000)
				if expired > 0 {
					logger.Info("reconciled stale messages", "expired", expired)
				}
				return err
			})
	}

	server := &http.Server{
		Addr: cfg.ControlAPIAddr,
		Handler: api.NewRouter(&api.Server{DB: pool, Redis: rdb, Logger: logger,
			ClickHouse: clickhouse, Connector: sandbox, Metrics: metrics}),
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
