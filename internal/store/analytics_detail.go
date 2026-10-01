package store

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
)

// The two detail views behind the stats pages: why messages failed, and how
// long delivery took. Both read per-message rows, not the rollup, because the
// rollup has no error-code or carrier dimension and quantiles cannot be summed
// out of counts. The raw table keeps 90 days, so the widest range is 90d.

// failedStatuses are the outcomes an operator or a gate ended a message in.
const failedStatuses = `'rejected','undelivered','carrier_rejected','expired'`

type DetailFilter struct {
	Since   time.Time
	Channel string
	Country string
}

func (f DetailFilter) where(tenantID uuid.UUID) (string, []any) {
	clause := "tenant_id = ? AND created_at >= ?"
	args := []any{tenantID, f.Since}
	if f.Channel != "" {
		clause += " AND channel = ?"
		args = append(args, f.Channel)
	}
	if f.Country != "" {
		clause += " AND country = ?"
		args = append(args, f.Country)
	}
	return clause, args
}

// ErrorRow is one failure reason on one channel.
type ErrorRow struct {
	Channel string
	Code    string
	Class   string
	Count   int
}

// ErrorTotals is the denominator the shares are read against.
type ErrorTotals struct {
	Messages int
	Failed   int
}

// QueryErrorBreakdown returns the most common failure codes, largest first.
func QueryErrorBreakdown(ctx context.Context, conn driver.Conn, tenantID uuid.UUID,
	filter DetailFilter) (ErrorTotals, []ErrorRow, error) {

	where, args := filter.where(tenantID)
	var totals ErrorTotals
	var all, failed uint64
	if err := conn.QueryRow(ctx, `SELECT count(), countIf(status IN (`+failedStatuses+`))
		FROM messages FINAL WHERE `+where, args...).Scan(&all, &failed); err != nil {
		return totals, nil, fmt.Errorf("store: error totals: %w", err)
	}
	totals = ErrorTotals{Messages: int(all), Failed: int(failed)}

	rows, err := conn.Query(ctx, `
		SELECT channel, ifNull(error_code, 'UNKNOWN'), ifNull(error_class, ''), count() AS n
		FROM messages FINAL WHERE `+where+` AND status IN (`+failedStatuses+`)
		GROUP BY channel, ifNull(error_code, 'UNKNOWN'), ifNull(error_class, '')
		ORDER BY n DESC, channel, 2 LIMIT 100`, args...)
	if err != nil {
		return totals, nil, fmt.Errorf("store: error breakdown: %w", err)
	}
	defer rows.Close()
	out := []ErrorRow{}
	for rows.Next() {
		var row ErrorRow
		var n uint64
		if err := rows.Scan(&row.Channel, &row.Code, &row.Class, &n); err != nil {
			return totals, nil, err
		}
		row.Count = int(n)
		out = append(out, row)
	}
	return totals, out, rows.Err()
}

// LatencyRow is delivery time for one channel and carrier, in milliseconds.
type LatencyRow struct {
	Channel   string
	Carrier   string
	Delivered int
	P50Ms     int
	P90Ms     int
	P99Ms     int
	AvgMs     int
}

// LatencyBucket counts delivered messages by how long they took.
type LatencyBucket struct {
	Label string
	Count int
}

// QueryLatency is created-to-delivered time, per channel and carrier, plus a
// histogram. Only delivered messages have a delivery time; a message that never
// arrived is a failure, not an infinitely slow success.
func QueryLatency(ctx context.Context, conn driver.Conn, tenantID uuid.UUID,
	filter DetailFilter) ([]LatencyRow, []LatencyBucket, error) {

	where, args := filter.where(tenantID)
	rows, err := conn.Query(ctx, `
		SELECT channel, carrier, count(),
		       quantile(0.5)(ms), quantile(0.9)(ms), quantile(0.99)(ms), avg(ms)
		FROM (
		  SELECT channel, carrier,
		         dateDiff('millisecond', created_at, delivered_at) AS ms
		  FROM messages FINAL
		  WHERE `+where+` AND delivered_at IS NOT NULL AND status = 'delivered'
		)
		GROUP BY channel, carrier ORDER BY count() DESC, channel, carrier`, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("store: latency: %w", err)
	}
	defer rows.Close()
	out := []LatencyRow{}
	for rows.Next() {
		var row LatencyRow
		var n uint64
		var p50, p90, p99, avg float64
		if err := rows.Scan(&row.Channel, &row.Carrier, &n, &p50, &p90, &p99, &avg); err != nil {
			return nil, nil, err
		}
		row.Delivered = int(n)
		row.P50Ms, row.P90Ms, row.P99Ms, row.AvgMs = ms(p50), ms(p90), ms(p99), ms(avg)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var fast, quick, minute, slow, slower uint64
	if err := conn.QueryRow(ctx, `
		SELECT countIf(ms < 5000), countIf(ms >= 5000 AND ms < 30000),
		       countIf(ms >= 30000 AND ms < 60000), countIf(ms >= 60000 AND ms < 300000),
		       countIf(ms >= 300000)
		FROM (SELECT dateDiff('millisecond', created_at, delivered_at) AS ms
		      FROM messages FINAL
		      WHERE `+where+` AND delivered_at IS NOT NULL AND status = 'delivered')`,
		args...).Scan(&fast, &quick, &minute, &slow, &slower); err != nil {
		return nil, nil, fmt.Errorf("store: latency histogram: %w", err)
	}
	return out, []LatencyBucket{
		{"under 5s", int(fast)}, {"5s to 30s", int(quick)}, {"30s to 1m", int(minute)},
		{"1m to 5m", int(slow)}, {"over 5m", int(slower)},
	}, nil
}

// ms turns a quantile into whole milliseconds; an empty group yields NaN.
func ms(value float64) int {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return int(value)
}
