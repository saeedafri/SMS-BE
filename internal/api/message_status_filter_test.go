package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Every declared status filters, and the seven together account for the whole
// log exactly once.
//
// Two assertions, because either alone passes while the filter is broken.
// Purity catches a filter that is ignored: ?status=rejected used to answer with
// the entire collection under a pager reporting its full size, and it hid well
// because the log is newest-first and the newest rows happened to be refusals,
// so page one looked filtered. Partition catches a filter that IS applied but
// under-inclusive: `failed` mapped to `undelivered` alone, so carrier
// rejections and expiries were unreachable — and every row it did return was
// correctly failed, which is why purity alone cannot see it.
//
// The gap on production was 10,226 rows reachable by no filter at all.
func TestEveryMessageStatusFilterPartitionsTheLog(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")

	// One row per INTERNAL state, so the partition has something to be wrong
	// about. Counts differ per state so a mapping that swaps two of them fails
	// rather than coincidentally summing.
	seeded := map[string]int{
		"queued": 2, "submitting": 3, "submitted": 4, "accepted": 5,
		"delivered": 6, "undelivered": 7, "rejected": 8,
		"carrier_rejected": 9, "expired": 10,
	}
	ctx := context.Background()
	conn, err := h.server.ClickHouse.Conn(ctx)
	if err != nil {
		t.Fatalf("clickhouse: %v", err)
	}
	batch, err := conn.PrepareBatch(ctx, `INSERT INTO messages (
		tenant_id, id, channel, country, sender_header, msisdn, status,
		fraud_flag, segments, cost_minor, currency,
		created_at, updated_at, version)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	now := time.Now().UTC()
	for state, n := range seeded {
		for i := 0; i < n; i++ {
			if err := batch.Append(acct.TenantID, uuid.New(), "SMS", "IN",
				"Acme", fmt.Sprintf("+9198765%05d", i), state,
				"none", uint8(1), int64(0), "INR", now, now, uint64(1)); err != nil {
				t.Fatalf("append %s: %v", state, err)
			}
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatalf("send batch: %v", err)
	}

	type page struct {
		Messages []struct {
			Status string `json:"status"`
		} `json:"messages"`
		Total int `json:"total"`
	}
	get := func(query string) page {
		t.Helper()
		res := h.do(http.MethodGet, "/v1/messages"+query, acct.Token, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s = %d\n%s", query, res.Code, res.Body)
		}
		var p page
		if err := json.Unmarshal([]byte(res.Body), &p); err != nil {
			t.Fatalf("decode %s: %v", query, err)
		}
		return p
	}

	unfiltered := get("?limit=1").Total
	if unfiltered == 0 {
		t.Fatal("no messages in the log, so this test cannot tell a filter from a no-op")
	}

	// What each wire value must cover, given the states seeded above.
	want := map[string]int{
		"queued":    seeded["queued"] + seeded["submitting"],
		"sent":      seeded["submitted"] + seeded["accepted"],
		"delivered": seeded["delivered"],
		"failed":    seeded["undelivered"] + seeded["carrier_rejected"] + seeded["expired"],
		"rejected":  seeded["rejected"],
		"read":      0,
		"cancelled": 0,
	}

	sum := 0
	for status, expected := range want {
		p := get("?status=" + status + "&limit=200")

		// Purity: a filter narrows, or it matches nothing. It never widens.
		for _, row := range p.Messages {
			if row.Status != status {
				t.Errorf("?status=%s returned a %q row — the filter is not applied",
					status, row.Status)
				break
			}
		}
		if p.Total == unfiltered && unfiltered > expected {
			t.Errorf("?status=%s returned the whole log (%d) — a value that maps to no "+
				"state must match nothing, not everything", status, p.Total)
		}
		if p.Total != expected {
			t.Errorf("?status=%s total = %d, want %d — the wire vocabulary is coarser "+
				"than the state machine, so this filter is a set", status, p.Total, expected)
		}
		sum += p.Total
	}

	// Partition: the seven cover the log exactly once between them.
	if sum != unfiltered {
		t.Errorf("the seven status filters sum to %d, the unfiltered log is %d — "+
			"%d rows are reachable by no filter at all", sum, unfiltered, unfiltered-sum)
	}
}
