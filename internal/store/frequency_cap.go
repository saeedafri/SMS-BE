package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FrequencyCap is a tenant's per-recipient ceiling on one channel. A nil limit
// is no limit for that window; the zero value of the struct caps nothing.
type FrequencyCap struct {
	Channel         string
	DailyLimit      *int
	WeeklyLimit     *int
	MonthlyLimit    *int
	ExcludedNumbers []string
}

// Active says whether the cap limits anything at all.
func (c FrequencyCap) Active() bool {
	return c.DailyLimit != nil || c.WeeklyLimit != nil || c.MonthlyLimit != nil
}

// Excludes says whether a recipient is exempt from the cap.
func (c FrequencyCap) Excludes(msisdn string) bool {
	for _, number := range c.ExcludedNumbers {
		if number == msisdn {
			return true
		}
	}
	return false
}

// ListFrequencyCaps returns every cap the tenant has set, one per channel.
func ListFrequencyCaps(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]FrequencyCap, error) {
	caps := []FrequencyCap{}
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT channel, daily_limit, weekly_limit, monthly_limit, excluded_numbers
			FROM frequency_caps ORDER BY channel`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c FrequencyCap
			if err := rows.Scan(&c.Channel, &c.DailyLimit, &c.WeeklyLimit,
				&c.MonthlyLimit, &c.ExcludedNumbers); err != nil {
				return err
			}
			caps = append(caps, c)
		}
		return rows.Err()
	})
	return caps, err
}

// CachedFrequencyCap is the send path's read: one channel's cap, through the
// hot cache. A tenant with no row gets the zero cap, which limits nothing.
func CachedFrequencyCap(ctx context.Context, pool *pgxpool.Pool, cache *HotCache,
	id Identity, channel string) (FrequencyCap, error) {

	key := FrequencyCapKey(id.TenantID, channel)
	if value, found := cache.Get(key); found {
		return value.(FrequencyCap), nil
	}
	var c FrequencyCap
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT channel, daily_limit, weekly_limit, monthly_limit, excluded_numbers
			FROM frequency_caps WHERE channel = $1`, channel).
			Scan(&c.Channel, &c.DailyLimit, &c.WeeklyLimit, &c.MonthlyLimit, &c.ExcludedNumbers)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		c, err = FrequencyCap{Channel: channel}, nil
	}
	if err != nil {
		return FrequencyCap{}, err
	}
	cache.Put(key, c)
	return c, nil
}

// FrequencyCapKey is exported so a save takes effect on the next send rather
// than when the cache entry expires.
func FrequencyCapKey(tenantID uuid.UUID, channel string) string {
	return "frequency-cap:" + tenantID.String() + ":" + channel
}

// SaveFrequencyCap writes one channel's cap. A cap with no limits and no
// exclusions is the same as none, so it deletes the row.
func SaveFrequencyCap(ctx context.Context, pool *pgxpool.Pool, cache *HotCache,
	id Identity, c FrequencyCap) error {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if !c.Active() && len(c.ExcludedNumbers) == 0 {
			_, err := tx.Exec(ctx, `DELETE FROM frequency_caps WHERE channel = $1`, c.Channel)
			return err
		}
		excluded := c.ExcludedNumbers
		if excluded == nil {
			excluded = []string{}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO frequency_caps
			    (tenant_id, channel, daily_limit, weekly_limit, monthly_limit, excluded_numbers)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (tenant_id, channel) DO UPDATE SET
			    daily_limit = EXCLUDED.daily_limit, weekly_limit = EXCLUDED.weekly_limit,
			    monthly_limit = EXCLUDED.monthly_limit,
			    excluded_numbers = EXCLUDED.excluded_numbers, updated_at = now()`,
			id.TenantID, c.Channel, c.DailyLimit, c.WeeklyLimit, c.MonthlyLimit, excluded)
		return err
	})
	if err == nil {
		cache.Forget(FrequencyCapKey(id.TenantID, c.Channel))
	}
	return err
}
