package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Ask 66. Each tenant's message log is held to the retention it chose, and
// the setting is a window on the whole table, not a flag stamped at send time.

func (h *harness) seedMessagesAged(acct account, ages ...time.Duration) {
	h.t.Helper()
	ctx := context.Background()
	conn, err := h.server.ClickHouse.Conn(ctx)
	if err != nil {
		h.t.Fatalf("clickhouse: %v", err)
	}
	batch, err := conn.PrepareBatch(ctx, `INSERT INTO messages (
		tenant_id, id, channel, country, sender_header, msisdn, status,
		fraud_flag, segments, cost_minor, currency, created_at, updated_at, version)`)
	if err != nil {
		h.t.Fatalf("prepare: %v", err)
	}
	now := time.Now().UTC()
	for _, age := range ages {
		if err := batch.Append(acct.TenantID, uuid.New(), "SMS", "IN", "Acme", "919876543210",
			"delivered", "none", uint8(1), int64(10), "INR",
			now.Add(-age), now.Add(-age), uint64(1)); err != nil {
			h.t.Fatalf("append: %v", err)
		}
	}
	if err := batch.Send(); err != nil {
		h.t.Fatalf("send batch: %v", err)
	}
}

func (h *harness) messageCount(acct account) uint64 {
	h.t.Helper()
	ctx := context.Background()
	conn, err := h.server.ClickHouse.Conn(ctx)
	if err != nil {
		h.t.Fatalf("clickhouse: %v", err)
	}
	var n uint64
	if err := conn.QueryRow(ctx, `SELECT count() FROM messages WHERE tenant_id = ?`,
		acct.TenantID).Scan(&n); err != nil {
		h.t.Fatalf("count: %v", err)
	}
	return n
}

func (h *harness) setRetention(acct account, days int) {
	h.t.Helper()
	res := h.do(http.MethodPatch, "/v1/data-retention", acct.Token,
		map[string]any{"messageLogRetentionDays": days})
	if res.Code != http.StatusOK {
		h.t.Fatalf("set retention %d = %d\n%s", days, res.Code, res.Body)
	}
}

func TestEachTenantsMessageLogIsHeldToTheRetentionItChose(t *testing.T) {
	h := newSendHarness(t)
	day := 24 * time.Hour
	short := h.newAccount("owner")  // 30 days
	normal := h.newAccount("owner") // never set: 90 days
	long := h.newAccount("owner")   // 365 days
	h.setRetention(short, 30)
	h.setRetention(long, 365)

	h.seedMessagesAged(short, 10*day, 40*day)
	h.seedMessagesAged(normal, 40*day, 100*day)
	h.seedMessagesAged(long, 40*day, 100*day)

	if err := h.server.EnforceMessageRetention(context.Background()); err != nil {
		t.Fatalf("enforce: %v", err)
	}
	for _, want := range []struct {
		name string
		acct account
		left uint64
	}{
		{"30-day tenant keeps only the 10-day message", short, 1},
		{"default tenant keeps the 40-day message, loses the 100-day one", normal, 1},
		{"365-day tenant keeps both, including one the old flat TTL deleted", long, 2},
	} {
		if got := h.messageCount(want.acct); got != want.left {
			t.Errorf("%s: %d messages left, want %d", want.name, got, want.left)
		}
	}

	// Lowering the setting applies to what is already stored, next cycle.
	h.setRetention(long, 30)
	if err := h.server.EnforceMessageRetention(context.Background()); err != nil {
		t.Fatalf("enforce: %v", err)
	}
	if got := h.messageCount(long); got != 0 {
		t.Errorf("after lowering to 30 days: %d messages left, want 0", got)
	}
}
