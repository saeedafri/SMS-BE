package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The three catalogue lists page, filter the collection rather than the page,
// and total the filtered set.
//
// One test over all three because they are one pattern, and the failure that
// matters is identical in each: a filter applied after the slice looks like it
// works and cannot see anything past page one.
// seedTemplateNamed puts one approved SMS template on the tenant, with the
// sender it needs.
func (h *harness) seedTemplateNamed(tenant account, name string) {
	h.t.Helper()
	ctx := context.Background()
	var senderID string
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, $2, 'SMS', 'IN', 'approved') RETURNING id`,
		tenant.TenantID, fmt.Sprintf("ENV%03d", h.nextSenderSeq())).Scan(&senderID); err != nil {
		h.t.Fatalf("seed sender: %v", err)
	}
	if _, err := h.admin.Exec(ctx, `
		INSERT INTO templates (tenant_id, sender_id, name, channel, country, body, status)
		VALUES ($1, $2, $3, 'SMS', 'IN', 'Hello', 'approved')`,
		tenant.TenantID, senderID, name); err != nil {
		h.t.Fatalf("seed template: %v", err)
	}
}

func TestCatalogueListsFilterTheCollectionRatherThanThePage(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	// The needle is seeded first so newest-first ordering pushes it off page one.
	token := "zzyzx" + uuid.NewString()[:8]
	h.seedTemplateNamed(acct, "Acme "+token+" Template")
	for i := 0; i < 12; i++ {
		h.seedTemplateNamed(acct, fmt.Sprintf("Haystack %d %s", i, uuid.NewString()[:6]))
	}

	type page struct {
		Templates []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"templates"`
		Total int `json:"total"`
	}
	get := func(query string) page {
		t.Helper()
		res := h.do(http.MethodGet, "/v1/templates"+query, acct.Token, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s = %d\n%s", query, res.Code, res.Body)
		}
		var p page
		if err := json.Unmarshal([]byte(res.Body), &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return p
	}

	all := get("?page=1&limit=100")
	if all.Total < 13 {
		t.Fatalf("total = %d, want the 13 just seeded", all.Total)
	}

	first := get("?page=1&limit=5")
	if len(first.Templates) != 5 {
		t.Fatalf("page of 5 returned %d", len(first.Templates))
	}
	for _, row := range first.Templates {
		if strings.Contains(row.Name, token) {
			t.Fatal("the needle is on page one unfiltered, so this test cannot tell " +
				"a collection filter from a page filter")
		}
	}

	found := get("?q=" + token + "&page=1&limit=5")
	if found.Total != 1 || len(found.Templates) != 1 {
		t.Fatalf("q=%s returned total=%d rows=%d, want the one needle past page one",
			token, found.Total, len(found.Templates))
	}
	if mid := get("?q=" + strings.ToUpper(token[2:])); mid.Total != 1 {
		t.Errorf("mid-word case-insensitive total = %d, want 1", mid.Total)
	}
	if blank := get("?q="); blank.Total != all.Total {
		t.Errorf("q= total %d != unfiltered %d — an empty search must not filter",
			blank.Total, all.Total)
	}
	if miss := get("?q=" + uuid.NewString()); miss.Total != 0 || len(miss.Templates) != 0 {
		t.Errorf("no-match total=%d rows=%d, want an empty 200", miss.Total, len(miss.Templates))
	}
	if anded := get("?q=" + token + "&channel=EMAIL"); anded.Total != 0 {
		t.Errorf("q AND channel=EMAIL total = %d, want 0", anded.Total)
	}

	// Paging covers the collection exactly once — the assertion that catches a
	// pager reporting more than it can reach.
	seen := map[string]int{}
	for p := 1; p <= 20; p++ {
		rows := get(fmt.Sprintf("?page=%d&limit=5", p)).Templates
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			seen[row.ID]++
		}
	}
	if len(seen) != all.Total {
		t.Errorf("paging reached %d distinct templates, total says %d", len(seen), all.Total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("template %s appeared on %d pages", id, n)
		}
	}

	// page < 1 is a 422 on all three, not a silent clamp.
	for _, path := range []string{"/v1/templates", "/v1/sender-ids", "/v1/automation/journeys"} {
		if res := h.do(http.MethodGet, path+"?page=0", acct.Token, nil); res.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s?page=0 = %d, want 422", path, res.Code)
		}
	}

	// The other two answer an envelope rather than a bare array.
	for _, spec := range []struct{ path, field string }{
		{"/v1/sender-ids", "senderIds"},
		{"/v1/automation/journeys", "journeys"},
	} {
		res := h.do(http.MethodGet, spec.path+"?page=1&limit=5", acct.Token, nil)
		var body map[string]any
		if err := json.Unmarshal([]byte(res.Body), &body); err != nil {
			t.Errorf("%s did not decode as an object: %v", spec.path, err)
			continue
		}
		if _, ok := body[spec.field]; !ok {
			t.Errorf("%s has no %q key — still a bare array?", spec.path, spec.field)
		}
		if _, ok := body["total"]; !ok {
			t.Errorf("%s has no total", spec.path)
		}
	}
}
