package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WebhookIntegration says an endpoint is a customer's account at a vendor, and
// holds the sealed request headers (their API key) it needs.
type WebhookIntegration struct {
	EndpointID    uuid.UUID
	Type          string
	SealedHeaders *string
}

// GetWebhookIntegration reads an endpoint's integration. False when it is a
// plain Relay webhook, which is almost all of them.
func GetWebhookIntegration(ctx context.Context, pool *pgxpool.Pool, id Identity,
	endpointID uuid.UUID) (WebhookIntegration, bool, error) {

	var out WebhookIntegration
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT endpoint_id, integration_type, sealed_headers
			FROM webhook_integrations WHERE endpoint_id = $1`, endpointID).
			Scan(&out.EndpointID, &out.Type, &out.SealedHeaders)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return WebhookIntegration{}, false, nil
	}
	if err != nil {
		return WebhookIntegration{}, false, fmt.Errorf("store: get webhook integration: %w", err)
	}
	return out, true, nil
}

// SetWebhookIntegration declares an endpoint's integration, replacing any.
func SetWebhookIntegration(ctx context.Context, pool *pgxpool.Pool, id Identity,
	integration WebhookIntegration) error {

	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO webhook_integrations (endpoint_id, tenant_id, integration_type, sealed_headers)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (endpoint_id) DO UPDATE SET integration_type = EXCLUDED.integration_type,
			    sealed_headers = EXCLUDED.sealed_headers, updated_at = now()`,
			integration.EndpointID, id.TenantID, integration.Type, integration.SealedHeaders)
		return err
	})
}

// ClearWebhookIntegration turns an endpoint back into a plain Relay webhook.
func ClearWebhookIntegration(ctx context.Context, pool *pgxpool.Pool, id Identity,
	endpointID uuid.UUID) error {

	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM webhook_integrations WHERE endpoint_id = $1`, endpointID)
		return err
	})
}
