package api_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// The four properties the frontend asked us to prove, on the endpoint they
// named as highest priority.
//
// Every operator list screen renders "Showing 1 to 20 of 54" beside a pager.
// Before this, seven of the eight operator endpoints declared cursor and limit
// and read neither, and no operator page carried total — so ?limit=1 returned
// all 19 rows and the footer had no denominator. The screens absorbed it by
// exhausting the pager and counting what arrived, which worked only because
// there was never a second page.
//
// That is why total and paging had to land together: paging alone would
// have started those screens walking pages they previously got in
// one request, to compute a number the server could have sent in one field.
func TestAuditLogPagesAndCountsWhatTheFilterMatches(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()

	// Two actions so one can be filtered for. Seven and three is deliberate:
	// the totals must be distinguishable from each other and from the page size.
	ctx := context.Background()
	tenantID := uuid.New()
	for i := 0; i < 7; i++ {
		if err := store.RecordOperatorAction(ctx, h.pool, "pager@relay.internal",
			"route.enable", nil, "", "Jio Direct", "Enabled the Jio Direct route"); err != nil {
			t.Fatalf("seed audit row: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := store.RecordOperatorAction(ctx, h.pool, "pager@relay.internal",
			"route.disable", &tenantID, "Acme", "Vi Direct", "Disabled the Vi Direct route"); err != nil {
			t.Fatalf("seed audit row: %v", err)
		}
	}

	type page struct {
		Entries []struct {
			ID     string `json:"id"`
			Action string `json:"action"`
			Detail string `json:"detail"`
		} `json:"entries"`
		Total int `json:"total"`
	}
	get := func(query string) page {
		t.Helper()
		res := h.do(http.MethodGet, "/v1/operator/audit-log"+query, operator, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s = %d\n%s", query, res.Code, res.Body)
		}
		var out page
		res.decode(t, &out)
		return out
	}

	// 1. limit is honoured and a cursor is issued.
	first := get("?range=90d&limit=5")
	if len(first.Entries) != 5 {
		t.Fatalf("limit=5 returned %d rows — the limit is being ignored", len(first.Entries))
	}
	if first.Total <= 5 {
		t.Fatalf("total = %d, want more than the page size", first.Total)
	}
	if first.Total <= len(first.Entries) {
		t.Fatalf("total = %d with %d rows on the page, so a client cannot tell "+
			"'no more data' from 'paging not implemented'", first.Total, len(first.Entries))
	}

	// 2. The cursor walks: page two is different rows.
	second := get("?range=90d&limit=5&page=2")
	if len(second.Entries) == 0 {
		t.Fatal("page two is empty")
	}
	seen := map[string]bool{}
	for _, e := range first.Entries {
		seen[e.ID] = true
	}
	for _, e := range second.Entries {
		if seen[e.ID] {
			t.Fatalf("row %s appears on both pages — the cursor is not advancing", e.ID)
		}
	}
	if second.Total != first.Total {
		t.Errorf("total moved between pages: %d then %d", first.Total, second.Total)
	}

	// 3. total tracks the filter. A total that ignored filters would read as the
	//    filter being broken: "Showing 1 to 20 of 4,182" over thirty rows.
	filtered := get("?range=90d&limit=5&action=route.disable")
	if filtered.Total >= first.Total {
		t.Fatalf("filtered total = %d, unfiltered = %d — total ignores the filter",
			filtered.Total, first.Total)
	}
	if filtered.Total < 3 {
		t.Fatalf("filtered total = %d, want at least the 3 rows just written", filtered.Total)
	}

	// 4. Walking every page yields exactly total rows — the assertion that
	// catches a pager reporting more than it can actually reach.
	walked := 0
	for page := 1; page <= 100; page++ {
		p := get(fmt.Sprintf("?range=90d&limit=4&action=route.disable&page=%d", page))
		if len(p.Entries) == 0 {
			break
		}
		walked += len(p.Entries)
	}
	if walked != filtered.Total {
		t.Errorf("walking every page yielded %d rows, total said %d", walked, filtered.Total)
	}
}

// detail is the Audit table's row header — the line an operator quotes into an
// incident write-up. Nine of ten live rows were empty, because most call sites
// passed "". The frontend fell back to targetLabel, which names WHAT was
// touched without saying what changed about it.
func TestOperatorActionsRecordAReadableDetail(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	acct := h.newAccount("owner")

	suspend := h.do(http.MethodPost, "/v1/operator/tenants/"+acct.TenantID.String()+"/suspend",
		operator, nil)
	if suspend.Code != http.StatusOK {
		t.Fatalf("suspend = %d\n%s", suspend.Code, suspend.Body)
	}

	res := h.do(http.MethodGet, "/v1/operator/audit-log?range=90d&action=tenant.suspend",
		operator, nil)
	var out struct {
		Entries []struct {
			Action string `json:"action"`
			Detail string `json:"detail"`
		} `json:"entries"`
	}
	res.decode(t, &out)
	if len(out.Entries) == 0 {
		t.Fatal("the suspension was not recorded")
	}
	if out.Entries[0].Detail == "" {
		t.Error("detail is empty — the Audit table's most prominent column has nothing in it")
	}
}

// Approvals write a readable detail too, not only rejections.
//
// A rejection always carried its reason; an approval passed the same empty
// string, so every approve row — half the Audit table — had a blank in its most
// prominent column.
func TestApprovingASenderWritesAReadableDetail(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	acct := h.newAccount("owner")

	created := h.do(http.MethodPost, "/v1/sender-ids", acct.Token, map[string]any{
		"header": "AUDTDT", "channel": "SMS", "country": "IN",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create sender = %d\n%s", created.Code, created.Body)
	}
	var sender struct {
		Id string `json:"id"`
	}
	created.decode(t, &sender)

	if res := h.do(http.MethodPost, "/v1/operator/senders/"+sender.Id+"/approve",
		operator, nil); res.Code != http.StatusOK {
		t.Fatalf("approve = %d\n%s", res.Code, res.Body)
	}

	res := h.do(http.MethodGet, "/v1/operator/audit-log?range=90d&action=sender.approve",
		operator, nil)
	var log struct {
		Entries []struct {
			TargetLabel string `json:"targetLabel"`
			Detail      string `json:"detail"`
		} `json:"entries"`
	}
	res.decode(t, &log)
	for _, entry := range log.Entries {
		if entry.TargetLabel != "AUDTDT" {
			continue
		}
		if entry.Detail == "" {
			t.Fatal("an approval wrote an empty detail — the Audit table's row header is blank")
		}
		return
	}
	t.Fatal("the approval was not audited")
}
