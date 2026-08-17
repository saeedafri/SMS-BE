// Command seed-demo creates the demo tenant the frontend team's Playwright
// suite expects. The rebuild itself lives in internal/demoseed, because the
// /v1/dev/reset-mock-state test hook has to run exactly the same thing between
// specs — two copies of a fixture definition would drift within a week.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saeedafri/sms-be/internal/demoseed"
	"github.com/saeedafri/sms-be/internal/platform/config"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// The admin URL is the migration role, which bypasses RLS. A seed writes
	// rows for a tenant before any session for that tenant exists, so it cannot
	// go through the normal scoped path.
	url := os.Getenv("DATABASE_ADMIN_URL")
	if url == "" {
		url = cfg.DatabaseURL
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := demoseed.Apply(ctx, pool); err != nil {
		return err
	}
	fmt.Println("seeded Acme Retail — founder@acme.test / relay-dev")
	return nil
}
