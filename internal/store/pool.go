// Package store owns database connectivity and the tenant-scoping contract.
// Every tenant-scoped read or write goes through WithTenant, which is what
// makes row-level security effective: the policy reads app.tenant_id, and this
// package is the only place that setting is ever written.
package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("store: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return pool, nil
}

// WithTenant runs fn inside a transaction scoped to one tenant, committing if
// fn succeeds and rolling back otherwise.
//
// set_config's third argument is is_local=true, which ties the setting to this
// transaction. That matters more than it looks: connections are pooled, so a
// session-scoped setting would carry one tenant's scope onto the next request
// that borrowed the same connection.
func WithTenant(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID,
	fn func(pgx.Tx) error) error {

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`,
		tenantID.String()); err != nil {
		return fmt.Errorf("store: set tenant scope: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// OpenOperatorPool opens a pool whose every connection can see across tenants.
//
// The operator console reads and writes for all customers at once, which is
// exactly what row-level security prevents. Rather than give operators a
// BYPASSRLS role — one leaked connection string and every policy in the system
// is void — the policies in 00019 open only while `app.operator` is set, and
// this pool sets it on each connection as it is created.
//
// The separation is the safety property: this pool is handed ONLY to operator
// handlers, and the ordinary pool is unchanged, so tenant traffic still cannot
// cross a tenant boundary no matter what a handler gets wrong. Passing this
// pool to a tenant query would defeat that, which is why it is a distinct
// constructor with a distinct name rather than a flag on the usual one.
func OpenOperatorPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("store: parse operator pool url: %w", err)
	}
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SELECT set_config('app.operator', 'on', false)`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("store: open operator pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping operator pool: %w", err)
	}
	return pool, nil
}
