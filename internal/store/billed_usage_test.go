package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// An invoice bills what was delivered, at the final version of each message,
// inside the period and nothing outside it.
func TestBilledUsageCountsOnlyDeliveredMessagesInThePeriod(t *testing.T) {
	url := os.Getenv("TEST_CLICKHOUSE_URL")
	if url == "" {
		t.Skip("TEST_CLICKHOUSE_URL not set")
	}
	ctx := context.Background()
	conn, err := store.OpenClickHouse(ctx, url)
	if err != nil {
		t.Fatalf("open clickhouse: %v", err)
	}
	defer conn.Close()

	tenantID := uuid.New()
	t.Cleanup(func() {
		_ = conn.Exec(context.Background(),
			`ALTER TABLE messages DELETE WHERE tenant_id = ? SETTINGS mutations_sync = 1`, tenantID)
	})
	// Recent, because the table's TTL drops old rows on insert.
	from := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	to := from.Add(2 * time.Hour)

	batch, err := conn.PrepareBatch(ctx, `INSERT INTO messages (
		tenant_id, id, channel, country, sender_header, msisdn, status,
		fraud_flag, segments, cost_minor, currency, created_at, updated_at, version)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	add := func(id uuid.UUID, status string, cost int64, at time.Time, version uint64) {
		if err := batch.Append(tenantID, id, "SMS", "IN", "BILTST", "+919820000001",
			status, "none", uint8(1), cost, "INR", at, at, version); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	inside := from.Add(time.Hour)
	delivered, undelivered := uuid.New(), uuid.New()
	add(delivered, "accepted", 12, inside, 1)
	add(delivered, "delivered", 12, inside, 2)
	add(undelivered, "accepted", 12, inside, 1)
	add(undelivered, "undelivered", 0, inside, 2)
	add(uuid.New(), "delivered", 12, to.Add(30*time.Minute), 2) // after the period
	if err := batch.Send(); err != nil {
		t.Fatalf("send: %v", err)
	}

	usage, err := store.BilledUsageBetween(ctx, conn, from, to)
	if err != nil {
		t.Fatalf("billed usage: %v", err)
	}
	var mine []store.BilledUsage
	for _, row := range usage {
		if row.TenantID == tenantID {
			mine = append(mine, row)
		}
	}
	if len(mine) != 1 || mine[0].MessageCount != 1 || mine[0].AmountMinor != 12 {
		t.Fatalf("usage = %+v, want exactly the one delivered message at 12", mine)
	}
}
