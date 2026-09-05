package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// APIKey is a credential for the public send API. The secret itself is present
// only on the one response that creates or rotates it.
type APIKey struct {
	ID          uuid.UUID
	Name        string
	Environment string
	Scopes      []string
	KeyPrefix   string
	Status      string
	LastUsedAt  *time.Time
	CreatedAt   time.Time
	// Secret is populated only when the key is minted. It is never read back
	// from the database, because only its hash is stored there.
	Secret string
}

// generateSecret mints a key. 32 bytes of crypto/rand is well past guessing
// range, and the environment is part of the visible prefix so a test key
// pasted into production config is obvious at a glance rather than at 3am.
func generateSecret(environment string) (secret, prefix string, hash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", nil, fmt.Errorf("store: generate key: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	kind := "live"
	if environment == "test" {
		kind = "test"
	}
	secret = "sk_" + kind + "_" + body
	// The prefix must be long enough to tell keys apart in a list but far too
	// short to narrow a brute-force search.
	prefix = secret[:len("sk_live_")+6]
	sum := sha256.Sum256([]byte(secret))
	return secret, prefix, sum[:], nil
}

// ListAPIKeys returns the tenant's keys for one environment.
//
// The environment is required, not optional. Live and test credentials are
// different secrets with different blast radii, and the screen that shows them
// is explicitly one or the other. Listing both together — which is what this
// did before, because the parameter was never read — puts live keys on the
// test-mode page.
func ListAPIKeys(ctx context.Context, pool *pgxpool.Pool, id Identity, environment string) ([]APIKey, error) {
	var out []APIKey
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, name, environment, scopes, key_prefix, status, last_used_at, created_at
			FROM api_keys WHERE environment = $1 ORDER BY created_at DESC`, environment)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key APIKey
			if err := rows.Scan(&key.ID, &key.Name, &key.Environment, &key.Scopes,
				&key.KeyPrefix, &key.Status, &key.LastUsedAt, &key.CreatedAt); err != nil {
				return err
			}
			out = append(out, key)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: list api keys: %w", err)
	}
	return out, nil
}

func CreateAPIKey(ctx context.Context, pool *pgxpool.Pool, id Identity,
	name, environment string, scopes []string) (APIKey, error) {

	secret, prefix, hash, err := generateSecret(environment)
	if err != nil {
		return APIKey{}, err
	}
	if scopes == nil {
		scopes = []string{}
	}

	var key APIKey
	err = WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO api_keys (tenant_id, name, environment, scopes, key_prefix, key_hash)
			VALUES ($1,$2,$3,$4,$5,$6)
			RETURNING id, name, environment, scopes, key_prefix, status, last_used_at, created_at`,
			id.TenantID, name, environment, scopes, prefix, hash,
		).Scan(&key.ID, &key.Name, &key.Environment, &key.Scopes, &key.KeyPrefix,
			&key.Status, &key.LastUsedAt, &key.CreatedAt)
	})
	if err != nil {
		return APIKey{}, fmt.Errorf("store: create api key: %w", err)
	}
	key.Secret = secret
	return key, nil
}

// RotateAPIKey issues a new secret for an existing key.
//
// The old secret stops working the instant this returns. That is the point of
// rotation — a rotation that left the previous secret valid would be useless
// for the case it exists to handle, which is a leak.
func RotateAPIKey(ctx context.Context, pool *pgxpool.Pool, id Identity,
	keyID uuid.UUID) (APIKey, error) {

	var key APIKey
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var environment string
		if err := tx.QueryRow(ctx,
			`SELECT environment FROM api_keys WHERE id = $1`, keyID).Scan(&environment); err != nil {
			return err
		}
		secret, prefix, hash, err := generateSecret(environment)
		if err != nil {
			return err
		}
		key.Secret = secret
		return tx.QueryRow(ctx, `
			UPDATE api_keys SET key_prefix = $2, key_hash = $3, status = 'active'
			WHERE id = $1
			RETURNING id, name, environment, scopes, key_prefix, status, last_used_at, created_at`,
			keyID, prefix, hash,
		).Scan(&key.ID, &key.Name, &key.Environment, &key.Scopes, &key.KeyPrefix,
			&key.Status, &key.LastUsedAt, &key.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, fmt.Errorf("store: rotate api key: %w", err)
	}
	return key, nil
}

func RevokeAPIKey(ctx context.Context, pool *pgxpool.Pool, id Identity, keyID uuid.UUID) error {
	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE api_keys SET status = 'revoked' WHERE id = $1`, keyID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ResolveAPIKey authenticates a bearer key. It calls a SECURITY DEFINER
// function for the same reason session resolution does: RLS cannot be satisfied
// before the tenant is known, and this key is what establishes it.
// The environment is returned, not discarded: a test key and a live key are
// throttled differently, so whoever enforces the limit has to know which one
// this is.
func ResolveAPIKey(ctx context.Context, pool *pgxpool.Pool, secret string) (
	Identity, []string, string, error) {

	sum := sha256.Sum256([]byte(secret))
	var identity Identity
	var scopes []string
	var environment string
	err := pool.QueryRow(ctx,
		`SELECT key_id, tenant_id, scopes, environment FROM resolve_api_key($1)`, sum[:],
	).Scan(&identity.SessionID, &identity.TenantID, &scopes, &environment)
	if errors.Is(err, pgx.ErrNoRows) {
		return Identity{}, nil, "", ErrNotFound
	}
	if err != nil {
		return Identity{}, nil, "", fmt.Errorf("store: resolve api key: %w", err)
	}
	return identity, scopes, environment, nil
}

// WebhookEndpoint is where we POST delivery events.
type WebhookEndpoint struct {
	ID                  uuid.UUID
	Environment         string
	URL                 string
	SubscribedEvents    []string
	SigningSecretPrefix string
	Status              string
	CreatedAt           time.Time
	// SigningSecret is returned only when minted, like an API key secret.
	SigningSecret string
}

// ListWebhooks returns the tenant's endpoints for one environment. Scoped for
// the same reason as ListAPIKeys: a test endpoint and a live endpoint receive
// different traffic, and showing both on one screen invites someone to point
// production deliveries at a staging URL.
// A nil environment means "every environment", which is what the by-id lookups
// need: an endpoint is addressed by its id alone, and the caller does not know
// which environment it lives in until it has been read.
func ListWebhooks(ctx context.Context, pool *pgxpool.Pool, id Identity, environment *string) ([]WebhookEndpoint, error) {
	var out []WebhookEndpoint
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, environment, url, subscribed_events, signing_secret_prefix,
			       status, created_at
			FROM webhook_endpoints
			WHERE ($1::text IS NULL OR environment = $1)
			ORDER BY created_at DESC`, environment)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var hook WebhookEndpoint
			if err := rows.Scan(&hook.ID, &hook.Environment, &hook.URL,
				&hook.SubscribedEvents, &hook.SigningSecretPrefix, &hook.Status,
				&hook.CreatedAt); err != nil {
				return err
			}
			out = append(out, hook)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: list webhooks: %w", err)
	}
	return out, nil
}

func CreateWebhook(ctx context.Context, pool *pgxpool.Pool, id Identity,
	environment, url string, events []string) (WebhookEndpoint, error) {

	secret, prefix, hash, err := generateSecret(environment)
	if err != nil {
		return WebhookEndpoint{}, err
	}
	if events == nil {
		events = []string{}
	}

	var hook WebhookEndpoint
	err = WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO webhook_endpoints (tenant_id, environment, url,
			    subscribed_events, signing_secret_prefix, signing_secret_hash)
			VALUES ($1,$2,$3,$4,$5,$6)
			RETURNING id, environment, url, subscribed_events, signing_secret_prefix,
			          status, created_at`,
			id.TenantID, environment, url, events, prefix, hash,
		).Scan(&hook.ID, &hook.Environment, &hook.URL, &hook.SubscribedEvents,
			&hook.SigningSecretPrefix, &hook.Status, &hook.CreatedAt)
	})
	if err != nil {
		return WebhookEndpoint{}, fmt.Errorf("store: create webhook: %w", err)
	}
	hook.SigningSecret = secret
	return hook, nil
}

func UpdateWebhook(ctx context.Context, pool *pgxpool.Pool, id Identity,
	hookID uuid.UUID, url *string, events []string, status *string) (WebhookEndpoint, error) {

	var hook WebhookEndpoint
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			UPDATE webhook_endpoints SET
			    url = COALESCE($2, url),
			    subscribed_events = COALESCE($3, subscribed_events),
			    status = COALESCE($4, status)
			WHERE id = $1
			RETURNING id, environment, url, subscribed_events, signing_secret_prefix,
			          status, created_at`,
			hookID, url, events, status,
		).Scan(&hook.ID, &hook.Environment, &hook.URL, &hook.SubscribedEvents,
			&hook.SigningSecretPrefix, &hook.Status, &hook.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return WebhookEndpoint{}, ErrNotFound
	}
	if err != nil {
		return WebhookEndpoint{}, fmt.Errorf("store: update webhook: %w", err)
	}
	return hook, nil
}

func DeleteWebhook(ctx context.Context, pool *pgxpool.Pool, id Identity, hookID uuid.UUID) error {
	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM webhook_endpoints WHERE id = $1`, hookID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// WebhookDelivery is one attempt at delivering one event.
type WebhookDelivery struct {
	ID              uuid.UUID
	EndpointID      uuid.UUID
	EventType       string
	Attempt         int
	Outcome         string
	HTTPStatus      *int
	ResponseSnippet *string
	OccurredAt      time.Time
	Payload         []byte
}

// ListWebhookEvents pages a endpoint's delivery history.
//
// It used to take a limit and nothing else: the route declared a cursor, the
// handler never read it, and the response never carried one — so nobody could
// see past the newest 50 deliveries of an endpoint, which is exactly when you
// need the history. It now pages and reports a total like every other list.
func ListWebhookEvents(ctx context.Context, pool *pgxpool.Pool, id Identity,
	hookID uuid.UUID, page, limit int) ([]WebhookDelivery, int, error) {

	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := pageOffset(page, limit)
	var out []WebhookDelivery
	var total int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM webhook_events WHERE endpoint_id = $1`,
			hookID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, endpoint_id, event_type, attempt, outcome, http_status,
			       response_snippet, occurred_at
			FROM webhook_events WHERE endpoint_id = $1
			ORDER BY occurred_at DESC, id DESC LIMIT $2 OFFSET $3`, hookID, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event WebhookDelivery
			if err := rows.Scan(&event.ID, &event.EndpointID, &event.EventType,
				&event.Attempt, &event.Outcome, &event.HTTPStatus,
				&event.ResponseSnippet, &event.OccurredAt); err != nil {
				return err
			}
			out = append(out, event)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, fmt.Errorf("store: list webhook events: %w", err)
	}
	return out, total, nil
}

func RecordWebhookEvent(ctx context.Context, pool *pgxpool.Pool, id Identity,
	event WebhookDelivery) (WebhookDelivery, error) {

	if event.Payload == nil {
		event.Payload = []byte("{}")
	}
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO webhook_events (tenant_id, endpoint_id, event_type, payload,
			    attempt, outcome, http_status, response_snippet)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			RETURNING id, endpoint_id, event_type, attempt, outcome, http_status,
			          response_snippet, occurred_at`,
			id.TenantID, event.EndpointID, event.EventType, event.Payload,
			event.Attempt, event.Outcome, event.HTTPStatus, event.ResponseSnippet,
		).Scan(&event.ID, &event.EndpointID, &event.EventType, &event.Attempt,
			&event.Outcome, &event.HTTPStatus, &event.ResponseSnippet, &event.OccurredAt)
	})
	if err != nil {
		return WebhookDelivery{}, fmt.Errorf("store: record webhook event: %w", err)
	}
	return event, nil
}

// GetWebhookEvent reads one delivery so it can be replayed.
func GetWebhookEvent(ctx context.Context, pool *pgxpool.Pool, id Identity,
	eventID uuid.UUID) (WebhookDelivery, error) {

	var event WebhookDelivery
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id, endpoint_id, event_type, attempt, outcome, http_status,
			       response_snippet, occurred_at, payload
			FROM webhook_events WHERE id = $1`, eventID,
		).Scan(&event.ID, &event.EndpointID, &event.EventType, &event.Attempt,
			&event.Outcome, &event.HTTPStatus, &event.ResponseSnippet,
			&event.OccurredAt, &event.Payload)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return WebhookDelivery{}, ErrNotFound
	}
	if err != nil {
		return WebhookDelivery{}, fmt.Errorf("store: get webhook event: %w", err)
	}
	return event, nil
}

// IPAllowEntry restricts which addresses may use this tenant's API keys.
type IPAllowEntry struct {
	ID          uuid.UUID
	Environment string
	CIDR        string
	Label       *string
	CreatedAt   time.Time
}

// ListIPAllowlist returns the allowed source ranges for one environment. An
// allowlist shown for the wrong environment is actively dangerous: it reads as
// "these addresses may call us" for a set of addresses that in fact may not.
func ListIPAllowlist(ctx context.Context, pool *pgxpool.Pool, id Identity, environment string) ([]IPAllowEntry, error) {
	var out []IPAllowEntry
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, environment, cidr, label, created_at FROM ip_allowlist
			 WHERE environment = $1 ORDER BY created_at DESC`, environment)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var entry IPAllowEntry
			if err := rows.Scan(&entry.ID, &entry.Environment, &entry.CIDR,
				&entry.Label, &entry.CreatedAt); err != nil {
				return err
			}
			out = append(out, entry)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: list ip allowlist: %w", err)
	}
	return out, nil
}

func AddIPAllowEntry(ctx context.Context, pool *pgxpool.Pool, id Identity,
	environment, cidr string, label *string) (IPAllowEntry, error) {

	var entry IPAllowEntry
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO ip_allowlist (tenant_id, environment, cidr, label)
			VALUES ($1,$2,$3,$4)
			RETURNING id, environment, cidr, label, created_at`,
			id.TenantID, environment, cidr, label,
		).Scan(&entry.ID, &entry.Environment, &entry.CIDR, &entry.Label, &entry.CreatedAt)
	})
	if err != nil {
		return IPAllowEntry{}, fmt.Errorf("store: add ip allow entry: %w", err)
	}
	return entry, nil
}

func DeleteIPAllowEntry(ctx context.Context, pool *pgxpool.Pool, id Identity,
	entryID uuid.UUID) error {

	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM ip_allowlist WHERE id = $1`, entryID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}
