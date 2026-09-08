package api_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The user-activity export and the screen it mirrors must describe the same set.
//
// Same requirement as the audit export, and the same assertion that carries it:
// the file's row count against the paged endpoint's `total` under an identical
// filter. That is what catches an export quietly reduced to a page, which is
// the failure a walk of the file cannot see.
func TestTheUserActivityExportMatchesThePagedEndpoint(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	acct := h.newAccount("owner")
	ctx := context.Background()

	const seeded = 9
	for i := 0; i < seeded; i++ {
		if _, err := h.admin.Exec(ctx, `
			INSERT INTO user_activity (tenant_id, user_id, user_name, user_email,
			                           event_type, detail)
			VALUES ($1, $2, 'Ada Exporter', $3, 'campaign.pause', 'Paused a campaign')`,
			acct.TenantID, acct.UserID, acct.Email); err != nil {
			t.Fatalf("seed activity: %v", err)
		}
	}

	records := func(query string) [][]string {
		t.Helper()
		res := h.do(http.MethodGet, "/v1/operator/user-activity/export"+query, operator, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("export%s = %d\n%s", query, res.Code, res.Body)
		}
		if got := res.Header.Get("Content-Type"); got != "text/csv; charset=utf-8" {
			t.Errorf("content-type = %q, want text/csv; charset=utf-8 — Excel reads a "+
				"CSV without a charset as mojibake", got)
		}
		if got := res.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") ||
			!strings.Contains(got, "user-activity-") {
			t.Errorf("content-disposition = %q, want a dated attachment", got)
		}
		rows, err := csv.NewReader(bytes.NewReader(res.Body)).ReadAll()
		if err != nil {
			t.Fatalf("parse csv: %v", err)
		}
		return rows
	}

	// Columns, in the order the contract declares them.
	all := records("?tenantId=" + acct.TenantID.String())
	want := []string{"occurredAt", "tenantId", "tenantName", "userName",
		"userEmail", "eventType", "detail"}
	if len(all) == 0 || strings.Join(all[0], ",") != strings.Join(want, ",") {
		t.Fatalf("header = %v, want %v", all[0], want)
	}

	// The row count against the paged endpoint's total, under the same filter.
	res := h.do(http.MethodGet,
		"/v1/operator/user-activity?tenantId="+acct.TenantID.String()+"&limit=1", operator, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("paged endpoint = %d: %s", res.Code, res.Body)
	}
	var page struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(res.Body, &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if got := len(all) - 1; got != page.Total {
		t.Errorf("the export holds %d rows and the screen reports %d — an export "+
			"that disagrees with the list it mirrors is worse than none", got, page.Total)
	}
	if page.Total < seeded {
		t.Errorf("paged total = %d, want at least the %d seeded", page.Total, seeded)
	}

	// A filter matching nothing is a header and no rows, not the collection.
	empty := records("?tenantId=" + uuid.NewString())
	if len(empty) != 1 {
		t.Errorf("a filter matching nothing exported %d rows, want a header alone",
			len(empty)-1)
	}
}
