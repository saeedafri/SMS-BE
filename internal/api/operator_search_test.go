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

// The whole of the frontend's search ask, in one property: a row that sits
// beyond the first page must be findable on page 1 of the filtered results.
//
// Filtering after the slice would pass every naive test — the search box looks
// like it works, returns plausible rows, and quietly cannot see anything past
// the first page. That is the failure mode they described and it is why the
// filter is in the WHERE rather than over the returned page.
func TestOperatorSearchFiltersTheCollectionRatherThanThePage(t *testing.T) {
	h := newHarness(t)
	operator := h.operatorToken()
	ctx := context.Background()

	// A needle deliberately created FIRST, so newest-first ordering pushes it
	// to the back of the collection and off page one.
	// Unique per run: the test database persists between runs, so a fixed word
	// would accumulate a needle per run and the counts would drift.
	token := "zzyzx" + uuid.NewString()[:8]
	needle := "Acme " + token + " Logistics"
	var needleID uuid.UUID
	if err := h.admin.QueryRow(ctx,
		`INSERT INTO tenants (id, name, country) VALUES ($1, $2, 'IN') RETURNING id`,
		uuid.New(), needle).Scan(&needleID); err != nil {
		t.Fatalf("seed needle tenant: %v", err)
	}
	for i := 0; i < 25; i++ {
		if _, err := h.admin.Exec(ctx,
			`INSERT INTO tenants (id, name, country) VALUES ($1, $2, 'IN')`,
			uuid.New(), fmt.Sprintf("Haystack %d %s", i, uuid.NewString()[:6])); err != nil {
			t.Fatalf("seed haystack tenant %d: %v", i, err)
		}
	}

	type page struct {
		Tenants []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"tenants"`
		Total int `json:"total"`
	}
	get := func(query string) page {
		t.Helper()
		res := h.do(http.MethodGet, "/v1/operator/tenants"+query, operator, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s = %d\n%s", query, res.Code, res.Body)
		}
		var p page
		if err := json.Unmarshal([]byte(res.Body), &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return p
	}

	// The needle is not on page one unfiltered — otherwise this test proves
	// nothing about where the filter runs.
	first := get("?page=1&limit=10")
	for _, row := range first.Tenants {
		if row.ID == needleID.String() {
			t.Fatal("the needle is on page one unfiltered, so this test cannot tell " +
				"a collection filter from a page filter")
		}
	}

	found := get("?q=" + token + "&page=1&limit=10")
	if len(found.Tenants) != 1 || found.Tenants[0].ID != needleID.String() {
		t.Fatalf("q=%s returned %d rows, want the one needle sitting past page one",
			token, len(found.Tenants))
	}
	if found.Total != 1 {
		t.Errorf("total = %d, want 1 — the total must describe the filtered set, "+
			"or the pager promises pages that do not exist", found.Total)
	}

	// Substring, not prefix: "zyzx" drops the leading Z, so a prefix match
	// would find nothing. Case-insensitive in the same assertion.
	if mid := get("?q=" + strings.ToUpper(token[2:])); mid.Total != 1 {
		t.Errorf("mid-word case-insensitive substring total = %d, want 1 — "+
			"the match must be substring and case-insensitive, not prefix", mid.Total)
	}
	if byID := get("?q=" + needleID.String()[:8]); byID.Total < 1 {
		t.Error("searching by id prefix found nothing; id is a searched field")
	}

	// Empty q filters nothing, rather than matching the empty string.
	blank, none := get("?q="), get("")
	if blank.Total != none.Total {
		t.Errorf("q= total %d != unfiltered total %d — an empty search must not filter",
			blank.Total, none.Total)
	}

	// A match on nothing is an empty 200, not a 404.
	if miss := get("?q=" + uuid.NewString()); miss.Total != 0 || len(miss.Tenants) != 0 {
		t.Errorf("no-match returned total=%d rows=%d, want an empty 200",
			miss.Total, len(miss.Tenants))
	}

	// q ANDs with the existing filters rather than replacing them.
	if anded := get("?q=" + token + "&country=GB"); anded.Total != 0 {
		t.Errorf("q AND country=GB total = %d, want 0 — q must narrow the other "+
			"filters, not replace them", anded.Total)
	}
}
