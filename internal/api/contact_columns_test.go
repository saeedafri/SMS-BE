package api_test

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The customer's own columns, the two doors that mean opposite things by a
// blank, and the consent nobody could record. Part 2 asks 1-4 and 7.

type contactBody struct {
	ID     string            `json:"id"`
	Msisdn string            `json:"msisdn"`
	Email  *string           `json:"email"`
	Fields map[string]string `json:"fields"`
}

type contactPageBody struct {
	Contacts   []contactBody `json:"contacts"`
	Total      int           `json:"total"`
	FieldNames []string      `json:"fieldNames"`
}

func number() string { return fmt.Sprintf("+9198765%05d", rand.Intn(100000)) }

func contactsIn(t *testing.T, h *harness, token, query string) contactPageBody {
	t.Helper()
	var page contactPageBody
	res := h.do(http.MethodGet, "/v1/contacts"+query, token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("GET /v1/contacts%s = %d %s", query, res.Code, res.Body)
	}
	res.decode(t, &page)
	return page
}

// A contact's columns are the customer's columns. Three fixed names meant a
// spreadsheet carrying "Order ID" lost the column between the browser and the
// database, and a template could never be filled from it.
func TestAContactKeepsEveryColumnTheCustomersFileCarried(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	list := createList(t, h, acct.Token, "Columns "+fmt.Sprint(rand.Int()))
	msisdn := number()

	importRows(t, h, acct.Token, list.Id.String(), []map[string]any{{
		"msisdn": msisdn,
		"fields": map[string]string{
			"First Name": "Rahul", "Order ID": "TX-4417", "Loyalty Tier": "Gold",
		},
	}}, "cols-"+fmt.Sprint(rand.Int()))

	page := contactsIn(t, h, acct.Token, "?listId="+list.Id.String())
	if len(page.Contacts) != 1 {
		t.Fatalf("contacts = %d, want 1", len(page.Contacts))
	}
	got := page.Contacts[0].Fields
	// Keys verbatim, spaces and capitals included: they are shown back to the
	// customer as their own column names, and normalising here would make the
	// screen disagree with their spreadsheet.
	for key, want := range map[string]string{
		"First Name": "Rahul", "Order ID": "TX-4417", "Loyalty Tier": "Gold",
	} {
		if got[key] != want {
			t.Errorf("fields[%q] = %q, want %q — the whole map is %+v", key, got[key], want, got)
		}
	}
	// fieldNames covers the whole filtered set, and the table renders one
	// column per name.
	if len(page.FieldNames) != 3 {
		t.Errorf("fieldNames = %v, want all three columns", page.FieldNames)
	}
}

// The bug: re-importing the same numbers from a file with no name column used
// to replace every stored name with an empty string. No error, no count,
// nothing on screen — the customer found out when a campaign read "Dear ,".
func TestReimportingWithoutAColumnKeepsWhatWasAlreadyStored(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	list := createList(t, h, acct.Token, "Reimport "+fmt.Sprint(rand.Int()))
	msisdn := number()

	importRows(t, h, acct.Token, list.Id.String(), []map[string]any{{
		"msisdn": msisdn,
		"fields": map[string]string{"First Name": "Rahul", "City": "Pune"},
	}}, "first-"+fmt.Sprint(rand.Int()))

	// The same number from a file with no name column at all — a suppression
	// list, an updated phone export, a re-upload of a trimmed sheet.
	importRows(t, h, acct.Token, list.Id.String(), []map[string]any{{
		"msisdn": msisdn,
		"fields": map[string]string{"City": "Mumbai"},
	}}, "second-"+fmt.Sprint(rand.Int()))

	fields := contactsIn(t, h, acct.Token, "?listId="+list.Id.String()).Contacts[0].Fields
	if fields["First Name"] != "Rahul" {
		t.Errorf("First Name = %q, want Rahul — the re-import erased it", fields["First Name"])
	}
	if fields["City"] != "Mumbai" {
		t.Errorf("City = %q, want the new Mumbai", fields["City"])
	}

	// A blank cell means "I did not fill this in", never "delete what you know".
	importRows(t, h, acct.Token, list.Id.String(), []map[string]any{{
		"msisdn": msisdn,
		"fields": map[string]string{"First Name": "   ", "City": "Delhi"},
	}}, "blank-"+fmt.Sprint(rand.Int()))
	fields = contactsIn(t, h, acct.Token, "?listId="+list.Id.String()).Contacts[0].Fields
	if fields["First Name"] != "Rahul" {
		t.Errorf("a blank cell erased the name: %q", fields["First Name"])
	}
}

// PATCH replaces, because a merge cannot express a deletion and a customer who
// empties a box on screen means it. The exact mirror of the import.
func TestCorrectingOneContactReplacesItsFieldsAndRefusesADuplicate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	list := createList(t, h, acct.Token, "Edit "+fmt.Sprint(rand.Int()))
	mine, theirs := number(), number()

	importRows(t, h, acct.Token, list.Id.String(), []map[string]any{
		{"msisdn": mine, "fields": map[string]string{"First Name": "Rahul", "City": "Mumbi"}},
		{"msisdn": theirs, "fields": map[string]string{"First Name": "Priya"}},
	}, "edit-"+fmt.Sprint(rand.Int()))

	page := contactsIn(t, h, acct.Token, "?listId="+list.Id.String())
	var id string
	for _, c := range page.Contacts {
		if c.Msisdn == mine {
			id = c.ID
		}
	}
	if id == "" {
		t.Fatal("could not find the contact just imported")
	}
	path := "/v1/contacts/" + id

	// An empty change is a 422, never a 200 to a no-op.
	if res := h.do(http.MethodPatch, path, acct.Token, map[string]any{}); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty body = %d %s, want 422", res.Code, res.Body)
	}
	// Any other key is refused by name.
	if res := h.do(http.MethodPatch, path, acct.Token,
		map[string]any{"country": "US"}); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown field = %d %s, want 422", res.Code, res.Body)
	}

	// fields REPLACES: the key not sent is gone.
	res := h.do(http.MethodPatch, path, acct.Token,
		map[string]any{"fields": map[string]string{"City": "Mumbai"}})
	if res.Code != http.StatusOK {
		t.Fatalf("patch = %d %s, want 200", res.Code, res.Body)
	}
	var updated contactBody
	res.decode(t, &updated)
	if updated.Fields["City"] != "Mumbai" {
		t.Errorf("City = %q, want Mumbai", updated.Fields["City"])
	}
	if _, still := updated.Fields["First Name"]; still {
		t.Errorf("fields merged instead of replacing: %+v", updated.Fields)
	}

	// A number another contact already has is a 409, with nothing written.
	conflict := h.do(http.MethodPatch, path, acct.Token, map[string]any{"msisdn": theirs})
	if conflict.Code != http.StatusConflict {
		t.Errorf("duplicate msisdn = %d %s, want 409", conflict.Code, conflict.Body)
	}
	after := contactsIn(t, h, acct.Token, "?listId="+list.Id.String())
	for _, c := range after.Contacts {
		if c.ID == id && c.Msisdn != mine {
			t.Errorf("a refused 409 changed the number to %s", c.Msisdn)
		}
	}

	// Normalised as the import does, so a customer can type it with a space.
	spaced := number()
	local := strings.TrimPrefix(spaced, "+91")
	if ok := h.do(http.MethodPatch, path, acct.Token,
		map[string]any{"msisdn": local[:5] + " " + local[5:]}); ok.Code != http.StatusOK {
		t.Errorf("a spaced number = %d %s, want 200", ok.Code, ok.Body)
	}
}

// Finding one person among thousands, without paging by eye.
func TestSearchingContactsCountsTheWholeCollectionNotThePage(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	list := createList(t, h, acct.Token, "Search "+fmt.Sprint(rand.Int()))

	rows := []map[string]any{}
	for i := 0; i < 6; i++ {
		name := "Priya"
		if i == 0 {
			name = "Rahul"
		}
		rows = append(rows, map[string]any{
			"msisdn": number(),
			"fields": map[string]string{"First Name": name, "City": "Pune"},
		})
	}
	importRows(t, h, acct.Token, list.Id.String(), rows, "search-"+fmt.Sprint(rand.Int()))
	base := "?listId=" + list.Id.String()

	// Case-insensitive substring, and the total counts every match rather than
	// the page — a page-local count would tell a customer searching 2,567
	// contacts that nobody is called Rahul while he sits on page 84.
	hit := contactsIn(t, h, acct.Token, base+"&"+url.QueryEscape("filter[First Name]")+"=RAH&limit=1")
	if hit.Total != 1 || len(hit.Contacts) != 1 {
		t.Errorf("filter total = %d rows = %d, want 1 and 1", hit.Total, len(hit.Contacts))
	}
	many := contactsIn(t, h, acct.Token, base+"&"+url.QueryEscape("filter[First Name]")+"=priya&limit=2")
	if many.Total != 5 || len(many.Contacts) != 2 {
		t.Errorf("total = %d rows = %d, want 5 matches over a 2-row page", many.Total, len(many.Contacts))
	}
	// Several filters AND together.
	both := contactsIn(t, h, acct.Token, base+
		"&"+url.QueryEscape("filter[First Name]")+"=rah&"+url.QueryEscape("filter[City]")+"=pune")
	if both.Total != 1 {
		t.Errorf("two filters = %d, want 1", both.Total)
	}
	if none := contactsIn(t, h, acct.Token, base+
		"&"+url.QueryEscape("filter[First Name]")+"=rah&"+
		url.QueryEscape("filter[City]")+"=delhi"); none.Total != 0 {
		t.Errorf("contradictory filters = %d, want 0", none.Total)
	}
	// An unknown column matches nothing and is NOT an error: a list can lose a
	// column between imports, and a 4xx would break a bookmarked URL.
	if unknown := contactsIn(t, h, acct.Token, base+
		"&"+url.QueryEscape("filter[Nope]")+"=x"); unknown.Total != 0 {
		t.Errorf("unknown column = %d, want 0 and no error", unknown.Total)
	}
	// An emptied search box means no filter, not "everyone matching nothing".
	if blank := contactsIn(t, h, acct.Token, base+
		"&"+url.QueryEscape("filter[First Name]")+"="); blank.Total != 6 {
		t.Errorf("blank term = %d, want every contact", blank.Total)
	}
	// msisdn is searchable too.
	if byNumber := contactsIn(t, h, acct.Token, base+
		"&"+url.QueryEscape("filter[msisdn]")+"=98765"); byNumber.Total == 0 {
		t.Error("searching by number found nothing")
	}
}

// Before this, the only door that could write consent was a spreadsheet upload.
func TestRecordingConsentNeverOptsBackInSomebodyWhoOptedOut(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	list := createList(t, h, acct.Token, "Consent "+fmt.Sprint(rand.Int()))
	out := number()

	importRows(t, h, acct.Token, list.Id.String(), []map[string]any{
		{"msisdn": number(), "fields": map[string]string{"n": "a"}},
		{"msisdn": number(), "fields": map[string]string{"n": "b"}},
		{"msisdn": out, "fields": map[string]string{"n": "c"}},
	}, "consent-"+fmt.Sprint(rand.Int()))

	// One of them has explicitly said no to RCS.
	if _, err := h.admin.Exec(h.t.Context(),
		`UPDATE contacts SET consent = consent || '{"RCS":"opted_out"}'::jsonb WHERE msisdn = $1`,
		out); err != nil {
		t.Fatalf("seed an opt-out: %v", err)
	}

	declaration := "These contacts gave us permission to message them on this channel."
	body := map[string]any{
		"listId": list.Id.String(), "channel": "RCS", "state": "opted_in",
		"declaration": declaration,
	}
	res := h.do(http.MethodPost, "/v1/contacts/consent", acct.Token, body)
	if res.Code != http.StatusOK {
		t.Fatalf("consent = %d %s, want 200", res.Code, res.Body)
	}
	var result struct {
		Updated    int    `json:"updated"`
		RecordedAt string `json:"recordedAt"`
		RecordedBy string `json:"recordedBy"`
	}
	res.decode(t, &result)
	// The two whose RCS was unknown, and NOT the one who opted out.
	if result.Updated != 2 {
		t.Errorf("updated = %d, want 2 — the opted-out contact must be untouched", result.Updated)
	}
	if result.RecordedBy == "" || result.RecordedAt == "" {
		t.Errorf("result = %+v, want it to say who recorded it and when", result)
	}

	var stillOut string
	if err := h.admin.QueryRow(h.t.Context(),
		`SELECT consent ->> 'RCS' FROM contacts WHERE msisdn = $1`, out).Scan(&stillOut); err != nil {
		t.Fatal(err)
	}
	if stillOut != "opted_out" {
		t.Errorf("the opted-out contact is now %q — a box ticked about a list must not overrule a person", stillOut)
	}

	// Running it again changes nobody, and does not restart a session clock.
	again := h.do(http.MethodPost, "/v1/contacts/consent", acct.Token, body)
	var second struct {
		Updated int `json:"updated"`
	}
	again.decode(t, &second)
	if second.Updated != 0 {
		t.Errorf("a repeat recorded %d again, want 0", second.Updated)
	}

	// The declaration is the point of the record and is stored with it.
	var declared string
	if err := h.admin.QueryRow(h.t.Context(),
		`SELECT declaration FROM contact_consent_records WHERE list_id = $1 ORDER BY recorded_at LIMIT 1`,
		list.Id).Scan(&declared); err != nil {
		t.Fatalf("no consent record was filed: %v", err)
	}
	if declared != declaration {
		t.Errorf("stored declaration = %q, want the sentence shown on screen", declared)
	}

	for name, bad := range map[string]map[string]any{
		"blank declaration": {"listId": list.Id.String(), "channel": "RCS", "state": "opted_in", "declaration": "   "},
		"no declaration":    {"listId": list.Id.String(), "channel": "RCS", "state": "opted_in"},
		"unknown channel":   {"listId": list.Id.String(), "channel": "PIGEON", "state": "opted_in", "declaration": "x"},
		"unknown state":     {"listId": list.Id.String(), "channel": "RCS", "state": "maybe", "declaration": "x"},
	} {
		if res := h.do(http.MethodPost, "/v1/contacts/consent", acct.Token, bad); res.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d %s, want 422", name, res.Code, res.Body)
		}
	}
	if res := h.do(http.MethodPost, "/v1/contacts/consent", acct.Token, map[string]any{
		"listId": "6f1d1f6a-0000-4000-8000-000000000000", "channel": "RCS",
		"state": "opted_in", "declaration": "x",
	}); res.Code != http.StatusNotFound {
		t.Errorf("unknown list = %d, want 404", res.Code)
	}
}
