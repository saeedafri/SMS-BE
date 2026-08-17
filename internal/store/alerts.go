package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LoadAlertRules returns the tenant's saved alert rules as raw contract JSON,
// or nil when they have never saved any.
//
// Raw JSON rather than a decoded struct because the API layer owns the
// generated contract types; decoding here would mean a second definition of the
// same shape, kept in step by hand. See db/migrations/00027_alert_rules.sql.
func LoadAlertRules(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]byte, error) {
	var rules []byte
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT rules FROM alert_rules WHERE tenant_id = $1`, id.TenantID).Scan(&rules)
	})
	// Never configured is not an error — it is a new account, and the caller
	// answers it with the documented defaults.
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: load alert rules: %w", err)
	}
	return rules, nil
}

// SaveAlertRules writes the tenant's complete rule document.
//
// The whole document, not a partial merge: the caller has already merged the
// incoming patch onto what was stored, because merging is a contract-shape
// concern and this layer deliberately does not know that shape.
func SaveAlertRules(ctx context.Context, pool *pgxpool.Pool, id Identity, rules []byte) error {
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO alert_rules (tenant_id, rules, updated_at)
			VALUES ($1, $2, now())
			ON CONFLICT (tenant_id) DO UPDATE
			  SET rules = EXCLUDED.rules, updated_at = now()`,
			id.TenantID, rules)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: save alert rules: %w", err)
	}
	return nil
}
