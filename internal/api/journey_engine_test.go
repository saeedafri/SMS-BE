package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saeedafri/sms-be/internal/sending"
)

// Ask 65. A journey runs: its trigger list is enrolled, send steps go through
// the campaign pipeline, waits hold, suppression exits, and the funnel counts
// what actually happened.

type journeyRig struct {
	h                        *harness
	acct                     account
	sender, template, listID string
}

func newJourneyRig(t *testing.T) journeyRig {
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	h.fundWallet(acct)
	sender, template := h.categorisedTemplate(acct, "TRANSACTIONAL")
	created := h.do(http.MethodPost, "/v1/contact-lists", acct.Token, map[string]any{"name": "Journey list"})
	var list struct {
		ID string `json:"id"`
	}
	created.decode(t, &list)
	return journeyRig{h: h, acct: acct, sender: sender, template: template, listID: list.ID}
}

func (r journeyRig) send(id string) map[string]any {
	return map[string]any{"type": "send", "id": id, "channel": "SMS",
		"senderId": r.sender, "templateId": r.template}
}

func (r journeyRig) start(trigger map[string]any, steps ...any) string {
	r.h.t.Helper()
	res := r.h.do(http.MethodPost, "/v1/automation/journeys", r.acct.Token, map[string]any{
		"name": "Engine test", "trigger": trigger, "steps": steps,
	})
	if res.Code != http.StatusCreated {
		r.h.t.Fatalf("create journey = %d\n%s", res.Code, res.Body)
	}
	var journey struct {
		ID string `json:"id"`
	}
	res.decode(r.h.t, &journey)
	if code := r.h.post(r.acct.Token, "/v1/automation/journeys/"+journey.ID+"/activate"); code != http.StatusOK {
		r.h.t.Fatalf("activate = %d", code)
	}
	return journey.ID
}

func (r journeyRig) run() {
	r.h.t.Helper()
	if err := r.h.server.RunJourneys(context.Background()); err != nil {
		r.h.t.Logf("run journeys: %v", err)
	}
}

// sentTo counts distinct messages to one number, whatever state they reached.
func (r journeyRig) sentTo(msisdn string) uint64 {
	r.h.t.Helper()
	ctx := context.Background()
	conn, err := r.h.server.ClickHouse.Conn(ctx)
	if err != nil {
		r.h.t.Fatalf("clickhouse: %v", err)
	}
	var n uint64
	if err := conn.QueryRow(ctx, `SELECT uniqExact(id) FROM messages
		WHERE tenant_id = ? AND endsWith(msisdn, ?)`, r.acct.TenantID, msisdn[3:]).Scan(&n); err != nil {
		r.h.t.Fatalf("count: %v", err)
	}
	return n
}

type funnelView struct {
	CompletedCount        int `json:"completedCount"`
	ExitedSuppressedCount int `json:"exitedSuppressedCount"`
	Funnel                struct {
		TotalEnrolled    int `json:"totalEnrolled"`
		Completed        int `json:"completed"`
		ExitedSuppressed int `json:"exitedSuppressed"`
		StepCounts       []struct {
			StepID string `json:"stepId"`
			Count  int    `json:"count"`
		} `json:"stepCounts"`
	} `json:"funnel"`
}

func (r journeyRig) funnel(id string) funnelView {
	r.h.t.Helper()
	var out funnelView
	res := r.h.do(http.MethodGet, "/v1/automation/journeys/"+id, r.acct.Token, nil)
	if res.Code != http.StatusOK {
		r.h.t.Fatalf("get journey = %d\n%s", res.Code, res.Body)
	}
	res.decode(r.h.t, &out)
	return out
}

func TestAListEntryJourneyEnrolsWhoeverJoinsTheListAndNobodyElse(t *testing.T) {
	r := newJourneyRig(t)
	first, second, outsider := "+919877300001", "+919877300002", "+919877300003"
	r.h.seedContacts(r.acct, r.listID, []string{first}, "opted_in")
	id := r.start(map[string]any{"type": "list_entry", "listId": r.listID}, r.send("s1"))

	r.run()
	r.h.seedContacts(r.acct, r.listID, []string{second}, "opted_in")
	other := r.h.do(http.MethodPost, "/v1/contact-lists", r.acct.Token, map[string]any{"name": "Elsewhere"})
	var otherList struct {
		ID string `json:"id"`
	}
	other.decode(t, &otherList)
	r.h.seedContacts(r.acct, otherList.ID, []string{outsider}, "opted_in")
	r.run()
	r.run()

	if r.sentTo(first) != 1 || r.sentTo(second) != 1 {
		t.Errorf("sends: first %d, second %d, want one each", r.sentTo(first), r.sentTo(second))
	}
	if got := r.sentTo(outsider); got != 0 {
		t.Errorf("a contact never on the list got %d messages", got)
	}
	f := r.funnel(id)
	if f.Funnel.TotalEnrolled != 2 || f.Funnel.Completed != 2 || f.CompletedCount != 2 {
		t.Errorf("funnel = %+v, want 2 enrolled and 2 completed", f)
	}
}

func TestAWaitHoldsTheNextSendAndSuppressionExitsTheJourney(t *testing.T) {
	r := newJourneyRig(t)
	stays, stops := "+919877400001", "+919877400002"
	r.h.seedContacts(r.acct, r.listID, []string{stays, stops}, "opted_in")
	id := r.start(map[string]any{"type": "list_entry", "listId": r.listID},
		r.send("s1"), map[string]any{"type": "wait", "id": "w1", "durationMinutes": 1}, r.send("s2"))

	r.run()
	r.run() // polling faster than the wait must not shorten it
	if r.sentTo(stays) != 1 || r.sentTo(stops) != 1 {
		t.Fatalf("after the first send: %d and %d, want one each", r.sentTo(stays), r.sentTo(stops))
	}
	f := r.funnel(id)
	waiting := 0
	for _, step := range f.Funnel.StepCounts {
		if step.StepID == "w1" {
			waiting = step.Count
		}
	}
	if waiting != 2 {
		t.Errorf("contacts on the wait step = %d, want 2", waiting)
	}

	if _, err := r.h.admin.Exec(context.Background(), `
		INSERT INTO suppressions (tenant_id, identity, msisdn, reason)
		VALUES ($1, $2, $2, 'opted_out_keyword')`, r.acct.TenantID, stops); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Minute)
	r.h.server.Now = func() time.Time { return later }
	r.run()

	if got := r.sentTo(stays); got != 2 {
		t.Errorf("contact who stayed got %d messages, want 2", got)
	}
	if got := r.sentTo(stops); got != 1 {
		t.Errorf("suppressed contact got %d messages, want still 1", got)
	}
	f = r.funnel(id)
	if f.Funnel.TotalEnrolled != 2 || f.Funnel.Completed != 1 || f.Funnel.ExitedSuppressed != 1 ||
		f.ExitedSuppressedCount != 1 {
		t.Errorf("funnel = %+v, want 2 enrolled, 1 completed, 1 exited suppressed", f.Funnel)
	}
}

func TestAScheduledJourneySweepsItsListOnceAtRunAt(t *testing.T) {
	r := newJourneyRig(t)
	early, late := "+919877500001", "+919877500002"
	r.h.seedContacts(r.acct, r.listID, []string{early}, "opted_in")
	runAt := time.Now().Add(time.Hour)
	r.start(map[string]any{"type": "scheduled", "listId": r.listID,
		"runAt": runAt.UTC().Format(time.RFC3339)}, r.send("s1"))

	r.run()
	if got := r.sentTo(early); got != 0 {
		t.Fatalf("sent %d before runAt", got)
	}
	r.h.server.Now = func() time.Time { return runAt.Add(time.Minute) }
	r.run()
	r.h.seedContacts(r.acct, r.listID, []string{late}, "opted_in")
	r.run()

	if got := r.sentTo(early); got != 1 {
		t.Errorf("contact on the list at runAt got %d, want 1", got)
	}
	if got := r.sentTo(late); got != 0 {
		t.Errorf("contact added after the sweep got %d, want 0", got)
	}
}

func TestAPausedJourneyNeitherEnrolsNorAdvances(t *testing.T) {
	r := newJourneyRig(t)
	msisdn := "+919877600001"
	id := r.start(map[string]any{"type": "list_entry", "listId": r.listID}, r.send("s1"))
	if code := r.h.post(r.acct.Token, "/v1/automation/journeys/"+id+"/pause"); code != http.StatusOK {
		t.Fatalf("pause = %d", code)
	}
	r.h.seedContacts(r.acct, r.listID, []string{msisdn}, "opted_in")
	r.run()
	if got := r.sentTo(msisdn); got != 0 {
		t.Errorf("paused journey sent %d", got)
	}
}

// Ask 69. A journey's messages carry which journey sent them, through the
// delivery report that replaces the row, into the log filter and the
// by-journey usage report.
func TestJourneyMessagesCarryTheirJourneyIntoTheLogAndTheUsageReport(t *testing.T) {
	r := newJourneyRig(t)
	r.h.seedContacts(r.acct, r.listID, []string{"+919876543210"}, "opted_in")
	id := r.start(map[string]any{"type": "list_entry", "listId": r.listID}, r.send("s1"))
	r.run()

	conn, err := r.h.server.ClickHouse.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	drainer := &sending.Service{DB: r.h.server.DB, ClickHouse: conn, Connector: r.h.server.Connector}
	if _, err := drainer.DrainSandboxReports(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}

	var page struct {
		Messages []struct {
			JourneyID   *string `json:"journeyId"`
			JourneyName *string `json:"journeyName"`
			CampaignID  *string `json:"campaignId"`
			Status      string  `json:"status"`
		} `json:"messages"`
	}
	r.h.do(http.MethodGet, "/v1/messages?journeyId="+id, r.acct.Token, nil).decode(t, &page)
	if len(page.Messages) != 1 {
		t.Fatalf("messages for the journey = %d, want 1", len(page.Messages))
	}
	got := page.Messages[0]
	if got.JourneyID == nil || *got.JourneyID != id || got.CampaignID != nil {
		t.Errorf("journeyId = %v, campaignId = %v; want the journey and no campaign",
			got.JourneyID, got.CampaignID)
	}
	if got.JourneyName == nil || *got.JourneyName != "Engine test" {
		t.Errorf("journeyName = %v, want Engine test", got.JourneyName)
	}

	var other struct {
		Messages []any `json:"messages"`
	}
	r.h.do(http.MethodGet, "/v1/messages?journeyId=00000000-0000-0000-0000-000000000001",
		r.acct.Token, nil).decode(t, &other)
	if len(other.Messages) != 0 {
		t.Errorf("an unrelated journey filter returned %d messages", len(other.Messages))
	}

	var usage struct {
		ByJourney []struct {
			JourneyID    string `json:"journeyId"`
			MessageCount int    `json:"messageCount"`
		} `json:"byJourney"`
	}
	r.h.do(http.MethodGet, "/v1/billing/usage", r.acct.Token, nil).decode(t, &usage)
	if len(usage.ByJourney) != 1 || usage.ByJourney[0].JourneyID != id ||
		usage.ByJourney[0].MessageCount != 1 {
		t.Errorf("byJourney = %+v, want one row for the journey with 1 message "+
			"(the message status was %q)", usage.ByJourney, got.Status)
	}
}

// Ask 70. A journey's money says the journey's name, and never a campaign's.
//
// LedgerEntry has carried JourneyID/JourneyName since ask 69 and nothing wrote
// them, so a journey's hold was recorded with a campaign id it does not have —
// and worse, with a description hardcoded to the words "Campaign hold". That
// string is not decoration: the wallet screen renders it as the entry's label
// whenever there is no journey name to link instead, so a journey send has been
// telling customers it was a campaign, on the screen about their money.
func TestAJourneysWalletEntriesNameTheJourneyAndNotACampaign(t *testing.T) {
	r := newJourneyRig(t)
	r.h.seedContacts(r.acct, r.listID, []string{"+919876543210"}, "opted_in")
	id := r.start(map[string]any{"type": "list_entry", "listId": r.listID}, r.send("s1"))
	r.run()

	var ledger struct {
		Entries []struct {
			Type        string  `json:"type"`
			Description string  `json:"description"`
			JourneyID   *string `json:"journeyId"`
			JourneyName *string `json:"journeyName"`
			CampaignID  *string `json:"campaignId"`
		} `json:"entries"`
	}
	r.h.do(http.MethodGet, "/v1/wallet/ledger", r.acct.Token, nil).decode(t, &ledger)

	var charge *struct {
		Type        string  `json:"type"`
		Description string  `json:"description"`
		JourneyID   *string `json:"journeyId"`
		JourneyName *string `json:"journeyName"`
		CampaignID  *string `json:"campaignId"`
	}
	for i := range ledger.Entries {
		if ledger.Entries[i].Type == "charge" {
			charge = &ledger.Entries[i]
			break
		}
	}
	if charge == nil {
		t.Fatalf("the journey sent but took no charge: %+v", ledger.Entries)
	}

	if charge.JourneyID == nil || *charge.JourneyID != id {
		t.Errorf("journeyId = %v, want %s — the hold is not attributed to the journey "+
			"that took it", charge.JourneyID, id)
	}
	if charge.JourneyName == nil || *charge.JourneyName != "Engine test" {
		t.Errorf("journeyName = %v, want Engine test", charge.JourneyName)
	}
	if charge.CampaignID != nil {
		t.Errorf("campaignId = %v on a journey's hold; no campaign was involved",
			*charge.CampaignID)
	}
	// The words themselves. This is what the customer reads when there is no
	// journey name to link, so "Campaign" here is a false statement about their
	// money rather than a cosmetic slip.
	if strings.Contains(charge.Description, "Campaign") {
		t.Errorf("description = %q — a journey's hold calls itself a campaign",
			charge.Description)
	}
	if !strings.Contains(charge.Description, "Journey") {
		t.Errorf("description = %q, want it to name the journey as what took the hold",
			charge.Description)
	}
}

// Ask 70's second acceptance row: the REFUND carries the journey too.
//
// A hold is taken for the whole page before anything is submitted, so a message
// the carrier rejects has money to give back — and that entry is a separate
// ledger row with its own attribution. Tagging only the charge would leave a
// journey's wallet history half-attributed: money out under the journey's name,
// money back under nothing.
//
// The sandbox rejects any number ending 000 at submit, which is the release
// path without needing a carrier to misbehave.
func TestAJourneysRefundCarriesTheJourneyToo(t *testing.T) {
	r := newJourneyRig(t)
	r.h.seedContacts(r.acct, r.listID, []string{"+919876543000"}, "opted_in")
	id := r.start(map[string]any{"type": "list_entry", "listId": r.listID}, r.send("s1"))
	r.run()

	var ledger struct {
		Entries []struct {
			Type        string  `json:"type"`
			Description string  `json:"description"`
			JourneyID   *string `json:"journeyId"`
			JourneyName *string `json:"journeyName"`
			CampaignID  *string `json:"campaignId"`
		} `json:"entries"`
	}
	r.h.do(http.MethodGet, "/v1/wallet/ledger", r.acct.Token, nil).decode(t, &ledger)

	for _, entry := range ledger.Entries {
		if entry.Type != "refund" {
			continue
		}
		if entry.JourneyID == nil || *entry.JourneyID != id {
			t.Errorf("refund journeyId = %v, want %s — the money came back to a "+
				"journey that the entry does not name", entry.JourneyID, id)
		}
		if entry.JourneyName == nil || *entry.JourneyName != "Engine test" {
			t.Errorf("refund journeyName = %v, want Engine test", entry.JourneyName)
		}
		if entry.CampaignID != nil {
			t.Errorf("refund campaignId = %v; no campaign was involved", *entry.CampaignID)
		}
		return
	}
	t.Fatalf("the rejected send released no hold, so there is no refund to attribute: %+v",
		ledger.Entries)
}
