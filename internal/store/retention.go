package store

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenantsByRetention groups every tenant by the message-log retention it
// chose. A tenant that never opened the setting has no row and keeps the
// column's default, 90 days.
func TenantsByRetention(ctx context.Context, operator *pgxpool.Pool) (map[int][]uuid.UUID, error) {
	rows, err := operator.Query(ctx, `
		SELECT t.id, coalesce(s.message_log_retention_days, 90)
		FROM tenants t LEFT JOIN tenant_settings s ON s.tenant_id = t.id`)
	if err != nil {
		return nil, fmt.Errorf("store: tenants by retention: %w", err)
	}
	defer rows.Close()
	out := map[int][]uuid.UUID{}
	for rows.Next() {
		var tenantID uuid.UUID
		var days int
		if err := rows.Scan(&tenantID, &days); err != nil {
			return nil, err
		}
		out[days] = append(out[days], tenantID)
	}
	return out, rows.Err()
}

// DeleteMessagesBefore removes these tenants' messages created before cutoff,
// reporting how many there were.
//
// Counted first so a cycle with nothing to remove issues no DELETE: every
// lightweight delete is a mutation ClickHouse has to schedule, and most cycles
// find nothing.
func DeleteMessagesBefore(ctx context.Context, conn driver.Conn, tenants []uuid.UUID,
	cutoff time.Time) (uint64, error) {

	if len(tenants) == 0 {
		return 0, nil
	}
	const where = `tenant_id IN (?) AND created_at < ?`
	var expired uint64
	if err := conn.QueryRow(ctx, `SELECT count() FROM messages WHERE `+where,
		tenants, cutoff).Scan(&expired); err != nil {
		return 0, fmt.Errorf("store: count expired messages: %w", err)
	}
	if expired == 0 {
		return 0, nil
	}
	if err := conn.Exec(ctx, `DELETE FROM messages WHERE `+where, tenants, cutoff); err != nil {
		return 0, fmt.Errorf("store: delete expired messages: %w", err)
	}
	return expired, nil
}
