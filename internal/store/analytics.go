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

// AnalyticsSummary is the headline row on the analytics screen.
type AnalyticsSummary struct {
	Sent          int
	Delivered     int
	Failed        int
	Read          int
	CostMinor     int64
	Currency      string
	CurrencyMixed bool
}

// AnalyticsBucket is one point on the time series.
type AnalyticsBucket struct {
	BucketStart time.Time
	Sent        int
	Delivered   int
	Failed      int
	Read        int
	CostMinor   int64
}

// DeliverabilityRow is one country/channel deliverability line.
type DeliverabilityRow struct {
	Country   string
	Channel   string
	Sent      int
	Delivered int
}

// AnalyticsFilter narrows a query to a range and optionally a channel/country.
type AnalyticsFilter struct {
	Since   time.Time
	Channel string
	Country string
}

// QueryAnalytics reads the hourly rollup rather than raw messages.
//
// The rollup is the only correct source here: raw message rows age out on a
// TTL while rollups are permanent, so a 90-day query against raw rows would
// silently under-report the oldest part of its own range. Reading the rollup
// also means an analytics query never scans the largest table in the system.
//
// Terminal statuses only. The rollup gets a row per transition, so a message
// that went queued then accepted then delivered contributes three rows;
// counting them all would triple-count one message. Summing only the terminal
// states gives one row per message that reached an outcome.
func QueryAnalytics(ctx context.Context, conn driver.Conn, tenantID uuid.UUID,
	filter AnalyticsFilter) (AnalyticsSummary, []AnalyticsBucket, []DeliverabilityRow, error) {

	where := "tenant_id = ? AND hour >= ?"
	args := []any{tenantID, filter.Since}
	if filter.Channel != "" {
		where += " AND channel = ?"
		args = append(args, filter.Channel)
	}
	if filter.Country != "" {
		where += " AND country = ?"
		args = append(args, filter.Country)
	}

	// sumIf over status keeps one pass over the data rather than one query per
	// metric.
	//
	// "sent" counts ACCEPTED and REJECTED rows, never sum(everything). The
	// rollup gets one row per TRANSITION, so a message that went queued ->
	// accepted -> delivered contributes three rows and summing them all reports
	// three messages where there was one. Every message produces at most one
	// accepted row and at most one rejected row, and never both, so this counts
	// each attempted message exactly once — the denominator a delivery rate is
	// meaningful against.
	summaryQuery := `
		SELECT
			sumIf(message_count, status IN ('accepted','submitted','rejected')) AS total,
			sumIf(message_count, status = 'delivered')   AS delivered,
			sumIf(message_count, status IN ('undelivered','rejected','expired')) AS failed,
			sumIf(cost_minor,    status = 'delivered')   AS cost,
			uniq(currency) AS currency_count,
			any(currency)  AS first_currency
		FROM message_rollup_hourly WHERE ` + where

	var summary AnalyticsSummary
	var total, delivered, failed uint64
	var cost int64
	var currencies uint64
	var currency string
	if err := conn.QueryRow(ctx, summaryQuery, args...).Scan(
		&total, &delivered, &failed, &cost, &currencies, &currency); err != nil {
		return summary, nil, nil, fmt.Errorf("store: analytics summary: %w", err)
	}
	summary = AnalyticsSummary{
		Sent: int(total), Delivered: int(delivered), Failed: int(failed),
		CostMinor: cost, Currency: currency,
		// A tenant sending in two currencies cannot have one honest cost
		// figure, and the contract has a flag for exactly that rather than
		// letting the UI add rupees to dollars.
		CurrencyMixed: currencies > 1,
	}
	if summary.Currency == "" {
		summary.Currency = "INR"
	}

	bucketRows, err := conn.Query(ctx, `
		SELECT toStartOfDay(hour) AS bucket,
		       sum(message_count),
		       sumIf(message_count, status = 'delivered'),
		       sumIf(message_count, status IN ('undelivered','rejected','expired')),
		       sumIf(cost_minor,    status = 'delivered')
		FROM message_rollup_hourly WHERE `+where+`
		GROUP BY bucket ORDER BY bucket`, args...)
	if err != nil {
		return summary, nil, nil, fmt.Errorf("store: analytics buckets: %w", err)
	}
	defer bucketRows.Close()

	var buckets []AnalyticsBucket
	for bucketRows.Next() {
		var bucket AnalyticsBucket
		var sent, delivered, failed uint64
		var cost int64
		if err := bucketRows.Scan(&bucket.BucketStart, &sent, &delivered, &failed, &cost); err != nil {
			return summary, nil, nil, fmt.Errorf("store: scan bucket: %w", err)
		}
		bucket.Sent, bucket.Delivered, bucket.Failed, bucket.CostMinor =
			int(sent), int(delivered), int(failed), cost
		buckets = append(buckets, bucket)
	}
	if err := bucketRows.Err(); err != nil {
		return summary, nil, nil, err
	}

	deliverRows, err := conn.Query(ctx, `
		SELECT country, channel,
		       sum(message_count),
		       sumIf(message_count, status = 'delivered')
		FROM message_rollup_hourly WHERE `+where+`
		GROUP BY country, channel ORDER BY sum(message_count) DESC`, args...)
	if err != nil {
		return summary, buckets, nil, fmt.Errorf("store: analytics deliverability: %w", err)
	}
	defer deliverRows.Close()

	var deliverability []DeliverabilityRow
	for deliverRows.Next() {
		var row DeliverabilityRow
		var sent, delivered uint64
		if err := deliverRows.Scan(&row.Country, &row.Channel, &sent, &delivered); err != nil {
			return summary, buckets, nil, fmt.Errorf("store: scan deliverability: %w", err)
		}
		row.Sent, row.Delivered = int(sent), int(delivered)
		deliverability = append(deliverability, row)
	}
	return summary, buckets, deliverability, deliverRows.Err()
}

// TenantSettings holds the single-row per-tenant toggles.
type TenantSettings struct {
	SSOEnabled              bool
	SSOProvider             *string
	SSOMetadataURL          *string
	SSOEntityID             *string
	MessageLogRetentionDays int
}

// GetTenantSettings returns the tenant's settings, creating the defaults row on
// first read so every caller sees a value rather than a missing row.
func GetTenantSettings(ctx context.Context, pool *pgxpool.Pool, id Identity) (TenantSettings, error) {
	var settings TenantSettings
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO tenant_settings (tenant_id) VALUES ($1)
			 ON CONFLICT (tenant_id) DO NOTHING`, id.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT sso_enabled, sso_provider, sso_metadata_url, sso_entity_id,
			       message_log_retention_days
			FROM tenant_settings WHERE tenant_id = $1`, id.TenantID,
		).Scan(&settings.SSOEnabled, &settings.SSOProvider, &settings.SSOMetadataURL,
			&settings.SSOEntityID, &settings.MessageLogRetentionDays)
	})
	if err != nil {
		return TenantSettings{}, fmt.Errorf("store: get tenant settings: %w", err)
	}
	return settings, nil
}

func UpdateSSO(ctx context.Context, pool *pgxpool.Pool, id Identity,
	settings TenantSettings) (TenantSettings, error) {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO tenant_settings (tenant_id, sso_enabled, sso_provider,
			    sso_metadata_url, sso_entity_id)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (tenant_id) DO UPDATE SET
			    sso_enabled = EXCLUDED.sso_enabled,
			    sso_provider = EXCLUDED.sso_provider,
			    sso_metadata_url = EXCLUDED.sso_metadata_url,
			    sso_entity_id = EXCLUDED.sso_entity_id,
			    updated_at = now()`,
			id.TenantID, settings.SSOEnabled, settings.SSOProvider,
			settings.SSOMetadataURL, settings.SSOEntityID)
		return err
	})
	if err != nil {
		return TenantSettings{}, fmt.Errorf("store: update sso: %w", err)
	}
	return GetTenantSettings(ctx, pool, id)
}

func UpdateRetention(ctx context.Context, pool *pgxpool.Pool, id Identity,
	days int) (TenantSettings, error) {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO tenant_settings (tenant_id, message_log_retention_days)
			VALUES ($1,$2)
			ON CONFLICT (tenant_id) DO UPDATE SET
			    message_log_retention_days = EXCLUDED.message_log_retention_days,
			    updated_at = now()`, id.TenantID, days)
		return err
	})
	if err != nil {
		return TenantSettings{}, fmt.Errorf("store: update retention: %w", err)
	}
	return GetTenantSettings(ctx, pool, id)
}

// ScheduledReport is a recurring analytics export.
type ScheduledReport struct {
	ID         uuid.UUID
	Frequency  string
	Range      string
	Recipients []string
	Paused     bool
	CreatedAt  time.Time
}

func ListScheduledReports(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]ScheduledReport, error) {
	var out []ScheduledReport
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, frequency, range_key, recipients, paused, created_at
			FROM scheduled_reports ORDER BY created_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var report ScheduledReport
			if err := rows.Scan(&report.ID, &report.Frequency, &report.Range,
				&report.Recipients, &report.Paused, &report.CreatedAt); err != nil {
				return err
			}
			out = append(out, report)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: list scheduled reports: %w", err)
	}
	return out, nil
}

func CreateScheduledReport(ctx context.Context, pool *pgxpool.Pool, id Identity,
	report ScheduledReport) (ScheduledReport, error) {

	if report.Recipients == nil {
		report.Recipients = []string{}
	}
	if report.Range == "" {
		report.Range = "30d"
	}
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO scheduled_reports (tenant_id, frequency, range_key, recipients)
			VALUES ($1,$2,$3,$4)
			RETURNING id, frequency, range_key, recipients, paused, created_at`,
			id.TenantID, report.Frequency, report.Range, report.Recipients,
		).Scan(&report.ID, &report.Frequency, &report.Range, &report.Recipients,
			&report.Paused, &report.CreatedAt)
	})
	if err != nil {
		return ScheduledReport{}, fmt.Errorf("store: create scheduled report: %w", err)
	}
	return report, nil
}

func DeleteScheduledReport(ctx context.Context, pool *pgxpool.Pool, id Identity,
	reportID uuid.UUID) error {

	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM scheduled_reports WHERE id = $1`, reportID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}
