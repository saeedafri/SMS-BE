// Command seed-demo creates the demo tenant the frontend team's Playwright
// suite expects.
//
// The UI repo ships 43 browser specs, all written against MSW's fixed fixture:
// founder@acme.test / relay-dev, org "Acme Retail", with specific sender and
// wallet state. Those specs are the real answer to "are the UI and backend in
// sync" — they drive actual forms in an actual browser, which no curl suite
// can. They were unrunnable against this backend only because the fixture
// existed nowhere but in MSW's memory. This puts it in Postgres.
//
// Identifiers are copied verbatim from ../SMS-UI/src/mocks/seed.ts. They are
// fixed rather than generated because the specs assert on them.
//
// It connects as the migration role so it can write across tenants, and it is
// idempotent: running it twice restores the fixture rather than duplicating it,
// which matters because the specs mutate this data as they run.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saeedafri/sms-be/internal/domain/auth"
	"github.com/saeedafri/sms-be/internal/platform/config"
)

const (
	tenantID = "88888888-8888-8888-8888-888888888888"
	userID   = "99999999-9999-9999-9999-999999999999"
	smsID    = "11111111-1111-1111-1111-111111111111"
	rcsID    = "22222222-2222-2222-2222-222222222222"
	password = "relay-dev"
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

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	// Deleting the tenant cascades to everything owned by it, which is what
	// makes a re-run restore the fixture instead of colliding with it.
	if _, err := pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID); err != nil {
		return fmt.Errorf("clear demo tenant: %w", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE email = 'founder@acme.test'`); err != nil {
		return fmt.Errorf("clear demo user: %w", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO tenants (id, name, country, status, capabilities)
		VALUES ($1, 'Acme Retail', 'IN', 'active',
		        ARRAY['sms.send','rcs.send','compliance.manage','billing.view'])`,
		tenantID); err != nil {
		return fmt.Errorf("seed tenant: %w", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, name, password_hash, email_verified)
		VALUES ($1, 'founder@acme.test', 'Alex Rao', $2, true)`,
		userID, hash); err != nil {
		return fmt.Errorf("seed user: %w", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, 'owner')`,
		tenantID, userID); err != nil {
		return fmt.Errorf("seed membership: %w", err)
	}

	// One approved sender and one still in review: the senders spec asserts on
	// both states, so a fixture with only approved rows would pass vacuously.
	if _, err := pool.Exec(ctx, `
		INSERT INTO sender_ids (id, tenant_id, header, channel, country, status, created_at)
		VALUES ($1, $3, 'ACMERT', 'SMS', 'IN', 'approved',    '2026-05-02T09:30:00Z'),
		       ($2, $3, 'ACMERT', 'RCS', 'IN', 'pending_review','2026-06-18T14:05:00Z')`,
		smsID, rcsID, tenantID); err != nil {
		return fmt.Errorf("seed senders: %w", err)
	}

	// The wallet is funded through the ledger rather than by setting a balance
	// directly, because the balance is derived and a trigger forbids rewriting
	// history. Seeding the balance column alone would leave the two disagreeing.
	if _, err := pool.Exec(ctx, `
		INSERT INTO wallet_balances (tenant_id, currency, balance_minor)
		VALUES ($1, 'INR', 0) ON CONFLICT DO NOTHING`, tenantID); err != nil {
		return fmt.Errorf("seed wallet: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO wallet_ledger (tenant_id, currency, entry_type, amount_minor,
		                           balance_after_minor, description)
		VALUES ($1, 'INR', 'topup', 4250000, 4250000, 'Demo seed')`,
		tenantID); err != nil {
		return fmt.Errorf("seed ledger: %w", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE wallet_balances SET balance_minor = 4250000
		 WHERE tenant_id = $1 AND currency = 'INR'`, tenantID); err != nil {
		return fmt.Errorf("set balance: %w", err)
	}

	fmt.Println("seeded Acme Retail — founder@acme.test / relay-dev")
	return nil
}
