package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The campaigns list filters the collection, not the page.
//
// Same property the operator search test proves for tenants, and the same
// reason: all three of these filters ran in the browser over the whole
// collection, so paging the list without moving them server-side would narrow
// a working filter to whatever happened to be on page one.
func TestCampaignFiltersNarrowTheCollectionRatherThanThePage(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")

	// The needle is seeded FIRST so newest-first ordering pushes it off page
	// one — otherwise the test cannot tell a collection filter from a page one.
	token := "zzyzx" + uuid.NewString()[:8]
	h.seedNamedCampaign(acct, "Acme "+token+" Promo", "queued")
	for i := 0; i < 12; i++ {
		h.seedCampaign(acct, "queued")
	}

	type page struct {
		Campaigns []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Status  string `json:"status"`
			Channel string `json:"channel"`
		} `json:"campaigns"`
		Total int `json:"total"`
	}
	get := func(query string) page {
		t.Helper()
		res := h.do(http.MethodGet, "/v1/campaigns"+query, acct.Token, nil)
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

	first := get("?page=1&limit=5")
	for _, c := range first.Campaigns {
		if strings.Contains(c.Name, token) {
			t.Fatal("the needle is on page one unfiltered, so this test cannot tell " +
				"a collection filter from a page filter")
		}
	}

	found := get("?q=" + token + "&page=1&limit=5")
	if found.Total != 1 || len(found.Campaigns) != 1 {
		t.Fatalf("q=%s returned total=%d rows=%d, want the one needle past page one",
			token, found.Total, len(found.Campaigns))
	}
	if !strings.Contains(found.Campaigns[0].Name, token) {
		t.Errorf("q returned %q, not the needle", found.Campaigns[0].Name)
	}

	// Substring and case-insensitive, not prefix.
	if mid := get("?q=" + strings.ToUpper(token[2:])); mid.Total != 1 {
		t.Errorf("mid-word case-insensitive total = %d, want 1", mid.Total)
	}
	// Empty q filters nothing.
	if blank := get("?q="); blank.Total != all.Total {
		t.Errorf("q= total %d != unfiltered %d", blank.Total, all.Total)
	}
	// No match is an empty 200, never the unfiltered set.
	if miss := get("?q=" + uuid.NewString()); miss.Total != 0 || len(miss.Campaigns) != 0 {
		t.Errorf("no-match total=%d rows=%d, want an empty 200", miss.Total, len(miss.Campaigns))
	}

	// status and channel filter, and every returned row really carries them.
	queued := get("?status=queued&limit=100")
	if queued.Total == 0 || queued.Total > all.Total {
		t.Errorf("status=queued total = %d against %d unfiltered", queued.Total, all.Total)
	}
	for _, c := range queued.Campaigns {
		if c.Status != "queued" {
			t.Errorf("status=queued returned a %q campaign", c.Status)
			break
		}
	}
	sms := get("?channel=SMS&limit=100")
	for _, c := range sms.Campaigns {
		if c.Channel != "SMS" {
			t.Errorf("channel=SMS returned a %q campaign", c.Channel)
			break
		}
	}

	// All three AND rather than replace each other.
	if anded := get("?q=" + token + "&channel=EMAIL"); anded.Total != 0 {
		t.Errorf("q AND channel=EMAIL total = %d, want 0 — the filters must narrow "+
			"each other, not replace", anded.Total)
	}
}
