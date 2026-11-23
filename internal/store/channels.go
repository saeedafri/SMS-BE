package store

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChannelEnabledByDefault is a channel's state for a tenant the operator has
// never switched: the two channels that sent before per-tenant enablement
// existed stay on, every other one starts off.
func ChannelEnabledByDefault(channel string) bool {
	return channel == "SMS" || channel == "RCS"
}

// TenantChannels says, per channel, whether the operator has switched it on for
// this tenant. Every channel is present: one never switched takes its default.
func TenantChannels(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID,
	channels []string) (map[string]bool, error) {

	enabled := make(map[string]bool, len(channels))
	for _, channel := range channels {
		enabled[channel] = ChannelEnabledByDefault(channel)
	}
	err := WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT channel, enabled FROM tenant_channels`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var channel string
			var on bool
			if err := rows.Scan(&channel, &on); err != nil {
				return err
			}
			enabled[channel] = on
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: tenant channels: %w", err)
	}
	return enabled, nil
}

// CachedTenantChannels is TenantChannels behind the hot cache, for the send
// path. SetTenantChannel's caller forgets the entry, so a switch bites on the
// next send.
func CachedTenantChannels(ctx context.Context, pool *pgxpool.Pool, cache *HotCache,
	tenantID uuid.UUID, channels []string) (map[string]bool, error) {

	key := TenantChannelsKey(tenantID)
	if value, found := cache.Get(key); found {
		return value.(map[string]bool), nil
	}
	enabled, err := TenantChannels(ctx, pool, tenantID, channels)
	if err != nil {
		return nil, err
	}
	cache.Put(key, enabled)
	return enabled, nil
}

func TenantChannelsKey(tenantID uuid.UUID) string {
	return "tenant-channels:" + tenantID.String()
}

// SetTenantChannel switches one channel on or off for a tenant.
func SetTenantChannel(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID,
	channel string, enabled bool) error {

	return WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO tenant_channels (tenant_id, channel, enabled) VALUES ($1, $2, $3)
			ON CONFLICT (tenant_id, channel)
			DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()`,
			tenantID, channel, enabled)
		return err
	})
}

// ChannelCheck is the last go-live check a channel ran.
type ChannelCheck struct {
	CheckedAt time.Time
	Passed    bool
}

// LastChannelChecks returns the last check of every channel that has run one.
func LastChannelChecks(ctx context.Context, pool *pgxpool.Pool) (map[string]ChannelCheck, error) {
	rows, err := pool.Query(ctx, `SELECT channel, checked_at, passed FROM channel_checks`)
	if err != nil {
		return nil, fmt.Errorf("store: channel checks: %w", err)
	}
	defer rows.Close()
	checks := map[string]ChannelCheck{}
	for rows.Next() {
		var channel string
		var check ChannelCheck
		if err := rows.Scan(&channel, &check.CheckedAt, &check.Passed); err != nil {
			return nil, fmt.Errorf("store: channel checks: %w", err)
		}
		checks[channel] = check
	}
	return checks, rows.Err()
}

// SaveChannelCheck records a channel's latest go-live check.
func SaveChannelCheck(ctx context.Context, pool *pgxpool.Pool, channel string, check ChannelCheck) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO channel_checks (channel, checked_at, passed) VALUES ($1, $2, $3)
		ON CONFLICT (channel) DO UPDATE SET checked_at = EXCLUDED.checked_at, passed = EXCLUDED.passed`,
		channel, check.CheckedAt, check.Passed)
	if err != nil {
		return fmt.Errorf("store: save channel check: %w", err)
	}
	return nil
}

// ChannelEvidence is what the message log says a channel has really carried,
// across every tenant: simulated messages never count.
type ChannelEvidence struct {
	// Accepted is messages a carrier took (it returned a reference).
	Accepted uint64
	// Receipted is messages a delivery receipt settled.
	Receipted uint64
	// Unclassified is failed messages that carry no reason at all.
	Unclassified uint64
}

func QueryChannelEvidence(ctx context.Context, conn driver.Conn, channel string,
	since time.Time) (ChannelEvidence, error) {

	var evidence ChannelEvidence
	err := conn.QueryRow(ctx, `
		SELECT countIf(carrier_ref IS NOT NULL),
		       countIf(dlr_received_at IS NOT NULL OR status = 'delivered'),
		       countIf(status IN ('undelivered', 'carrier_rejected', 'expired') AND error_code IS NULL)
		FROM messages FINAL
		WHERE channel = ? AND simulated = false AND created_at >= ?`,
		channel, since).Scan(&evidence.Accepted, &evidence.Receipted, &evidence.Unclassified)
	if err != nil {
		return ChannelEvidence{}, fmt.Errorf("store: channel evidence: %w", err)
	}
	return evidence, nil
}

// SandboxCharge is one message the sandbox carried that its tenant paid for.
type SandboxCharge struct {
	TenantID    uuid.UUID
	MessageID   uuid.UUID
	AmountMinor int64
	Currency    string
}

// SandboxCharges finds every message the sandbox carried and still holds a
// charge for: a sandbox carrier reference and a cost above 0. Across tenants.
func SandboxCharges(ctx context.Context, conn driver.Conn) ([]SandboxCharge, error) {
	rows, err := conn.Query(ctx, `
		SELECT tenant_id, id, cost_minor, currency FROM messages FINAL
		WHERE carrier_ref LIKE 'sbx-%' AND cost_minor > 0 AND simulated = false
		ORDER BY tenant_id, currency, created_at`)
	if err != nil {
		return nil, fmt.Errorf("store: sandbox charges: %w", err)
	}
	defer rows.Close()
	var out []SandboxCharge
	for rows.Next() {
		var charge SandboxCharge
		if err := rows.Scan(&charge.TenantID, &charge.MessageID, &charge.AmountMinor,
			&charge.Currency); err != nil {
			return nil, fmt.Errorf("store: sandbox charges: %w", err)
		}
		out = append(out, charge)
	}
	return out, rows.Err()
}

// RefundSandboxCharges refunds one tenant's charges in one currency as a
// single compensating ledger entry, each message at most once ever. The marks
// and the refund share a transaction: neither can land without the other.
// It returns how many messages and how much were refunded now.
func RefundSandboxCharges(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID,
	currency string, charges []SandboxCharge) (int, int64, error) {

	count, total := 0, int64(0)
	err := WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		for _, charge := range charges {
			tag, err := tx.Exec(ctx, `
				INSERT INTO sandbox_refunds (message_id, tenant_id, amount_minor, currency)
				VALUES ($1, $2, $3, $4) ON CONFLICT (message_id) DO NOTHING`,
				charge.MessageID, tenantID, charge.AmountMinor, currency)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				count++
				total += charge.AmountMinor
			}
		}
		if total == 0 {
			return nil
		}
		_, err := appendLedgerInTx(ctx, tx, Identity{TenantID: tenantID}, LedgerEntry{
			Currency: currency, Type: "refund", AmountMinor: total,
			Description: fmt.Sprintf("Refund of %d message(s) the sandbox carried and never "+
				"delivered to a handset (reason: sandbox_refund)", count),
		})
		return err
	})
	if err != nil {
		return 0, 0, fmt.Errorf("store: refund sandbox charges: %w", err)
	}
	return count, total, nil
}
