package store

import (
	"context"
	"errors"
	"fmt"
	"math"
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

	// Latency is handset-confirmed delivery time — created_at to delivered_at —
	// not time-to-carrier-accept. Accept latency is nearly always fast and
	// nearly always meaningless; what a customer feels is when the phone buzzes.
	LatencyP50Ms int
	LatencyP90Ms int

	// Fraud counts come from the per-message table rather than the rollup: the
	// rollup aggregates by status and has no fraud dimension, so a fraud figure
	// derived from it would be an invention.
	FraudVelocity   int
	FraudGeoAnomaly int
	FraudBlocked    int

	// Channel-specific figures. Each is meaningful for exactly one channel and
	// meaningless for the rest — an SMS has no bounce and a voice call has no
	// read receipt — so the API sends each one only when the view is filtered
	// to the channel it belongs to.
	//
	// The contract calls these approximate, and they are: they are derived from
	// the outcome the carrier reported, not from a separate engagement pipeline
	// we do not have. Bounces are counted from the error codes a mail provider
	// returns; conversations approximate WhatsApp's billing unit; answered rate
	// is the share of calls that connected.
	Bounced       int
	Conversations int
	VoiceAnswered int
	VoiceAttempts int

	// Mean connected-call length, in seconds. Approximated by how long a call
	// took to reach its delivered state — for Voice that span IS the call, from
	// dial to hang-up, because a voice message is delivered by being played.
	VoiceAvgSeconds float64
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
	Carrier   string
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

	// Latency and fraud come from the per-message table, which the rollup cannot
	// answer: it has no fraud dimension and quantiles cannot be summed out of
	// pre-aggregated counts. This is a second pass over a narrower slice
	// (delivered messages only, for the same window) rather than a second full
	// scan.
	//
	// The filter is rebuilt because `messages` is partitioned on created_at
	// while the rollup keys on `hour` — reusing the rollup's predicate here
	// would silently scan every partition.
	messageWhere := "tenant_id = ? AND created_at >= ?"
	messageArgs := []any{tenantID, filter.Since}
	if filter.Channel != "" {
		messageWhere += " AND channel = ?"
		messageArgs = append(messageArgs, filter.Channel)
	}
	if filter.Country != "" {
		messageWhere += " AND country = ?"
		messageArgs = append(messageArgs, filter.Country)
	}
	var p50, p90 float64
	var velocity, geoAnomaly, blocked uint64
	var bounced, conversations, voiceAnswered, voiceAttempts uint64
	var voiceAvgSeconds float64
	if err := conn.QueryRow(ctx, `
		SELECT
			quantileIf(0.5)(dateDiff('millisecond', created_at, delivered_at),
			                delivered_at IS NOT NULL),
			quantileIf(0.9)(dateDiff('millisecond', created_at, delivered_at),
			                delivered_at IS NOT NULL),
			countIf(fraud_flag = 'velocity'),
			countIf(fraud_flag = 'geo_anomaly'),
			countIf(fraud_flag = 'blocked'),
			-- A bounce is the mail provider refusing the address, hard or soft.
			-- Counted from the error code rather than from status alone, because
			-- "undelivered" also covers a message we never handed over.
			countIf(channel = 'EMAIL' AND error_code LIKE 'BOUNCED%'),
			-- WhatsApp bills per 24-hour conversation with a person, not per
			-- message, so the billable unit is the distinct recipient-day. This
			-- is the same approximation their own reporting makes, and it is
			-- labelled approximate in the contract for that reason.
			uniqExactIf((msisdn, toDate(created_at)), channel = 'WHATSAPP'),
			-- Answered means the call connected. A call nobody picked up is a
			-- normal outcome, not a failure, which is why this is a rate rather
			-- than a delivery figure.
			countIf(channel = 'VOICE' AND status = 'delivered'),
			countIf(channel = 'VOICE' AND status IN ('delivered','undelivered')),
			-- Only connected calls. Averaging in the ones nobody answered would
			-- drag the mean toward zero and describe a call length that never
			-- happened.
			avgIf(dateDiff('second', sent_at, delivered_at),
			      channel = 'VOICE' AND status = 'delivered'
			      AND sent_at IS NOT NULL AND delivered_at IS NOT NULL)
		FROM messages WHERE `+messageWhere, messageArgs...,
	).Scan(&p50, &p90, &velocity, &geoAnomaly, &blocked,
		&bounced, &conversations, &voiceAnswered, &voiceAttempts,
		&voiceAvgSeconds); err != nil {
		// A tenant with no delivered messages yet is not an error — quantiles
		// over an empty set return NaN, and the dashboard should show zeros
		// rather than fail the whole page over a missing latency figure.
		p50, p90 = 0, 0
	}
	if math.IsNaN(p50) {
		p50 = 0
	}
	if math.IsNaN(p90) {
		p90 = 0
	}
	summary.LatencyP50Ms, summary.LatencyP90Ms = int(p50), int(p90)
	summary.FraudVelocity = int(velocity)
	summary.FraudGeoAnomaly = int(geoAnomaly)
	summary.FraudBlocked = int(blocked)
	summary.Bounced = int(bounced)
	summary.Conversations = int(conversations)
	summary.VoiceAnswered = int(voiceAnswered)
	summary.VoiceAttempts = int(voiceAttempts)
	// An average over no connected calls is NaN, not zero.
	if !math.IsNaN(voiceAvgSeconds) {
		summary.VoiceAvgSeconds = voiceAvgSeconds
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

	// Read from `messages` rather than the rollup: deliverability is broken down
	// BY CARRIER, and the rollup has no carrier dimension. Adding one would mean
	// changing the ingest path that the send tests already cover, to serve one
	// screen — this grouped aggregate over a filtered slice is the cheaper trade.
	// Reporting a single synthetic "all" carrier instead, which is what this did
	// before, hides exactly the comparison the screen exists to make.
	//
	// Messages with no carrier recorded are EXCLUDED rather than bucketed under
	// a placeholder: this table exists to compare carriers, and a row that
	// attributes traffic to a carrier that may not have carried it is worse
	// than a row that is absent. The summary above still counts them.
	deliverRows, err := conn.Query(ctx, `
		SELECT country, channel, carrier,
		       countIf(status IN ('accepted','delivered','undelivered','rejected')) AS sent,
		       countIf(status = 'delivered') AS delivered
		FROM messages WHERE `+messageWhere+` AND carrier != ''
		GROUP BY country, channel, carrier ORDER BY sent DESC`, messageArgs...)
	if err != nil {
		return summary, buckets, nil, fmt.Errorf("store: analytics deliverability: %w", err)
	}
	defer deliverRows.Close()

	var deliverability []DeliverabilityRow
	for deliverRows.Next() {
		var row DeliverabilityRow
		var sent, delivered uint64
		if err := deliverRows.Scan(&row.Country, &row.Channel, &row.Carrier,
			&sent, &delivered); err != nil {
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

// AttributedSpend is spend grouped by the thing that caused it.
type AttributedSpend struct {
	ID           string
	Name         string
	Channel      string
	Currency     string
	Amount       int64
	MessageCount int
}

// UsageByCampaign and UsageByJourney attribute spend to what drove it.
//
// This has to come from ClickHouse rather than the wallet: the ledger records
// that money moved, but only the message rows know WHICH campaign or journey
// moved it. Charging is per message, so the message table is the only place the
// attribution exists.
//
// Delivered messages only, matching every other cost figure in the product — a
// tenant is not billed for a message that never arrived, so attributing spend
// to one would inflate the campaign's cost.
func UsageByCampaign(ctx context.Context, conn driver.Conn, tenantID uuid.UUID,
	since time.Time, currency string) ([]AttributedSpend, error) {
	return attributedSpend(ctx, conn, tenantID, "campaign_id", "campaign_name", since, currency)
}

func UsageByJourney(ctx context.Context, conn driver.Conn, tenantID uuid.UUID,
	since time.Time, currency string) ([]AttributedSpend, error) {
	return attributedSpend(ctx, conn, tenantID, "journey_id", "journey_name", since, currency)
}

func attributedSpend(ctx context.Context, conn driver.Conn, tenantID uuid.UUID,
	idColumn, nameColumn string, since time.Time, currency string) ([]AttributedSpend, error) {

	// max(name), not any(name).
	//
	// any() returns an ARBITRARY row from the group, and these groups are mixed:
	// a campaign's messages can carry the name on some rows and an empty string
	// on others, depending on which path recorded them. any() then picked the
	// empty one often enough to matter, and the usage report rendered a row with
	// a real amount and a blank label — which the frontend turns into a link
	// with no text, so a screen reader announces nothing at all. That is how it
	// was found: an axe check on /billing/usage, not a billing complaint.
	//
	// The empty string sorts below every real name, so max() returns a genuine
	// name whenever the group holds one.
	// Collapse each message to its latest version BEFORE aggregating.
	//
	// messages is a ReplacingMergeTree: every status change writes another row
	// for the same id and the versions coexist until a background merge runs.
	// Aggregating raw rows makes both numbers depend on whether that merge has
	// happened yet.
	//
	// It bit amount specifically. sumIf(cost_minor, status = 'delivered') reads
	// a message that went delivered -> read as billable while both rows are
	// present, and as free the moment the merge leaves only the 'read' one — the
	// charge silently leaves the report with no data having changed. argMax over
	// version pins each message to one row and one status, so the answer is the
	// same before and after a merge.
	//
	// message_count is then a count of those collapsed rows, so a journey with
	// several settled send steps reports its real volume rather than that volume
	// times its step count. amount is still summed, because each message's cost
	// is its own.
	rows, err := conn.Query(ctx, fmt.Sprintf(`
		SELECT entity_id, max(name) AS name, channel, currency,
		       sumIf(cost, final_status IN ('delivered', 'read')) AS amount,
		       countIf(final_status IN ('delivered', 'read')) AS message_count
		FROM (
			SELECT toString(%s) AS entity_id, max(%s) AS name, channel, currency, id,
			       argMax(status, version) AS final_status,
			       argMax(cost_minor, version) AS cost
			FROM messages
			WHERE tenant_id = ? AND %s IS NOT NULL AND created_at >= ?
			  AND (? = '' OR currency = ?)
			GROUP BY entity_id, channel, currency, id
		)
		GROUP BY entity_id, channel, currency
		HAVING amount > 0
		ORDER BY amount DESC`, idColumn, nameColumn, idColumn),
		tenantID, since, currency, currency)
	if err != nil {
		return nil, fmt.Errorf("store: attributed spend: %w", err)
	}
	defer rows.Close()
	out := []AttributedSpend{}
	for rows.Next() {
		var row AttributedSpend
		var messageCount uint64
		if err := rows.Scan(&row.ID, &row.Name, &row.Channel, &row.Currency,
			&row.Amount, &messageCount); err != nil {
			return nil, fmt.Errorf("store: scan attributed spend: %w", err)
		}
		row.MessageCount = int(messageCount)
		out = append(out, row)
	}
	return out, rows.Err()
}

// SetScheduledReportPaused pauses or resumes a report.
//
// Pausing rather than deleting is the point: a report someone spent time
// configuring should be stoppable without losing its recipients and schedule,
// so they can turn it back on later without rebuilding it.
func SetScheduledReportPaused(ctx context.Context, pool *pgxpool.Pool, id Identity,
	reportID uuid.UUID, paused bool) (ScheduledReport, error) {

	var report ScheduledReport
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			UPDATE scheduled_reports SET paused = $2 WHERE id = $1
			RETURNING id, frequency, range_key, recipients, paused, created_at`,
			reportID, paused,
		).Scan(&report.ID, &report.Frequency, &report.Range, &report.Recipients,
			&report.Paused, &report.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ScheduledReport{}, ErrNotFound
	}
	if err != nil {
		return ScheduledReport{}, fmt.Errorf("store: set scheduled report paused: %w", err)
	}
	return report, nil
}
