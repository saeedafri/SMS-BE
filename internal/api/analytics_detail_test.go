package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

type statRow struct {
	status, code, carrier string
	took                  time.Duration
}

// seedStatRows writes one message per row, created a minute ago, delivered
// `took` later when delivered.
func (h *harness) seedStatRows(acct account, rows []statRow) {
	h.t.Helper()
	ctx := context.Background()
	conn, err := h.server.ClickHouse.Conn(ctx)
	if err != nil {
		h.t.Fatalf("clickhouse: %v", err)
	}
	batch, err := conn.PrepareBatch(ctx, `INSERT INTO messages (
		tenant_id, id, channel, country, sender_header, msisdn, status, error_code, carrier,
		fraud_flag, segments, cost_minor, currency, created_at, delivered_at, updated_at, version)`)
	if err != nil {
		h.t.Fatalf("prepare: %v", err)
	}
	created := time.Now().UTC().Add(-time.Minute)
	for i, row := range rows {
		var code *string
		if row.code != "" {
			code = &row.code
		}
		var delivered *time.Time
		if row.status == "delivered" {
			at := created.Add(row.took)
			delivered = &at
		}
		if err := batch.Append(acct.TenantID, uuid.New(), "SMS", "IN", "Acme",
			"9198765432"+string(rune('0'+i%10))+string(rune('0'+i/10%10)), row.status, code, row.carrier,
			"none", uint8(1), int64(10), "INR", created, delivered, created, uint64(1)); err != nil {
			h.t.Fatalf("append: %v", err)
		}
	}
	if err := batch.Send(); err != nil {
		h.t.Fatalf("send: %v", err)
	}
}

func TestErrorStatsRankTheReasonsMessagesFailed(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	h.seedStatRows(acct, []statRow{
		{"delivered", "", "VIDEOCON", time.Second},
		{"delivered", "", "VIDEOCON", time.Second},
		{"undelivered", "ABSENT_SUBSCRIBER", "VIDEOCON", 0},
		{"undelivered", "ABSENT_SUBSCRIBER", "VIDEOCON", 0},
		{"rejected", "dnd_blocked", "", 0},
		{"expired", "", "VIDEOCON", 0},
	})
	res := h.do(http.MethodGet, "/v1/analytics/errors?range=24h", acct.Token, nil)
	var out struct {
		Messages, Failed int
		FailureRate      float64
		Errors           []struct {
			Code            string
			Count           int
			ShareOfFailures float64
		}
	}
	if err := json.Unmarshal(res.Body, &out); err != nil || res.Code != 200 {
		t.Fatalf("errors = %d %s", res.Code, res.Body)
	}
	if out.Messages != 6 || out.Failed != 4 || out.FailureRate < 0.66 || out.FailureRate > 0.67 {
		t.Errorf("totals = %s", res.Body)
	}
	if len(out.Errors) != 3 || out.Errors[0].Code != "ABSENT_SUBSCRIBER" || out.Errors[0].Count != 2 ||
		out.Errors[0].ShareOfFailures != 0.5 {
		t.Errorf("breakdown = %s, want ABSENT_SUBSCRIBER first with half the failures", res.Body)
	}
	// A missing code is reported, not dropped.
	found := false
	for _, e := range out.Errors {
		found = found || e.Code == "UNKNOWN"
	}
	if !found {
		t.Errorf("a failure with no code vanished: %s", res.Body)
	}
}

func TestLatencyStatsMeasureOnlyDeliveredMessages(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	h.seedStatRows(acct, []statRow{
		{"delivered", "", "VIDEOCON", 2 * time.Second},
		{"delivered", "", "VIDEOCON", 4 * time.Second},
		{"delivered", "", "VIDEOCON", 40 * time.Second},
		{"undelivered", "ABSENT_SUBSCRIBER", "VIDEOCON", 0},
	})
	res := h.do(http.MethodGet, "/v1/analytics/latency?range=24h", acct.Token, nil)
	var out struct {
		Delivered int
		ByCarrier []struct {
			Carrier   string
			Delivered int
			P50Ms     int
			P99Ms     int
		}
		Histogram []struct {
			Label string
			Count int
		}
	}
	if err := json.Unmarshal(res.Body, &out); err != nil || res.Code != 200 {
		t.Fatalf("latency = %d %s", res.Code, res.Body)
	}
	if out.Delivered != 3 || len(out.ByCarrier) != 1 || out.ByCarrier[0].Delivered != 3 ||
		out.ByCarrier[0].P50Ms < 3000 || out.ByCarrier[0].P50Ms > 4500 || out.ByCarrier[0].P99Ms < 30000 {
		t.Errorf("latency = %s", res.Body)
	}
	if len(out.Histogram) != 5 || out.Histogram[0].Count != 2 || out.Histogram[2].Count != 1 {
		t.Errorf("histogram = %s, want 2 under 5s and 1 in 30s-1m", res.Body)
	}
}

func TestStatsOfNothingAreZerosNotErrors(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	for _, path := range []string{"/v1/analytics/errors", "/v1/analytics/latency"} {
		if res := h.do(http.MethodGet, path, acct.Token, nil); res.Code != 200 {
			t.Errorf("%s with no traffic = %d %s", path, res.Code, res.Body)
		}
	}
}

func TestStatsRefuseBadFiltersAndAnonymousCallers(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	for _, q := range []string{"?range=1y", "?range=bogus", "?channel=FAX", "?country=ZZ"} {
		for _, path := range []string{"/v1/analytics/errors", "/v1/analytics/latency"} {
			if res := h.do(http.MethodGet, path+q, acct.Token, nil); res.Code != 422 {
				t.Errorf("%s%s = %d, want 422", path, q, res.Code)
			}
		}
	}
	if res := h.do(http.MethodGet, "/v1/analytics/errors", "", nil); res.Code != 401 {
		t.Errorf("anonymous = %d, want 401", res.Code)
	}
}

func TestStatsDoNotCrossTenants(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	one, two := h.newAccount("owner"), h.newAccount("owner")
	h.seedStatRows(one, []statRow{{"undelivered", "ABSENT_SUBSCRIBER", "VIDEOCON", 0}})
	res := h.do(http.MethodGet, "/v1/analytics/errors", two.Token, nil)
	var out struct{ Messages, Failed int }
	_ = json.Unmarshal(res.Body, &out)
	if out.Messages != 0 || out.Failed != 0 {
		t.Errorf("tenant two sees tenant one's failures: %s", res.Body)
	}
}
