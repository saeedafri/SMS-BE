package api_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// The export and the screen must describe the same set.
//
// The requirement worth testing is not "it produces a CSV" — it is that the
// filters mean the same thing in both places. An export that silently drops the
// operator's filters is worse than no export, because nobody finds out until
// they open the file.
func TestTheAuditExportHonoursTheSameFiltersAsTheList(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	ctx := context.Background()

	tenantID := uuid.New()
	for i := 0; i < 7; i++ {
		if err := store.RecordOperatorAction(ctx, h.pool, "exporter@relay.internal",
			"route.enable", nil, "", "Jio Direct", "Enabled the Jio Direct route"); err != nil {
			t.Fatalf("seed audit row: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := store.RecordOperatorAction(ctx, h.pool, "exporter@relay.internal",
			"route.disable", &tenantID, "Acme", "Vi Direct", "Disabled the Vi Direct route"); err != nil {
			t.Fatalf("seed audit row: %v", err)
		}
	}

	records := func(query string) [][]string {
		t.Helper()
		res := h.do(http.MethodGet, "/v1/operator/audit-log/export"+query, operator, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("export%s = %d\n%s", query, res.Code, res.Body)
		}
		rows, err := csv.NewReader(bytes.NewReader(res.Body)).ReadAll()
		if err != nil {
			t.Fatalf("parse csv: %v", err)
		}
		return rows
	}
	listTotal := func(query string) int {
		t.Helper()
		res := h.do(http.MethodGet, "/v1/operator/audit-log"+query, operator, nil)
		var body struct {
			Total int `json:"total"`
		}
		res.decode(t, &body)
		return body.Total
	}

	header := []string{"occurredAt", "actor", "action", "tenantId", "tenantName",
		"targetLabel", "detail"}

	all := records("?range=90d")
	if len(all) == 0 {
		t.Fatal("empty export, not even a header")
	}
	for i, column := range header {
		if all[0][i] != column {
			t.Fatalf("column %d is %q, want %q — the file's columns are the screen's",
				i, all[0][i], column)
		}
	}

	// The decisive one: row count equals the paged endpoint's total under the
	// identical filter, with no page cap in the way.
	filtered := records("?range=90d&action=route.disable")
	if got, want := len(filtered)-1, listTotal("?range=90d&action=route.disable"); got != want {
		t.Errorf("export returned %d rows, the list totals %d under the same filter — "+
			"an export that ignores the filters is worse than none", got, want)
	}
	if len(filtered)-1 >= len(all)-1 {
		t.Errorf("the filtered export (%d) is not smaller than the unfiltered one (%d)",
			len(filtered)-1, len(all)-1)
	}
	for _, row := range filtered[1:] {
		if row[2] != "route.disable" {
			t.Errorf("export carries a %q row under action=route.disable", row[2])
			break
		}
	}

	// A filter matching nothing is a header and no data rows — never the whole
	// log. The same widening rule the status filter now enforces. A tenant id
	// nobody has used is the only filter guaranteed to match nothing in a
	// database other tests are writing to.
	unknown := "?range=90d&tenantId=" + uuid.NewString()
	empty := records(unknown)
	if len(empty) != 1 {
		t.Errorf("a filter matching nothing produced %d data rows, want none",
			len(empty)-1)
	}
	if got := listTotal(unknown); got != 0 {
		t.Errorf("the list totals %d for the same unmatched filter", got)
	}

	// It downloads rather than renders, and declares its charset.
	res := h.do(http.MethodGet, "/v1/operator/audit-log/export?range=90d", operator, nil)
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "charset=utf-8") {
		t.Errorf("Content-Type = %q, want a declared utf-8 charset — Excel reads a "+
			"CSV without one as mojibake", ct)
	}
	if cd := res.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("Content-Disposition = %q, want an attachment", cd)
	}
}
