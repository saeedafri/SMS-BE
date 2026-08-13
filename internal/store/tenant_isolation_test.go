package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saeedafri/sms-be/internal/store"
)

// seedTenants inserts tenants using the admin connection (which owns the table
// and so is not subject to the app role's policy) and removes them afterwards.
func seedTenants(t *testing.T, ctx context.Context, ids ...uuid.UUID) *pgxpool.Pool {
	t.Helper()

	adminURL := os.Getenv("TEST_DATABASE_ADMIN_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_ADMIN_URL not set")
	}
	admin, err := store.Open(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	t.Cleanup(admin.Close)

	for _, id := range ids {
		if _, err := admin.Exec(ctx,
			`INSERT INTO tenants (id, name) VALUES ($1, $2)`, id, "t-"+id.String()); err != nil {
			t.Fatalf("seed tenant %s: %v", id, err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(),
			`DELETE FROM tenants WHERE id = ANY($1)`, ids)
	})
	return admin
}

func appPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := store.Open(ctx, url)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Tenant A must never read tenant B's row. Isolation is enforced by Postgres
// row-level security, not by the query text — so the query below deliberately
// carries NO tenant predicate beyond the id being looked up. If RLS is
// misconfigured, this finds a row and fails, which is exactly the data-leak
// defect the PRD calls out in §8.
func TestTenantCannotReadAnotherTenant(t *testing.T) {
	ctx := context.Background()
	tenantA, tenantB := uuid.New(), uuid.New()
	seedTenants(t, ctx, tenantA, tenantB)
	pool := appPool(t, ctx)

	var found int
	err := store.WithTenant(ctx, pool, tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM tenants WHERE id = $1`, tenantB).Scan(&found)
	})
	if err != nil {
		t.Fatalf("WithTenant: %v", err)
	}
	if found != 0 {
		t.Fatalf("tenant A read %d of tenant B's rows, want 0 — RLS is not isolating", found)
	}
}

func TestTenantCanReadItsOwnRow(t *testing.T) {
	ctx := context.Background()
	tenantA := uuid.New()
	seedTenants(t, ctx, tenantA)
	pool := appPool(t, ctx)

	var found int
	err := store.WithTenant(ctx, pool, tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM tenants WHERE id = $1`, tenantA).Scan(&found)
	})
	if err != nil {
		t.Fatalf("WithTenant: %v", err)
	}
	if found != 1 {
		t.Fatalf("tenant A read %d of its own rows, want 1 — RLS is over-blocking", found)
	}
}

// A connection that never scoped itself to a tenant must see nothing. This is
// the fail-closed property: forgetting to call WithTenant loses you data, it
// does not hand you everyone's.
func TestUnscopedConnectionSeesNothing(t *testing.T) {
	ctx := context.Background()
	tenantA := uuid.New()
	seedTenants(t, ctx, tenantA)
	pool := appPool(t, ctx)

	var found int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tenants`).Scan(&found); err != nil {
		t.Fatalf("query: %v", err)
	}
	if found != 0 {
		t.Fatalf("an unscoped connection read %d rows, want 0 — RLS is not failing closed", found)
	}
}

// Isolation must hold for every tenant-scoped table, not just the one it was
// first proven on. Each new table added in a later stage gets a row here — a
// table missing from this list is a table nobody has proven isolates.
func TestEveryTenantScopedTableIsolates(t *testing.T) {
	ctx := context.Background()
	tenantA, tenantB := uuid.New(), uuid.New()
	admin := seedTenants(t, ctx, tenantA, tenantB)
	pool := appPool(t, ctx)

	userID := uuid.New()
	if _, err := admin.Exec(ctx,
		`INSERT INTO users (id, email, name, password_hash)
		 VALUES ($1, $2, 'B User', 'x')`, userID, "b-"+userID.String()+"@example.test"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	// Every row below belongs to tenant B.
	if _, err := admin.Exec(ctx,
		`INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, 'owner')`,
		tenantB, userID); err != nil {
		t.Fatalf("seed tenant_users: %v", err)
	}
	if _, err := admin.Exec(ctx,
		`INSERT INTO sessions (tenant_id, user_id, token_hash, expires_at)
		 VALUES ($1, $2, $3, now() + interval '1 hour')`,
		tenantB, userID, []byte("hash-for-isolation-test")); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}

	// Counting all visible rows would be wrong: tenant A legitimately sees its
	// own. Each query below counts only rows owned by tenant B, so any non-zero
	// result is a genuine cross-tenant leak.
	senderID := uuid.New()
	if _, err := admin.Exec(ctx,
		`INSERT INTO sender_ids (id, tenant_id, header, channel, country)
		 VALUES ($1, $2, 'BTENANT', 'SMS', 'IN')`, senderID, tenantB); err != nil {
		t.Fatalf("seed sender_ids: %v", err)
	}
	if _, err := admin.Exec(ctx,
		`INSERT INTO templates (tenant_id, sender_id, name, channel, country, body)
		 VALUES ($1, $2, 'b-template', 'SMS', 'IN', 'hello')`, tenantB, senderID); err != nil {
		t.Fatalf("seed templates: %v", err)
	}
	if _, err := admin.Exec(ctx,
		`INSERT INTO registrations (tenant_id, country, object_key)
		 VALUES ($1, 'IN', 'pe_rtm_entity')`, tenantB); err != nil {
		t.Fatalf("seed registrations: %v", err)
	}

	if _, err := admin.Exec(ctx,
		`INSERT INTO wallet_balances (tenant_id, currency, balance_minor)
		 VALUES ($1, 'INR', 5000)`, tenantB); err != nil {
		t.Fatalf("seed wallet_balances: %v", err)
	}
	if _, err := admin.Exec(ctx,
		`INSERT INTO wallet_ledger (tenant_id, currency, entry_type, amount_minor, balance_after_minor)
		 VALUES ($1, 'INR', 'topup', 5000, 5000)`, tenantB); err != nil {
		t.Fatalf("seed wallet_ledger: %v", err)
	}
	if _, err := admin.Exec(ctx,
		`INSERT INTO payment_methods (tenant_id, brand, last4) VALUES ($1, 'visa', '4242')`,
		tenantB); err != nil {
		t.Fatalf("seed payment_methods: %v", err)
	}
	if _, err := admin.Exec(ctx,
		`INSERT INTO auto_recharge_configs (tenant_id, currency) VALUES ($1, 'INR')`,
		tenantB); err != nil {
		t.Fatalf("seed auto_recharge_configs: %v", err)
	}
	if _, err := admin.Exec(ctx,
		`INSERT INTO invoices (tenant_id, currency, period_start, period_end)
		 VALUES ($1, 'INR', now() - interval '30 days', now())`, tenantB); err != nil {
		t.Fatalf("seed invoices: %v", err)
	}

	tables := []struct{ name, query string }{
		{"tenants", `SELECT count(*) FROM tenants WHERE id = $1`},
		{"tenant_users", `SELECT count(*) FROM tenant_users WHERE tenant_id = $1`},
		{"sessions", `SELECT count(*) FROM sessions WHERE tenant_id = $1`},
		{"sender_ids", `SELECT count(*) FROM sender_ids WHERE tenant_id = $1`},
		{"templates", `SELECT count(*) FROM templates WHERE tenant_id = $1`},
		{"registrations", `SELECT count(*) FROM registrations WHERE tenant_id = $1`},
		{"wallet_balances", `SELECT count(*) FROM wallet_balances WHERE tenant_id = $1`},
		{"wallet_ledger", `SELECT count(*) FROM wallet_ledger WHERE tenant_id = $1`},
		{"payment_methods", `SELECT count(*) FROM payment_methods WHERE tenant_id = $1`},
		{"auto_recharge_configs", `SELECT count(*) FROM auto_recharge_configs WHERE tenant_id = $1`},
		{"invoices", `SELECT count(*) FROM invoices WHERE tenant_id = $1`},
	}
	for _, table := range tables {
		t.Run(table.name, func(t *testing.T) {
			var found int
			err := store.WithTenant(ctx, pool, tenantA, func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, table.query, tenantB).Scan(&found)
			})
			if err != nil {
				t.Fatalf("WithTenant: %v", err)
			}
			if found != 0 {
				t.Fatalf("scoped to tenant A, %s exposed %d of tenant B's rows, want 0",
					table.name, found)
			}
		})
	}
}

// The scoping must not survive its transaction. SET LOCAL ties the setting to
// the transaction, so the next user of this pooled connection starts clean —
// otherwise tenant A's scope would leak onto tenant B's request.
func TestTenantScopeDoesNotLeakAcrossTransactions(t *testing.T) {
	ctx := context.Background()
	tenantA := uuid.New()
	seedTenants(t, ctx, tenantA)
	pool := appPool(t, ctx)

	err := store.WithTenant(ctx, pool, tenantA, func(tx pgx.Tx) error {
		var n int
		return tx.QueryRow(ctx, `SELECT count(*) FROM tenants`).Scan(&n)
	})
	if err != nil {
		t.Fatalf("WithTenant: %v", err)
	}

	var found int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tenants`).Scan(&found); err != nil {
		t.Fatalf("query: %v", err)
	}
	if found != 0 {
		t.Fatalf("after a scoped transaction, an unscoped query read %d rows, want 0 — the scope leaked", found)
	}
}
