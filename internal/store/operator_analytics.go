package store

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Platform-wide analytics for the operator console.
//
// Every query here is deliberately NOT filtered by tenant — that is the whole
// point of an operator report. They are kept in their own file so no tenant
// query can pick one up by accident: a missing WHERE tenant_id in this file is
// correct, and in every other store file it is a data breach.

// PlatformUsageRow is one grouped count in the usage report.
type PlatformUsageRow struct {
	Key   string
	Count uint64
}

// PlatformUsage is the operator's usage report across all tenants.
type PlatformUsage struct {
	Total     uint64
	ByDay     []PlatformUsageRow
	ByChannel []PlatformUsageRow
	ByCountry []PlatformUsageRow
	ByTenant  []PlatformUsageRow
}

// QueryPlatformUsage counts messages across every tenant since a cutoff.
//
// It counts ATTEMPTS (accepted, submitted, rejected) rather than every rollup
// row, for the same reason the tenant report does: the rollup holds one row per
// transition, so summing all of them reports a message three times.
func QueryPlatformUsage(ctx context.Context, conn driver.Conn, since time.Time) (
	PlatformUsage, error) {

	const attempted = `status IN ('accepted','submitted','rejected')`
	var usage PlatformUsage

	if err := conn.QueryRow(ctx, `
		SELECT sumIf(message_count, `+attempted+`)
		FROM message_rollup_hourly WHERE hour >= ?`, since).Scan(&usage.Total); err != nil {
		return PlatformUsage{}, fmt.Errorf("store: platform usage total: %w", err)
	}

	grouped := func(column string) ([]PlatformUsageRow, error) {
		rows, err := conn.Query(ctx, fmt.Sprintf(`
			SELECT toString(%s) AS bucket, sumIf(message_count, %s) AS total
			FROM message_rollup_hourly WHERE hour >= ?
			GROUP BY bucket HAVING total > 0 ORDER BY bucket`, column, attempted), since)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []PlatformUsageRow{}
		for rows.Next() {
			var row PlatformUsageRow
			if err := rows.Scan(&row.Key, &row.Count); err != nil {
				return nil, err
			}
			out = append(out, row)
		}
		return out, rows.Err()
	}

	var err error
	if usage.ByDay, err = grouped("toDate(hour)"); err != nil {
		return PlatformUsage{}, fmt.Errorf("store: usage by day: %w", err)
	}
	if usage.ByChannel, err = grouped("channel"); err != nil {
		return PlatformUsage{}, fmt.Errorf("store: usage by channel: %w", err)
	}
	if usage.ByCountry, err = grouped("country"); err != nil {
		return PlatformUsage{}, fmt.Errorf("store: usage by country: %w", err)
	}
	if usage.ByTenant, err = grouped("tenant_id"); err != nil {
		return PlatformUsage{}, fmt.Errorf("store: usage by tenant: %w", err)
	}
	return usage, nil
}

// PlatformRevenueRow is revenue and volume for one grouping key.
type PlatformRevenueRow struct {
	Key      string
	Currency string
	Revenue  int64
	Segments uint64
}

// QueryPlatformRevenue returns what tenants were charged, grouped three ways.
//
// Revenue counts DELIVERED messages only — the same basis the tenant-facing
// cost figure uses. Billing for a message that never arrived is the behaviour
// this product exists to compete against, so the margin report must not quietly
// assume it either.
func QueryPlatformRevenue(ctx context.Context, conn driver.Conn, since time.Time,
	column string) ([]PlatformRevenueRow, error) {

	rows, err := conn.Query(ctx, fmt.Sprintf(`
		SELECT toString(%s) AS bucket, currency,
		       sumIf(cost_minor, status = 'delivered')    AS revenue,
		       sumIf(segment_count, status = 'delivered') AS segments
		FROM message_rollup_hourly WHERE hour >= ?
		GROUP BY bucket, currency HAVING revenue > 0 ORDER BY bucket`, column), since)
	if err != nil {
		return nil, fmt.Errorf("store: platform revenue: %w", err)
	}
	defer rows.Close()
	out := []PlatformRevenueRow{}
	for rows.Next() {
		var row PlatformRevenueRow
		if err := rows.Scan(&row.Key, &row.Currency, &row.Revenue, &row.Segments); err != nil {
			return nil, fmt.Errorf("store: scan revenue row: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// TenantNames maps ids to names for report labels, so a report can show
// "Acme Retail" where the warehouse only knows a uuid.
func TenantNames(ctx context.Context, pool *pgxpool.Pool) (map[string]string, error) {
	rows, err := pool.Query(ctx, `SELECT id, name FROM tenants`)
	if err != nil {
		return nil, fmt.Errorf("store: tenant names: %w", err)
	}
	defer rows.Close()
	names := map[string]string{}
	for rows.Next() {
		var id uuid.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("store: scan tenant name: %w", err)
		}
		names[id.String()] = name
	}
	return names, rows.Err()
}
