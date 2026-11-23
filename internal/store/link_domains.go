package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LinkDomain is a tenant's own short-link domain and where its setup stands.
type LinkDomain struct {
	ID            uuid.UUID
	Domain        string
	Status        string
	VerifyToken   string
	CNAMEStatus   string
	TXTStatus     string
	FailureReason *string
	CreatedAt     time.Time
}

const linkDomainColumns = `id, domain, status, verify_token, cname_status, txt_status,
	failure_reason, created_at`

func scanLinkDomain(row pgx.Row) (LinkDomain, error) {
	var d LinkDomain
	err := row.Scan(&d.ID, &d.Domain, &d.Status, &d.VerifyToken, &d.CNAMEStatus, &d.TXTStatus,
		&d.FailureReason, &d.CreatedAt)
	return d, err
}

// ListLinkDomains pages a tenant's domains, newest first.
func ListLinkDomains(ctx context.Context, pool *pgxpool.Pool, id Identity, page, limit int) (
	[]LinkDomain, int, error) {

	var out []LinkDomain
	var total int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM link_domains`).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+linkDomainColumns+` FROM link_domains
			ORDER BY created_at DESC, id LIMIT $1 OFFSET $2`, limit, pageOffset(page, limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			domain, err := scanLinkDomain(rows)
			if err != nil {
				return err
			}
			out = append(out, domain)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, fmt.Errorf("store: list link domains: %w", err)
	}
	return out, total, nil
}

// CreateLinkDomain claims a host for a tenant. A host already claimed by
// anyone is a conflict.
func CreateLinkDomain(ctx context.Context, pool *pgxpool.Pool, id Identity, domain string) (LinkDomain, error) {
	var created LinkDomain
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		created, err = scanLinkDomain(tx.QueryRow(ctx, `
			INSERT INTO link_domains (tenant_id, domain) VALUES ($1, $2)
			RETURNING `+linkDomainColumns, id.TenantID, domain))
		return err
	})
	if isUniqueViolation(err) {
		return LinkDomain{}, ErrConflict
	}
	if err != nil {
		return LinkDomain{}, fmt.Errorf("store: create link domain: %w", err)
	}
	return created, nil
}

// GetLinkDomain reads one of a tenant's domains.
func GetLinkDomain(ctx context.Context, pool *pgxpool.Pool, id Identity, domainID uuid.UUID) (LinkDomain, error) {
	var domain LinkDomain
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		domain, err = scanLinkDomain(tx.QueryRow(ctx,
			`SELECT `+linkDomainColumns+` FROM link_domains WHERE id = $1`, domainID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return LinkDomain{}, ErrNotFound
	}
	if err != nil {
		return LinkDomain{}, fmt.Errorf("store: get link domain: %w", err)
	}
	return domain, nil
}

// SaveLinkDomainCheck records what a validation found.
func SaveLinkDomainCheck(ctx context.Context, pool *pgxpool.Pool, id Identity, domain LinkDomain) error {
	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE link_domains SET status = $2, cname_status = $3, txt_status = $4,
			    failure_reason = $5 WHERE id = $1`,
			domain.ID, domain.Status, domain.CNAMEStatus, domain.TXTStatus, domain.FailureReason)
		return err
	})
}

// DeleteLinkDomain removes a domain; one a short link still uses is a conflict.
func DeleteLinkDomain(ctx context.Context, pool *pgxpool.Pool, id Identity, domainID uuid.UUID) error {
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM link_domains WHERE id = $1`, domainID)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return err
	})
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, ErrNotFound):
		return ErrNotFound
	case errors.As(err, &pgErr) && pgErr.Code == "23503":
		return ErrConflict
	case err != nil:
		return fmt.Errorf("store: delete link domain: %w", err)
	}
	return nil
}

// SaveLookup keeps the last line-type answer for a number.
func SaveLookup(ctx context.Context, pool *pgxpool.Pool, id Identity, msisdn string,
	result any, checkedAt time.Time) error {

	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO number_lookups (tenant_id, msisdn, result, checked_at) VALUES ($1, $2, $3, $4)
			ON CONFLICT (tenant_id, msisdn) DO UPDATE SET result = EXCLUDED.result,
			    checked_at = EXCLUDED.checked_at`, id.TenantID, msisdn, encoded, checkedAt)
		return err
	})
}

// LookupsFor reads the last answers for these numbers, keyed by number.
func LookupsFor(ctx context.Context, pool *pgxpool.Pool, id Identity, msisdns []string) (
	map[string]json.RawMessage, error) {

	out := map[string]json.RawMessage{}
	if len(msisdns) == 0 {
		return out, nil
	}
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT msisdn, result FROM number_lookups WHERE msisdn = ANY($1)`, msisdns)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var msisdn string
			var result json.RawMessage
			if err := rows.Scan(&msisdn, &result); err != nil {
				return err
			}
			out[msisdn] = result
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: lookups: %w", err)
	}
	return out, nil
}
