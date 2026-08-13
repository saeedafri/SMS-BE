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

	"github.com/saeedafri/sms-be/internal/api"
	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/platform/config"
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

	if clickhouse != nil {
		// Applies the sandbox carrier's delivery reports. A real carrier POSTs
		// these to our ingest endpoint; the sandbox queues them in-process, so
		// without this a message would sit at "sent" forever and the delivered
		// vs undelivered distinction — the whole product — would be invisible.
		// Same settlement code either way, different trigger.
		drainer := &sending.Service{DB: pool, ClickHouse: clickhouse, Connector: sandbox}
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if applied, err := drainer.DrainSandboxReports(ctx); err != nil {
						logger.Error("drain sandbox reports", "error", err)
					} else if applied > 0 {
						logger.Info("applied delivery reports", "count", applied)
					}
				}
			}
		}()

		reconciler := &sending.Service{DB: pool, ClickHouse: clickhouse}
		go func() {
			ticker := time.NewTicker(15 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					expired, err := reconciler.Reconcile(ctx, sending.DefaultValidityWindow, 1000)
					if err != nil {
						logger.Error("reconcile", "expired", expired, "error", err)
					} else if expired > 0 {
						logger.Info("reconciled stale messages", "expired", expired)
					}
				}
			}
		}()
	}

	server := &http.Server{
		Addr: cfg.ControlAPIAddr,
		Handler: api.NewRouter(&api.Server{DB: pool, Redis: rdb, Logger: logger,
			ClickHouse: clickhouse, Connector: sandbox}),
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
