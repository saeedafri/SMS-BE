package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// The last three lists answer an envelope, page it, and count the filtered set.
//
// One test over all three because they are one pattern and the failures are
// identical in each: a `total` that reports the page length rather than the
// collection, and — on the two that are scoped by environment — a filter
// applied after the slice, which makes `total` describe a different set from
// the rows beside it.
//
// The environment assertion is the one that cannot be faked by a walk: a
// tenant's live and test keys are different collections, and a total that
// counts both while the rows show one is wrong in a way every page agrees with.
func TestDeveloperListsAnswerAPagedEnvelope(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	ctx := context.Background()

	const live, test = 7, 3
	for i := 0; i < live; i++ {
		h.seedAPIKey(acct, "live", fmt.Sprintf("live key %d", i))
		h.seedWebhook(acct, "live", fmt.Sprintf("https://example.test/live/%d", i))
	}
	for i := 0; i < test; i++ {
		h.seedAPIKey(acct, "test", fmt.Sprintf("test key %d", i))
		h.seedWebhook(acct, "test", fmt.Sprintf("https://example.test/test/%d", i))
	}
	const lists = 6
	for i := 0; i < lists; i++ {
		if _, err := h.admin.Exec(ctx,
			`INSERT INTO contact_lists (tenant_id, name) VALUES ($1, $2)`,
			acct.TenantID, fmt.Sprintf("envelope list %d %s", i, uuid.NewString()[:6])); err != nil {
			t.Fatalf("seed list: %v", err)
		}
	}

	for _, endpoint := range []struct {
		path, rows string
		total      int
		// scoped lists are counted per environment, and the OTHER environment's
		// total proves the filter ran before the slice rather than after it.
		otherPath  string
		otherTotal int
	}{
		{"/v1/developer/api-keys?environment=live", "keys", live,
			"/v1/developer/api-keys?environment=test", test},
		{"/v1/developer/webhooks?environment=live", "webhooks", live,
			"/v1/developer/webhooks?environment=test", test},
		{"/v1/contact-lists?", "lists", lists, "", 0},
	} {
		t.Run(endpoint.rows, func(t *testing.T) {
			read := func(query string) ([]map[string]any, int) {
				t.Helper()
				res := h.do(http.MethodGet, query, acct.Token, nil)
				if res.Code != http.StatusOK {
					t.Fatalf("GET %s = %d: %s", query, res.Code, res.Body)
				}
				var page map[string]json.RawMessage
				if err := json.Unmarshal(res.Body, &page); err != nil {
					t.Fatalf("%s is not an object: %s", query, res.Body)
				}
				var rows []map[string]any
				if err := json.Unmarshal(page[endpoint.rows], &rows); err != nil {
					t.Fatalf("%s has no %q array: %s", query, endpoint.rows, res.Body)
				}
				var total int
				if err := json.Unmarshal(page["total"], &total); err != nil {
					t.Fatalf("%s has no numeric total: %s", query, res.Body)
				}
				return rows, total
			}

			// limit=1 is the assertion that catches an endpoint reporting the
			// page length as the total — the exact defect the message log had.
			first, total := read(endpoint.path + "&limit=1")
			if total != endpoint.total {
				t.Errorf("total = %d, want %d", total, endpoint.total)
			}
			if len(first) != 1 {
				t.Fatalf("limit=1 returned %d rows", len(first))
			}

			second, _ := read(endpoint.path + "&limit=1&page=2")
			if len(second) != 1 {
				t.Fatalf("page 2 returned %d rows", len(second))
			}
			if fmt.Sprint(first[0]["id"]) == fmt.Sprint(second[0]["id"]) {
				t.Errorf("page 2 re-served page 1 — paging does not advance")
			}

			// A page past the end is empty with the same total, not a wrap.
			past, pastTotal := read(fmt.Sprintf("%s&limit=%d&page=99", endpoint.path, endpoint.total))
			if len(past) != 0 {
				t.Errorf("a page past the end returned %d rows, want 0", len(past))
			}
			if pastTotal != endpoint.total {
				t.Errorf("a page past the end reports total %d, want %d", pastTotal, endpoint.total)
			}

			if endpoint.otherPath != "" {
				if _, other := read(endpoint.otherPath + "&limit=1"); other != endpoint.otherTotal {
					t.Errorf("the other environment totals %d, want %d — the filter "+
						"must run in the WHERE of the count, not after the slice",
						other, endpoint.otherTotal)
				}
			}

			// Walking every page must see the collection exactly once.
			seen := map[string]bool{}
			for page := 1; page <= endpoint.total+1; page++ {
				rows, _ := read(fmt.Sprintf("%s&limit=2&page=%d", endpoint.path, page))
				for _, row := range rows {
					id := fmt.Sprint(row["id"])
					if seen[id] {
						t.Errorf("%s appears on more than one page", id)
					}
					seen[id] = true
				}
			}
			if len(seen) != endpoint.total {
				t.Errorf("walking every page saw %d rows, want %d", len(seen), endpoint.total)
			}

			res := h.do(http.MethodGet, endpoint.path+"&page=0", acct.Token, nil)
			if res.Code != http.StatusUnprocessableEntity {
				t.Errorf("page=0 = %d, want 422", res.Code)
			}
		})
	}
}

func (h *harness) seedAPIKey(acct account, environment, name string) {
	h.t.Helper()
	if _, err := h.admin.Exec(context.Background(), `
		INSERT INTO api_keys (tenant_id, name, environment, scopes, key_prefix,
		                      key_hash, status)
		VALUES ($1, $2, $3, ARRAY['messages:write'], $4, $5, 'active')`,
		acct.TenantID, name, environment,
		"sk_"+environment+"_"+uuid.NewString()[:8], uuid.NewString()); err != nil {
		h.t.Fatalf("seed api key: %v", err)
	}
}

func (h *harness) seedWebhook(acct account, environment, url string) {
	h.t.Helper()
	if _, err := h.admin.Exec(context.Background(), `
		INSERT INTO webhook_endpoints (tenant_id, environment, url, subscribed_events,
		                               signing_secret_prefix, signing_secret_hash, status)
		VALUES ($1, $2, $3, ARRAY['message.delivered'], $4, $5, 'enabled')`,
		acct.TenantID, environment, url,
		"whsec_"+uuid.NewString()[:8], uuid.NewString()); err != nil {
		h.t.Fatalf("seed webhook: %v", err)
	}
}
