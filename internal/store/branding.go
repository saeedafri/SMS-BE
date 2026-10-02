package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Branding is a tenant's white-label settings.
type Branding struct {
	DisplayName, LogoURL, PrimaryColor, SecondaryColor, SupportEmail, CustomDomain *string
	DomainToken                                                                    string
	DomainVerifiedAt                                                               *time.Time
}

// ErrDomainTaken: another tenant already claims this domain.
var ErrDomainTaken = errors.New("store: domain already claimed")

const brandingColumns = `display_name, logo_url, primary_color, secondary_color, support_email,
	custom_domain, domain_token, domain_verified_at`

func scanBranding(row pgx.Row) (Branding, error) {
	var b Branding
	err := row.Scan(&b.DisplayName, &b.LogoURL, &b.PrimaryColor, &b.SecondaryColor, &b.SupportEmail,
		&b.CustomDomain, &b.DomainToken, &b.DomainVerifiedAt)
	return b, err
}

// GetBranding returns a tenant's settings; the zero value, with a fresh token
// on first save, when it has none.
func GetBranding(ctx context.Context, pool *pgxpool.Pool, id Identity) (Branding, bool, error) {
	var b Branding
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		b, err = scanBranding(tx.QueryRow(ctx, `SELECT `+brandingColumns+` FROM tenant_branding`))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Branding{}, false, nil
	}
	if err != nil {
		return Branding{}, false, fmt.Errorf("store: get branding: %w", err)
	}
	return b, true, nil
}

// SaveBranding writes the settings. Changing the domain drops its verification,
// since the proof was for the old name.
func SaveBranding(ctx context.Context, pool *pgxpool.Pool, id Identity, b Branding) (Branding, error) {
	var saved Branding
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		saved, err = scanBranding(tx.QueryRow(ctx, `
			INSERT INTO tenant_branding (tenant_id, display_name, logo_url, primary_color,
			    secondary_color, support_email, custom_domain)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (tenant_id) DO UPDATE SET display_name=EXCLUDED.display_name,
			    logo_url=EXCLUDED.logo_url, primary_color=EXCLUDED.primary_color,
			    secondary_color=EXCLUDED.secondary_color, support_email=EXCLUDED.support_email,
			    domain_verified_at = CASE WHEN tenant_branding.custom_domain IS NOT DISTINCT FROM
			        EXCLUDED.custom_domain THEN tenant_branding.domain_verified_at ELSE NULL END,
			    custom_domain=EXCLUDED.custom_domain, updated_at=now()
			RETURNING `+brandingColumns,
			id.TenantID, b.DisplayName, b.LogoURL, b.PrimaryColor, b.SecondaryColor,
			b.SupportEmail, b.CustomDomain))
		return err
	})
	if isUniqueViolation(err) {
		return Branding{}, ErrDomainTaken
	}
	if err != nil {
		return Branding{}, fmt.Errorf("store: save branding: %w", err)
	}
	return saved, nil
}

func MarkDomainVerified(ctx context.Context, pool *pgxpool.Pool, id Identity) error {
	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE tenant_branding SET domain_verified_at = now()
			WHERE custom_domain IS NOT NULL`)
		return err
	})
}

func DeleteBranding(ctx context.Context, pool *pgxpool.Pool, id Identity) error {
	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM tenant_branding`)
		return err
	})
}

// BrandingForHost is the public lookup: a VERIFIED custom domain only.
func BrandingForHost(ctx context.Context, operator *pgxpool.Pool, host string) (Branding, bool, error) {
	b, err := scanBranding(operator.QueryRow(ctx, `SELECT `+brandingColumns+` FROM tenant_branding
		WHERE custom_domain = $1 AND domain_verified_at IS NOT NULL`, host))
	if errors.Is(err, pgx.ErrNoRows) {
		return Branding{}, false, nil
	}
	if err != nil {
		return Branding{}, false, fmt.Errorf("store: branding for host: %w", err)
	}
	return b, true, nil
}
