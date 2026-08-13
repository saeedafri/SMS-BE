package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a lookup finds nothing. Callers translate it to
// the right HTTP status; the store layer does not know about HTTP.
var ErrNotFound = errors.New("store: not found")

// Identity is who the caller is, resolved once per request and carried in the
// request context.
type Identity struct {
	SessionID    uuid.UUID
	UserID       uuid.UUID
	TenantID     uuid.UUID
	Role         string
	Capabilities []string
	Name         string
	Email        string
	TenantName   string
	Country      string
	EmailVerd    bool
	MFAEnabled   bool
}

// ResolveSession maps a token hash to its session, tenant and user. It calls a
// SECURITY DEFINER function because RLS on `sessions` cannot be satisfied
// before the tenant is known — see migration 00004 for the full reasoning.
func ResolveSession(ctx context.Context, pool *pgxpool.Pool, tokenHash []byte) (Identity, error) {
	var id Identity
	err := pool.QueryRow(ctx,
		`SELECT session_id, tenant_id, user_id FROM resolve_session($1)`, tokenHash,
	).Scan(&id.SessionID, &id.TenantID, &id.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Identity{}, ErrNotFound
	}
	if err != nil {
		return Identity{}, fmt.Errorf("store: resolve session: %w", err)
	}
	return id, nil
}

// LoadIdentityDetail fills in everything GET /v1/me needs. It runs scoped, so
// RLS still applies to the tenant-owned rows it touches.
func LoadIdentityDetail(ctx context.Context, pool *pgxpool.Pool, id Identity) (Identity, error) {
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT u.name, u.email::text, u.email_verified, u.mfa_enabled,
			       t.name, t.country, t.capabilities, tu.role
			FROM users u
			JOIN tenant_users tu ON tu.user_id = u.id AND tu.tenant_id = $1
			JOIN tenants t       ON t.id = tu.tenant_id
			WHERE u.id = $2`,
			id.TenantID, id.UserID,
		).Scan(&id.Name, &id.Email, &id.EmailVerd, &id.MFAEnabled,
			&id.TenantName, &id.Country, &id.Capabilities, &id.Role)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Identity{}, ErrNotFound
	}
	if err != nil {
		return Identity{}, fmt.Errorf("store: load identity: %w", err)
	}
	return id, nil
}

// TouchSession records activity so GET /v1/sessions shows something truthful.
// Callers rate-limit how often they call this; every request would be a write
// per request, which is a lot of WAL for a column nobody reads in real time.
func TouchSession(ctx context.Context, pool *pgxpool.Pool, id Identity) error {
	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE sessions SET last_active_at = now() WHERE id = $1`, id.SessionID)
		return err
	})
}

// UpdateUserName changes the signed-in user's display name.
func UpdateUserName(ctx context.Context, pool *pgxpool.Pool, id Identity, name string) error {
	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE users SET name = $1, updated_at = now() WHERE id = $2`, name, id.UserID)
		return err
	})
}

// UpdateTenantName renames the tenant/organisation.
func UpdateTenantName(ctx context.Context, pool *pgxpool.Pool, id Identity, name string) error {
	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE tenants SET name = $1, updated_at = now() WHERE id = $2`, name, id.TenantID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// TenantStatus reports whether a tenant is active, suspended or throttled. The
// send gate reads it on every message so an operator suspension takes effect
// immediately rather than at the next campaign.
func TenantStatus(ctx context.Context, pool *pgxpool.Pool, id Identity) (string, error) {
	var status string
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM tenants WHERE id = $1`, id.TenantID).Scan(&status)
	})
	if err != nil {
		return "", fmt.Errorf("store: tenant status: %w", err)
	}
	return status, nil
}
