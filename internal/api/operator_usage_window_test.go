package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/saeedafri/sms-be/internal/gen/api"
)

// The tenant detail screen and the platform usage report must report the same
// 30-day volume for the same tenant.
//
// They already shared the table and the "attempted" status filter, and the
// store carries a comment saying that is what keeps them from drifting. It was
// not enough: they computed the WINDOW differently. The platform report used
// rangeSince, which truncates to the hour because the rollup is hourly; the
// detail screen used time.Now().AddDate(0, 0, -30), which lands mid-bucket and
// drops whatever part of the oldest hour precedes it.
//
// Live, that read 1,783 on the detail screen against 1,788 on /admin/usage —
// close enough to look like a rounding quirk and wrong enough that an operator
// reconciling the two screens cannot.
//
// The row below sits in exactly that gap: on the truncated hour boundary, which
// the correct window includes and the old one dropped for 59 minutes out of
// every 60.
func TestTheTwoOperatorUsageScreensCountTheSameWindow(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	operator := h.operatorToken()

	ctx := context.Background()
	conn, err := h.server.ClickHouse.Conn(ctx)
	if err != nil {
		t.Fatalf("clickhouse: %v", err)
	}
	batch, err := conn.PrepareBatch(ctx, `INSERT INTO message_rollup_hourly (
		tenant_id, hour, channel, country, status, message_count, cost_minor, currency)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	oldestBucket := time.Now().UTC().AddDate(0, 0, -30).Truncate(time.Hour)
	if err := batch.Append(acct.TenantID, oldestBucket, "SMS", "IN", "accepted",
		uint64(7), int64(700), "INR"); err != nil {
		t.Fatalf("append: %v", err)
	}
	// A second row well inside the window, so neither screen can pass by
	// reporting zero.
	if err := batch.Append(acct.TenantID, time.Now().UTC().Add(-2*time.Hour).Truncate(time.Hour),
		"SMS", "IN", "accepted", uint64(3), int64(300), "INR"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := batch.Send(); err != nil {
		t.Fatalf("send batch: %v", err)
	}

	detail := h.do(http.MethodGet, "/v1/operator/tenants/"+acct.TenantID.String(), operator, nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("tenant detail = %d: %s", detail.Code, detail.Body)
	}
	var tenant gen.TenantDetail
	detail.decode(t, &tenant)

	report := h.do(http.MethodGet, "/v1/operator/usage?range=30d", operator, nil)
	if report.Code != http.StatusOK {
		t.Fatalf("operator usage = %d: %s", report.Code, report.Body)
	}
	var usage gen.OperatorUsageReport
	report.decode(t, &usage)

	var onUsageScreen int
	for _, row := range usage.ByTenant {
		if row.TenantId == acct.TenantID {
			onUsageScreen = row.MessageCount
		}
	}

	if tenant.Usage.MessagesSent30d != onUsageScreen {
		t.Errorf("tenant detail says %d messages in 30d, /admin/usage says %d — "+
			"the same tenant and the same window",
			tenant.Usage.MessagesSent30d, onUsageScreen)
	}
	if tenant.Usage.MessagesSent30d != 10 {
		t.Errorf("messagesSent30d = %d, want 10 — the row on the oldest hour "+
			"boundary was dropped", tenant.Usage.MessagesSent30d)
	}
}
