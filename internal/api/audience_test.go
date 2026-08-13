package api_test

import (
	"net/http"
	"testing"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func createList(t *testing.T, h *harness, token, name string) gen.ContactList {
	t.Helper()
	res := h.do(http.MethodPost, "/v1/contact-lists", token, map[string]string{"name": name})
	if res.Code != http.StatusCreated {
		t.Fatalf("create list: status = %d, want 201; body = %s", res.Code, res.Body)
	}
	var list gen.ContactList
	res.decode(t, &list)
	return list
}

func importRows(t *testing.T, h *harness, token, listID string, rows []map[string]any, key string) gen.ImportSummary {
	t.Helper()
	body := map[string]any{
		"targetListId":   listID,
		"defaultCountry": "IN",
		"consentBasis":   map[string]string{"SMS": "opted_in"},
		"rows":           rows,
	}
	path := "/v1/contacts/import"
	res := h.doWithHeaders(http.MethodPost, path, token, body,
		map[string]string{"Idempotency-Key": key})
	if res.Code != http.StatusOK {
		t.Fatalf("import: status = %d, want 200; body = %s", res.Code, res.Body)
	}
	var summary gen.ImportSummary
	res.decode(t, &summary)
	return summary
}

func TestContactListLifecycle(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	list := createList(t, h, acct.Token, "Diwali 2026")
	if list.Name != "Diwali 2026" || list.ContactCount != 0 {
		t.Fatalf("list = %+v, want an empty list named Diwali 2026", list)
	}
	if list.ConsentedCounts == nil {
		t.Error("consentedCounts is null, want an empty object")
	}

	// A duplicate name is a 422 rather than a silent second list.
	if dup := h.do(http.MethodPost, "/v1/contact-lists", acct.Token,
		map[string]string{"name": "Diwali 2026"}); dup.Code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate name: status = %d, want 422", dup.Code)
	}

	renamed := h.do(http.MethodPatch, "/v1/contact-lists/"+list.Id.String(), acct.Token,
		map[string]string{"name": "Diwali 2026 — VIP"})
	if renamed.Code != http.StatusOK {
		t.Fatalf("rename: status = %d; body = %s", renamed.Code, renamed.Body)
	}
	var updated gen.ContactList
	renamed.decode(t, &updated)
	if updated.Name != "Diwali 2026 — VIP" {
		t.Fatalf("name = %q after rename", updated.Name)
	}

	if del := h.do(http.MethodDelete, "/v1/contact-lists/"+list.Id.String(),
		acct.Token, nil); del.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204", del.Code)
	}
	if get := h.do(http.MethodGet, "/v1/contact-lists/"+list.Id.String(),
		acct.Token, nil); get.Code != http.StatusNotFound {
		t.Fatalf("get after delete: status = %d, want 404", get.Code)
	}
}

func TestImportCreatesContactsAndCountsThem(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	list := createList(t, h, acct.Token, "Import target")

	summary := importRows(t, h, acct.Token, list.Id.String(), []map[string]any{
		{"msisdn": "9876543210", "firstName": "Asha", "line": 2},
		{"msisdn": "09876543211", "firstName": "Ravi", "line": 3},
		{"msisdn": "+919876543212", "firstName": "Meera", "line": 4},
	}, "import-key-1")

	if summary.Created != 3 || summary.Invalid != 0 || summary.Skipped != 0 {
		t.Fatalf("summary = %+v, want 3 created", summary)
	}

	// The list count must reflect the import.
	got := h.do(http.MethodGet, "/v1/contact-lists/"+list.Id.String(), acct.Token, nil)
	var updated gen.ContactList
	got.decode(t, &updated)
	if updated.ContactCount != 3 {
		t.Fatalf("contactCount = %d, want 3", updated.ContactCount)
	}
	// Consent was declared opted_in for SMS, so the tally must show it.
	if updated.ConsentedCounts["SMS"] != 3 {
		t.Fatalf("consentedCounts = %v, want SMS: 3", updated.ConsentedCounts)
	}

	contacts := h.do(http.MethodGet, "/v1/contacts?listId="+list.Id.String(), acct.Token, nil)
	var page gen.ContactPage
	contacts.decode(t, &page)
	if page.Total != 3 || len(page.Contacts) != 3 {
		t.Fatalf("page total = %d, len = %d, want 3", page.Total, len(page.Contacts))
	}
	// All three national forms must have normalised to the same E.164 shape.
	for _, contact := range page.Contacts {
		if len(contact.Msisdn) != 13 || contact.Msisdn[:3] != "+91" {
			t.Errorf("msisdn %q is not normalised E.164", contact.Msisdn)
		}
	}
}

// Re-importing the same number updates rather than duplicating. A duplicate
// contact means sending the same person twice and billing for both.
func TestImportUpsertsRatherThanDuplicating(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	list := createList(t, h, acct.Token, "Upsert target")

	importRows(t, h, acct.Token, list.Id.String(),
		[]map[string]any{{"msisdn": "9876543210", "firstName": "Asha", "line": 2}}, "upsert-1")
	second := importRows(t, h, acct.Token, list.Id.String(),
		[]map[string]any{{"msisdn": "+919876543210", "firstName": "Asha Devi", "line": 2}}, "upsert-2")

	if second.Created != 0 || second.Updated != 1 {
		t.Fatalf("summary = %+v, want 0 created and 1 updated", second)
	}
	got := h.do(http.MethodGet, "/v1/contact-lists/"+list.Id.String(), acct.Token, nil)
	var updated gen.ContactList
	got.decode(t, &updated)
	if updated.ContactCount != 1 {
		t.Fatalf("contactCount = %d after re-import, want 1", updated.ContactCount)
	}
}

// A resubmitted import must not run twice — duplicate contacts mean duplicate
// sends and duplicate charges, so this is a correctness control.
func TestImportIsIdempotent(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	list := createList(t, h, acct.Token, "Idempotent target")
	rows := []map[string]any{{"msisdn": "9876543210", "line": 2}}

	first := importRows(t, h, acct.Token, list.Id.String(), rows, "same-key")
	second := importRows(t, h, acct.Token, list.Id.String(), rows, "same-key")

	if first.Created != 1 {
		t.Fatalf("first import created %d, want 1", first.Created)
	}
	// The replay returns the original summary rather than re-running.
	if second.Created != first.Created || second.Updated != first.Updated {
		t.Fatalf("replay = %+v, want the original %+v", second, first)
	}
}

// Invalid rows are reported with the line number the client supplied, never
// guessed from array position — the contract is explicit about this because
// the client may have compacted the array while filtering.
func TestImportReportsConflictsWithRealProvenance(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	list := createList(t, h, acct.Token, "Conflict target")

	summary := importRows(t, h, acct.Token, list.Id.String(), []map[string]any{
		{"msisdn": "9876543210", "line": 7},
		{"msisdn": "not-a-number", "line": 12},
		{"msisdn": "9876543210", "line": 19}, // duplicate within the file
		{"msisdn": "123"},                    // no provenance supplied
	}, "conflict-key")

	if summary.Created != 1 {
		t.Fatalf("created = %d, want 1", summary.Created)
	}
	if summary.Invalid != 2 || summary.Skipped != 1 {
		t.Fatalf("summary = %+v, want 2 invalid and 1 skipped", summary)
	}

	// Keyed by msisdn, not reason: two rows here share the invalid_msisdn
	// reason, so a reason-keyed map would silently keep only the last.
	byMsisdn := map[string]*int{}
	reasons := map[string]string{}
	for _, conflict := range summary.Conflicts {
		byMsisdn[conflict.Msisdn] = conflict.Line
		reasons[conflict.Msisdn] = conflict.Reason
	}
	if line := byMsisdn["not-a-number"]; line == nil || *line != 12 {
		t.Errorf("invalid row line = %v, want 12", line)
	}
	if reasons["not-a-number"] != "invalid_msisdn" {
		t.Errorf("reason = %q, want invalid_msisdn", reasons["not-a-number"])
	}
	if line := byMsisdn["+919876543210"]; line == nil || *line != 19 {
		t.Errorf("duplicate row line = %v, want 19", line)
	}
	if reasons["+919876543210"] != "duplicate_in_file" {
		t.Errorf("reason = %q, want duplicate_in_file", reasons["+919876543210"])
	}
	// The row with no line must report null, not a guess.
	sawNull := false
	for _, conflict := range summary.Conflicts {
		if conflict.Line == nil {
			sawNull = true
		}
	}
	if !sawNull {
		t.Error("a row without provenance did not report a null line")
	}
}

// The product's core promise: someone who opted out stays out. A suppressed
// number must never enter a sendable list.
func TestImportSkipsSuppressedNumbers(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	list := createList(t, h, acct.Token, "Suppression target")

	added := h.do(http.MethodPost, "/v1/suppressions", acct.Token, map[string]any{
		"msisdns": []string{"+919876543210"}, "reason": "opted_out_keyword",
	})
	if added.Code != http.StatusOK {
		t.Fatalf("add suppression: status = %d; body = %s", added.Code, added.Body)
	}

	summary := importRows(t, h, acct.Token, list.Id.String(), []map[string]any{
		{"msisdn": "9876543210", "line": 2},
		{"msisdn": "9876543211", "line": 3},
	}, "suppression-key")

	if summary.Created != 1 || summary.Skipped != 1 {
		t.Fatalf("summary = %+v, want 1 created and 1 skipped", summary)
	}
	for _, conflict := range summary.Conflicts {
		if conflict.Reason == "suppressed" && conflict.Msisdn != "+919876543210" {
			t.Errorf("suppressed conflict names %q", conflict.Msisdn)
		}
	}
}

func TestSuppressionLifecycle(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPost, "/v1/suppressions", acct.Token, map[string]any{
		"msisdns": []string{"+919876543210", "00919876543211", "bad"},
		"emails":  []string{"Alice@Example.COM", "not-an-email"},
		"reason":  "manual", "note": "requested by support",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}
	var result struct {
		Created int `json:"created"`
		Skipped int `json:"skipped"`
		Invalid int `json:"invalid"`
	}
	res.decode(t, &result)
	if result.Created != 3 || result.Invalid != 2 {
		t.Fatalf("result = %+v, want 3 created and 2 invalid", result)
	}

	// Re-adding is a no-op, not an error.
	again := h.do(http.MethodPost, "/v1/suppressions", acct.Token, map[string]any{
		"msisdns": []string{"+919876543210"}, "reason": "manual",
	})
	again.decode(t, &result)
	if result.Created != 0 || result.Skipped != 1 {
		t.Fatalf("re-add = %+v, want 0 created and 1 skipped", result)
	}

	listed := h.do(http.MethodGet, "/v1/suppressions", acct.Token, nil)
	var page gen.SuppressionPage
	listed.decode(t, &page)
	if len(page.Suppressions) != 3 {
		t.Fatalf("got %d suppressions, want 3", len(page.Suppressions))
	}

	// An email suppressed with mixed case must be removable in any case.
	if removed := h.do(http.MethodDelete, "/v1/suppressions/ALICE@example.com",
		acct.Token, nil); removed.Code != http.StatusNoContent {
		t.Fatalf("remove: status = %d, want 204", removed.Code)
	}
	listed = h.do(http.MethodGet, "/v1/suppressions", acct.Token, nil)
	listed.decode(t, &page)
	if len(page.Suppressions) != 2 {
		t.Fatalf("got %d suppressions after removal, want 2", len(page.Suppressions))
	}
}

func TestAudienceIsTenantScoped(t *testing.T) {
	h := newHarness(t)
	owner := h.newAccount("owner")
	other := h.newAccount("owner")
	list := createList(t, h, owner.Token, "Private list")
	importRows(t, h, owner.Token, list.Id.String(),
		[]map[string]any{{"msisdn": "9876543210", "line": 2}}, "scoped-key")

	if res := h.do(http.MethodGet, "/v1/contact-lists/"+list.Id.String(),
		other.Token, nil); res.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant get: status = %d, want 404", res.Code)
	}

	lists := h.do(http.MethodGet, "/v1/contact-lists", other.Token, nil)
	var visible []gen.ContactList
	lists.decode(t, &visible)
	if len(visible) != 0 {
		t.Fatalf("another tenant sees %d lists, want 0", len(visible))
	}

	contacts := h.do(http.MethodGet, "/v1/contacts", other.Token, nil)
	var page gen.ContactPage
	contacts.decode(t, &page)
	if page.Total != 0 {
		t.Fatalf("another tenant sees %d contacts, want 0", page.Total)
	}
}

func TestAudienceRequiresAuthentication(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/v1/contact-lists", "/v1/contacts", "/v1/suppressions"} {
		if res := h.do(http.MethodGet, path, "", nil); res.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", path, res.Code)
		}
	}
}
