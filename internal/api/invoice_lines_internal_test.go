package api

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

type billedMessage struct {
	segments uint8
	cost     int64
}

// seedDelivered writes delivered SMS for one tenant, at one instant.
func seedDelivered(t *testing.T, at time.Time, messages ...billedMessage) (driver.Conn, uuid.UUID) {
	t.Helper()
	url := os.Getenv("TEST_CLICKHOUSE_URL")
	if url == "" {
		t.Skip("TEST_CLICKHOUSE_URL not set")
	}
	ctx := context.Background()
	conn, err := store.OpenClickHouse(ctx, url)
	if err != nil {
		t.Fatalf("open clickhouse: %v", err)
	}
	tenant := uuid.New()
	t.Cleanup(func() {
		_ = conn.Exec(context.Background(),
			"ALTER TABLE messages DELETE WHERE tenant_id = ? SETTINGS mutations_sync = 1", tenant)
		_ = conn.Close()
	})
	records := make([]store.MessageRecord, 0, len(messages))
	for _, m := range messages {
		records = append(records, store.MessageRecord{
			TenantID: tenant, ID: uuid.New(), Channel: "SMS", Country: "IN", SenderHeader: "ACMERT",
			Msisdn: "919820000040", Status: "delivered", FraudFlag: "none", Segments: m.segments,
			CostMinor: m.cost, Currency: "INR", CreatedAt: at, UpdatedAt: at, Version: 3,
		})
	}
	if err := store.InsertMessages(ctx, conn, records); err != nil {
		t.Fatalf("seed messages: %v", err)
	}
	return conn, tenant
}

func linesFor(t *testing.T, conn driver.Conn, tenant uuid.UUID, at time.Time) []store.InvoiceLine {
	t.Helper()
	usage, err := store.BilledUsageBetween(context.Background(), conn, at.Add(-time.Minute), at.Add(time.Minute))
	if err != nil {
		t.Fatalf("billed usage: %v", err)
	}
	var own []store.BilledUsage
	for _, row := range usage {
		if row.TenantID == tenant {
			own = append(own, row)
		}
	}
	return invoiceLines(own)[invoiceKey{tenant, "INR"}]
}

// Ask 38. Two unit prices never share a line and a price is never averaged:
// quantity counts segments, the unit the rate card prices.
func TestInvoiceLinesSplitByUnitPrice(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second)
	conn, tenant := seedDelivered(t, at,
		billedMessage{1, 12}, billedMessage{1, 12}, billedMessage{1, 12},
		billedMessage{2, 24}, billedMessage{2, 24},
		billedMessage{1, 15})
	lines := linesFor(t, conn, tenant, at)

	got := map[int64][2]int64{}
	for _, line := range lines {
		got[line.UnitMinor] = [2]int64{line.Quantity, line.AmountMinor}
	}
	if len(lines) != 2 || got[12] != [2]int64{7, 84} || got[15] != [2]int64{1, 15} {
		t.Fatalf("lines = %+v, want (qty 7, unit 12, 84) and (qty 1, unit 15, 15)", lines)
	}
	if subtotal := buildInvoice("INR", at, at, lines).SubtotalMinor; subtotal != 99 {
		t.Fatalf("subtotal = %d, want 99", subtotal)
	}
}

// Ask 38. quantity * unitMinor == amountMinor on every line, and the lines sum
// to the subtotal.
func TestEveryInvoiceLineReconciles(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second)
	conn, tenant := seedDelivered(t, at,
		billedMessage{1, 12}, billedMessage{1, 12}, billedMessage{1, 12},
		billedMessage{2, 24}, billedMessage{2, 24}, billedMessage{1, 15})
	lines := linesFor(t, conn, tenant, at)
	var sum int64
	for _, line := range lines {
		if line.Quantity*line.UnitMinor != line.AmountMinor {
			t.Errorf("%q: %d x %d = %d, amount %d", line.Description, line.Quantity, line.UnitMinor,
				line.Quantity*line.UnitMinor, line.AmountMinor)
		}
		sum += line.AmountMinor
	}
	if subtotal := buildInvoice("INR", at, at, lines).SubtotalMinor; sum != subtotal {
		t.Errorf("lines sum to %d, subtotal %d", sum, subtotal)
	}
}
