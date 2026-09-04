package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// A row must be readable the instant its INSERT returns.
//
// This is the guard on async_insert. Server-side batching is what stops every
// single send becoming its own ClickHouse data part, and it is only safe
// because wait_for_async_insert makes the INSERT block until the rows are
// queryable. Without that, a delivery report arriving in the buffering window
// finds no message, is dropped as untrusted, and the hold against an
// undelivered message is never released — the tenant is billed forever for a
// message that never arrived.
//
// If someone sets wait_for_async_insert to 0 to chase latency, this fails, and
// it fails on the exact behaviour that would otherwise show up as a billing
// complaint weeks later.
func TestARowIsQueryableAsSoonAsTheInsertReturns(t *testing.T) {
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

	tenantID, now := uuid.New(), time.Now().UTC()

	// Ten separate one-row inserts, each read back immediately. One would pass
	// by luck on a quiet server; ten in a row is the batching window actually
	// being exercised.
	for attempt := 0; attempt < 10; attempt++ {
		messageID := uuid.New()
		batch, err := conn.PrepareBatch(ctx, `INSERT INTO messages (
			tenant_id, id, channel, country, sender_header, msisdn, status,
			fraud_flag, segments, cost_minor, currency, created_at, updated_at, version)`)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if err := batch.Append(tenantID, messageID, "SMS", "IN", "RAWTST",
			"+919820000001", "queued", "none", uint8(1), int64(100), "INR",
			now, now, uint64(1)); err != nil {
			t.Fatalf("append: %v", err)
		}
		if err := batch.Send(); err != nil {
			t.Fatalf("send: %v", err)
		}

		// No sleep, no retry. The insert has returned, so the row exists.
		var found uint64
		if err := conn.QueryRow(ctx,
			`SELECT count() FROM messages WHERE tenant_id = ? AND id = ?`,
			tenantID, messageID).Scan(&found); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if found != 1 {
			t.Fatalf("attempt %d: the insert returned but the row is not queryable — "+
				"a delivery report arriving now would be dropped and the hold never "+
				"released. Check wait_for_async_insert.", attempt)
		}
	}
}
