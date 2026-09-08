package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// A page of campaigns costs ONE read of the message log, not one per campaign.
//
// The counts were correct before this and the endpoint was still wrong: the
// list rendered each campaign by asking ClickHouse for its own breakdown, so a
// page of twenty cost twenty round trips against a pool of sixteen
// connections. Invisible with one reader and fatal with a hundred — measured on
// production, /v1/campaigns fell from 565 requests a second at 16 concurrent
// readers to 2.2 at 128, while the same page over Postgres alone held 630.
//
// Asserted by counting the log's own queries rather than by timing anything, so
// it fails on a laptop and in CI for the same reason it failed in production.
func TestACampaignPageReadsTheMessageLogOnce(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")

	const campaigns = 6
	ids := make([]string, 0, campaigns)
	for i := 0; i < campaigns; i++ {
		_, campaignID := h.seedOptedOutCampaign(acct, fmt.Sprintf("counted %d", i))
		h.seedCampaignMessages(acct, campaignID,
			[]string{fmt.Sprintf("+9198772%05d", i)})
		ids = append(ids, campaignID)
	}

	before := h.clickhouseSelects()
	res := h.do(http.MethodGet, "/v1/campaigns?limit=20", acct.Token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", res.Code, res.Body)
	}
	after := h.clickhouseSelects()

	var page struct {
		Campaigns []struct {
			Id     string `json:"id"`
			Counts struct {
				Delivered int `json:"delivered"`
			} `json:"counts"`
		} `json:"campaigns"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(res.Body, &page); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The counts still have to be RIGHT, or "one query" is trivially satisfied
	// by not querying at all.
	seen := map[string]int{}
	for _, c := range page.Campaigns {
		seen[c.Id] = c.Counts.Delivered
	}
	for _, id := range ids {
		if seen[id] != 1 {
			t.Errorf("campaign %s reports %d delivered, want 1", id, seen[id])
		}
	}

	// One SELECT for the whole page. Two would be a regression worth catching;
	// the old code issued one per campaign.
	if queries := after - before; queries > 2 {
		t.Errorf("rendering %d campaigns issued %d message-log queries, want 1 — "+
			"a page must not cost a round trip per row", len(page.Campaigns), queries)
	}
}

// clickhouseSelects reads the server's own count of SELECT queries, from
// ClickHouse's system tables, so the assertion is about round trips rather than
// about elapsed time.
func (h *harness) clickhouseSelects() int {
	h.t.Helper()
	conn, err := h.server.ClickHouse.Conn(h.t.Context())
	if err != nil {
		h.t.Fatalf("clickhouse: %v", err)
	}
	var total uint64
	if err := conn.QueryRow(h.t.Context(),
		`SELECT value FROM system.events WHERE event = 'SelectQuery'`).Scan(&total); err != nil {
		h.t.Fatalf("read query counter: %v", err)
	}
	return int(total)
}
