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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saeedafri/sms-be/internal/api"
	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/mailer"
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

	// ClickHouse is a SELF-HEALING handle, not a connection made once here.
	// Dialling at startup meant a ClickHouse that was slow to become ready on a
	// server reboot left the API permanently without message logs, and a
	// ClickHouse restarted later left dead connections nobody replaced. Both
	// needed a human to fix a dependency whose absence is supposed to be a
	// degraded screen.
	clickhouse := store.NewClickHousePool(cfg.ClickHouseURL, logger)
	defer clickhouse.Close()

	// The admin pool exists only to let the dev reset hook rebuild the demo
	// tenant, so it is opened only when those hooks are on. A production process
	// never holds a handle to the migration role.
	var adminPool *pgxpool.Pool
	if cfg.EnableDevEndpoints && cfg.DatabaseAdminURL != "" {
		adminPool, err = pgxpool.New(ctx, cfg.DatabaseAdminURL)
		if err != nil {
			return err
		}
		defer adminPool.Close()
	}

	// The operator console's pool. Separate from the tenant pool because its
	// connections carry cross-tenant visibility; keeping them apart means a
	// tenant handler cannot accidentally acquire one.
	operatorPool, err := store.OpenOperatorPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer operatorPool.Close()

	sandbox := connector.NewSandbox(0)
	metrics := api.NewMetrics()

	if clickhouse.Configured() {
		// Applies the sandbox carrier's delivery reports. A real carrier POSTs
		// these to an ingest endpoint; the sandbox queues them in-process, so
		// without this a message sits at "sent" forever and the delivered vs
		// undelivered distinction — the whole product — stays invisible.
		go resilience.Supervise(ctx, "delivery-drainer", 2*time.Second, logger,
			func(name string) { metrics.RecordIncident("worker_panic", name) },
			func(ctx context.Context) error {
				// Resolved per tick, so a ClickHouse restart is picked up by
				// the next tick rather than killing the worker for good.
				conn, err := clickhouse.Conn(ctx)
				if err != nil {
					return nil // nothing to drain while it is unreachable
				}
				drainer := &sending.Service{DB: pool, ClickHouse: conn, Connector: sandbox}
				applied, err := drainer.DrainSandboxReports(ctx)
				if err != nil {
					clickhouse.Drop()
				}
				if err == nil && applied > 0 {
					logger.Info("applied delivery reports", "count", applied)
				}
				return err
			})

		// Releases money held against messages a carrier accepted and never
		// reported on. Without it a lost report means a tenant is charged
		// forever for a message nobody can prove arrived.
		go resilience.Supervise(ctx, "reconciler", 15*time.Minute, logger,
			func(name string) { metrics.RecordIncident("worker_panic", name) },
			func(ctx context.Context) error {
				conn, err := clickhouse.Conn(ctx)
				if err != nil {
					return nil
				}
				reconciler := &sending.Service{DB: pool, ClickHouse: conn, Logger: logger}
				expired, err := reconciler.Reconcile(ctx, sending.DefaultValidityWindow, 1000)
				if err != nil {
					clickhouse.Drop()
				}
				if expired > 0 {
					logger.Info("reconciled stale messages", "expired", expired)
				}

				// Campaigns abandoned mid-fan-out. Same tick rather than a
				// second worker: it reads the same ClickHouse handle, runs in
				// milliseconds, and a campaign stuck at 'sending' is the
				// campaign-level version of exactly what the sweep above fixes
				// per message.
				landed, campaignErr := sending.ReconcileStuckCampaigns(ctx,
					operatorPool, pool, conn, sending.StuckCampaignWindow, 100)
				if landed > 0 {
					logger.Info("landed abandoned campaigns", "count", landed)
				}
				return errors.Join(err, campaignErr)
			})
	}

	// Parsed before the server starts: a malformed allowlist must stop the
	// process, not silently leave the console open to everyone.
	operatorAllowlist, err := api.ParseIPAllowlist(cfg.OperatorIPAllowlist)
	if err != nil {
		logger.Error("OPERATOR_IP_ALLOWLIST is not a list of addresses or CIDRs", "error", err)
		os.Exit(1)
	}
	carrierWebhookAllowlist, err := api.ParseIPAllowlist(cfg.CarrierWebhookIPAllowlist)
	if err != nil {
		logger.Error("RCS_WEBHOOK_IP_ALLOWLIST is not a list of addresses or CIDRs", "error", err)
		os.Exit(1)
	}

	// Left nil when no carrier is configured, which is the honest state for a
	// deployment without a commercial RCS agreement: capability discovery then
	// says so, rather than answering "unreachable" for every handset in India.
	var rcsCarrier connector.RCSCapabilityChecker
	switch cfg.RCSCarrierName() {
	case "airtel":
		rcsCarrier = &connector.AirtelRCS{
			BaseURL:      cfg.RCSAirtelBaseURL,
			AuthToken:    cfg.RCSAirtelAuthToken,
			AgentID:      cfg.RCSAirtelAgentID,
			CustomerID:   cfg.RCSAirtelCustomerID,
			SubAccountID: cfg.RCSAirtelSubAccountID,
		}
	case "vi":
		rcsCarrier = &connector.ViRCS{
			BaseURL:      cfg.RCSViBaseURL,
			TokenURL:     cfg.RCSViTokenURL,
			ClientID:     cfg.RCSViClientID,
			ClientSecret: cfg.RCSViClientSecret,
			BotID:        cfg.RCSViBotID,
		}
	}
	// The same carrier serves capability discovery, template registration and
	// sending. Registering it per channel is what keeps an SMS from being handed
	// to an RCS gateway that would 400 every message.
	carriers := connector.Registry{Default: sandbox}
	if rcsCarrier != nil {
		logger.Info("rcs carrier enabled", "vendor", rcsCarrier.Vendor())
		if sender, ok := rcsCarrier.(connector.Connector); ok {
			carriers.ByChannel = map[string]connector.Connector{"RCS": sender}
		}
		// Without a webhook nothing ever settles: every RCS message sits in
		// flight until the reconciler expires it and refunds a message that may
		// well have arrived. Said at boot because it is invisible otherwise —
		// sending looks perfect and the money comes back hours later.
		if cfg.CarrierWebhookToken == "" {
			logger.Warn("rcs carrier is configured but RCS_WEBHOOK_TOKEN is not: " +
				"no delivery reports or template decisions will be accepted")
		}
	}

	apiServer := &api.Server{DB: pool, Redis: rdb, Logger: logger,
		ClickHouse: clickhouse, Connector: sandbox, Metrics: metrics,
		EnableDevEndpoints: cfg.EnableDevEndpoints,
		SignupInviteCode:   cfg.SignupInviteCode, AdminDB: adminPool,
		AllowGreyRoutes:   cfg.AllowGreyRoutes,
		OperatorAllowlist: operatorAllowlist,
		OperatorDB:        operatorPool, AppBaseURL: cfg.AppBaseURL,
		RCSCarrier:              rcsCarrier,
		Carriers:                carriers,
		CarrierWebhookToken:     cfg.CarrierWebhookToken,
		CarrierWebhookAllowlist: carrierWebhookAllowlist,
		Mail: &mailer.Mailer{
			APIKey: cfg.ResendAPIKey, From: cfg.MailFrom, Logger: logger,
		}}

	// Said at every boot, because "rotate the seeded password" is exactly the
	// kind of task that stays open forever when nothing mentions it again.
	api.WarnOnPublishedOperatorPassword(ctx, apiServer)

	server := &http.Server{
		Addr:              cfg.ControlAPIAddr,
		Handler:           api.NewRouter(apiServer),
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
