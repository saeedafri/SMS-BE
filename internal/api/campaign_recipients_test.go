package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A campaign's dispatched recipients are what it SENT to, whatever consent
// says today.
//
// The defect this catches is subtle and was live: the audience rule — list
// membership, addressability, and an explicit opt-in on the channel — was
// applied to both halves of this endpoint. Consent is mutable, so filtering the
// dispatched half by it answers a question about what a campaign did with a
// fact about what is true now. A contact who opts out after being messaged
// disappears from the campaign's own record of having messaged them, and a
// campaign that ran before the consent rule existed reports nobody at all.
// Measured on production: one campaign with 4 message rows listed 2 recipients,
// another with 2,500 listed 0.
//
// Seeded so the two halves DISAGREE — every recipient here has a message row
// and none of them is currently reachable. An implementation that consults the
// audience for this half returns an empty page; one that reads the log returns
// all of them.
func TestDispatchedRecipientsAreReadFromTheLogRatherThanTodaysConsent(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	listID, campaignID := h.seedOptedOutCampaign(acct, "log over consent")

	// Every one of them is opted OUT, and every one of them was messaged.
	const recipients = 5
	identities := make([]string, 0, recipients)
	for i := 0; i < recipients; i++ {
		identities = append(identities, fmt.Sprintf("+9198770%05d", i))
	}
	h.seedContacts(acct, listID, identities, "opted_out")
	messageIDs := h.seedCampaignMessages(acct, campaignID, identities)

	page := h.recipientPage(acct, campaignID, "?state=dispatched&limit=50")

	if page.Total != recipients {
		t.Errorf("total = %d, want %d — a message row is the record that this "+
			"address was reached, and consent as it stands today cannot revise it",
			page.Total, recipients)
	}
	seen := map[string]string{}
	for _, row := range page.Recipients {
		if row.State != "dispatched" {
			t.Errorf("%s came back as %q, want dispatched", row.Identity, row.State)
		}
		if row.MessageId == nil {
			t.Errorf("%s is dispatched with no message id", row.Identity)
			continue
		}
		seen[row.Identity] = *row.MessageId
	}
	for _, identity := range identities {
		got, ok := seen[identity]
		if !ok {
			t.Errorf("%s has a message row for this campaign and is absent from "+
				"the recipients endpoint", identity)
			continue
		}
		if got != messageIDs[identity] {
			t.Errorf("%s points at message %s, want %s", identity, got, messageIDs[identity])
		}
	}
	// Nothing was cancelled, so the other half must be empty rather than
	// inheriting the dispatched rows.
	if cancelled := h.recipientPage(acct, campaignID, "?state=cancelled&limit=50"); cancelled.Total != 0 {
		t.Errorf("a campaign that completed reports %d cancelled recipients, want 0",
			cancelled.Total)
	}
}

// The unfiltered page is the two halves back to back, so a walk of every page
// sees each recipient exactly once and `total` is the sum.
//
// A cancelled campaign is the only shape where BOTH halves have rows, which is
// what makes it the only shape that can catch the block arithmetic. An earlier
// version of this test used a completed campaign, where the cancelled half is
// empty by definition — so the offset the second block is asked for never
// mattered and a mutation that dropped it entirely stayed green.
func TestRecipientPagesWalkBothHalvesWithoutOverlap(t *testing.T) {
	h := newSendHarness(t)
	acct := h.newAccount("owner")

	listID, campaignID := h.seedOptedOutCampaign(acct, "two halves")

	// Ten members, all opted in, seeded oldest first. Fan-out walks newest
	// first, so the four newest are the ones it reached before the halt.
	identities := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		identities = append(identities, fmt.Sprintf("+9198771%05d", i))
	}
	h.seedContacts(acct, listID, identities, "opted_in")

	// The cursor sits between them: everything strictly older is what the
	// cancellation caught.
	dispatched := h.haltCampaignAfter(acct, campaignID, 4)
	h.seedCampaignMessages(acct, campaignID, dispatched)

	const wantTotal = 10
	first := h.recipientPage(acct, campaignID, "?limit=3")
	if first.Total != wantTotal {
		t.Fatalf("total = %d, want %d — dispatched and cancelled are the two "+
			"halves of the same list", first.Total, wantTotal)
	}

	states := map[string]int{}
	walked := map[string]bool{}
	for page := 1; page <= 5; page++ {
		got := h.recipientPage(acct, campaignID, fmt.Sprintf("?limit=3&page=%d", page))
		if got.Total != first.Total {
			t.Errorf("page %d reports total %d, want %d — the total must not move "+
				"as the caller pages", page, got.Total, first.Total)
		}
		// A page never exceeds its limit. This is the assertion that catches
		// the block arithmetic: ask both halves for the same window and they
		// can still partition the collection cleanly with no duplicate and no
		// gap, while handing back twice as many rows as were asked for.
		if len(got.Recipients) > 3 {
			t.Errorf("page %d returned %d rows for limit=3", page, len(got.Recipients))
		}
		for _, row := range got.Recipients {
			if walked[row.Identity] {
				t.Errorf("%s appears on more than one page", row.Identity)
			}
			walked[row.Identity] = true
			states[row.State]++
		}
	}
	if len(walked) != wantTotal {
		t.Errorf("walking every page saw %d recipients, want %d — the two blocks "+
			"are contiguous, so a page that straddles them must show both",
			len(walked), wantTotal)
	}
	if states["dispatched"] != len(dispatched) {
		t.Errorf("saw %d dispatched, want %d", states["dispatched"], len(dispatched))
	}
	if states["cancelled"] != wantTotal-len(dispatched) {
		t.Errorf("saw %d cancelled, want %d", states["cancelled"], wantTotal-len(dispatched))
	}

	// And each half asked for on its own agrees with the walk.
	if only := h.recipientPage(acct, campaignID, "?state=dispatched&limit=50"); only.Total != len(dispatched) {
		t.Errorf("state=dispatched totals %d, want %d", only.Total, len(dispatched))
	}
	if only := h.recipientPage(acct, campaignID, "?state=cancelled&limit=50"); only.Total != wantTotal-len(dispatched) {
		t.Errorf("state=cancelled totals %d, want %d", only.Total, wantTotal-len(dispatched))
	}
}

// An unknown campaign is a 404, not an empty page. The two used to be the same
// response, which made "no such campaign" indistinguishable from "a campaign
// with nothing to show".
func TestUnknownCampaignRecipientsIsNotAnEmptyPage(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodGet, "/v1/campaigns/"+uuid.NewString()+"/recipients", acct.Token, nil)
	if res.Code != http.StatusNotFound {
		t.Errorf("unknown campaign = %d, want 404 (body %s)", res.Code, string(res.Body))
	}
	res = h.do(http.MethodGet,
		"/v1/campaigns/"+uuid.NewString()+"/recipients?page=0", acct.Token, nil)
	if res.Code != http.StatusUnprocessableEntity {
		t.Errorf("page=0 = %d, want 422", res.Code)
	}
}

type recipientPage struct {
	Recipients []struct {
		ContactId string  `json:"contactId"`
		Identity  string  `json:"identity"`
		State     string  `json:"state"`
		MessageId *string `json:"messageId"`
	} `json:"recipients"`
	Total int `json:"total"`
}

func (h *harness) recipientPage(acct account, campaignID, query string) recipientPage {
	h.t.Helper()
	res := h.do(http.MethodGet, "/v1/campaigns/"+campaignID+"/recipients"+query, acct.Token, nil)
	if res.Code != http.StatusOK {
		h.t.Fatalf("GET recipients%s = %d: %s", query, res.Code, res.Body)
	}
	var page recipientPage
	if err := json.Unmarshal(res.Body, &page); err != nil {
		h.t.Fatalf("decode recipients: %v", err)
	}
	return page
}

// seedOptedOutCampaign puts a completed SMS campaign and its (empty) list on
// the tenant, and returns both ids.
func (h *harness) seedOptedOutCampaign(acct account, name string) (string, string) {
	h.t.Helper()
	ctx := context.Background()
	var listID, senderID, templateID, campaignID string
	if err := h.admin.QueryRow(ctx,
		`INSERT INTO contact_lists (tenant_id, name) VALUES ($1, $2) RETURNING id`,
		acct.TenantID, name+" "+uuid.NewString()[:8]).Scan(&listID); err != nil {
		h.t.Fatalf("seed list: %v", err)
	}
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, $2, 'SMS', 'IN', 'approved') RETURNING id`,
		acct.TenantID, fmt.Sprintf("RCP%03d", h.nextSenderSeq())).Scan(&senderID); err != nil {
		h.t.Fatalf("seed sender: %v", err)
	}
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO templates (tenant_id, sender_id, name, channel, country, body, status)
		VALUES ($1, $2, $3, 'SMS', 'IN', 'Hello', 'approved') RETURNING id`,
		acct.TenantID, senderID, name+" template").Scan(&templateID); err != nil {
		h.t.Fatalf("seed template: %v", err)
	}
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO campaigns (tenant_id, name, channel, country, sender_id, template_id,
		                       list_id, status, recipients, send_started_at)
		VALUES ($1, $2, 'SMS', 'IN', $3, $4, $5, 'sent', 0, now())
		RETURNING id`, acct.TenantID, name, senderID, templateID, listID).Scan(&campaignID); err != nil {
		h.t.Fatalf("seed campaign: %v", err)
	}
	return listID, campaignID
}

// seedContacts puts contacts on a list with one consent state for SMS.
func (h *harness) seedContacts(acct account, listID string, identities []string, state string) {
	h.t.Helper()
	ctx := context.Background()
	for _, msisdn := range identities {
		var contactID string
		if err := h.admin.QueryRow(ctx, `
			INSERT INTO contacts (tenant_id, msisdn, country, fields, consent)
			VALUES ($1, $2, 'IN', '{}'::jsonb, jsonb_build_object('SMS', $3::text))
			RETURNING id`, acct.TenantID, msisdn, state).Scan(&contactID); err != nil {
			h.t.Fatalf("seed contact %s: %v", msisdn, err)
		}
		if _, err := h.admin.Exec(ctx, `
			INSERT INTO contact_list_members (tenant_id, list_id, contact_id)
			VALUES ($1, $2, $3)`, acct.TenantID, listID, contactID); err != nil {
			h.t.Fatalf("seed membership %s: %v", msisdn, err)
		}
	}
}

// seedCampaignMessages writes one delivered message row per identity and
// returns the id each became.
func (h *harness) seedCampaignMessages(acct account, campaignID string,
	identities []string) map[string]string {

	h.t.Helper()
	ctx := context.Background()
	conn, err := h.server.ClickHouse.Conn(ctx)
	if err != nil {
		h.t.Fatalf("clickhouse: %v", err)
	}
	batch, err := conn.PrepareBatch(ctx, `INSERT INTO messages (
		tenant_id, id, campaign_id, channel, country, sender_header, msisdn, status,
		fraud_flag, segments, cost_minor, currency, created_at, updated_at, version)`)
	if err != nil {
		h.t.Fatalf("prepare: %v", err)
	}
	out := map[string]string{}
	now := time.Now().UTC()
	for i, msisdn := range identities {
		id := uuid.New()
		out[msisdn] = id.String()
		if err := batch.Append(acct.TenantID, id, uuid.MustParse(campaignID), "SMS", "IN",
			"Acme", msisdn, "delivered", "none", uint8(1), int64(10), "INR",
			now.Add(-time.Duration(i)*time.Minute), now, uint64(1)); err != nil {
			h.t.Fatalf("append %s: %v", msisdn, err)
		}
	}
	if err := batch.Send(); err != nil {
		h.t.Fatalf("send batch: %v", err)
	}
	return out
}

// haltCampaignAfter cancels a campaign with its dispatch cursor parked after
// the newest `reached` members of its list, and returns those identities.
//
// The cursor is the fan-out's own encoding — base64 of "RFC3339Nano|uuid" — and
// the walk is newest-first, so parking it on the Nth newest row makes the N
// newer ones the dispatched half and everything older the cancelled one.
func (h *harness) haltCampaignAfter(acct account, campaignID string, reached int) []string {
	h.t.Helper()
	ctx := context.Background()
	rows, err := h.admin.Query(ctx, `
		SELECT c.msisdn, c.created_at, c.id
		FROM contacts c
		JOIN contact_list_members m ON m.contact_id = c.id
		JOIN campaigns k ON k.list_id = m.list_id
		WHERE k.id = $1 AND c.tenant_id = $2
		ORDER BY c.created_at DESC, c.id DESC`, campaignID, acct.TenantID)
	if err != nil {
		h.t.Fatalf("read audience: %v", err)
	}
	defer rows.Close()
	var identities []string
	var cursorAt time.Time
	var cursorID uuid.UUID
	for rows.Next() {
		var msisdn string
		var createdAt time.Time
		var id uuid.UUID
		if err := rows.Scan(&msisdn, &createdAt, &id); err != nil {
			h.t.Fatalf("scan audience: %v", err)
		}
		if len(identities) < reached {
			identities = append(identities, msisdn)
			cursorAt, cursorID = createdAt, id
		}
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("iterate audience: %v", err)
	}
	if len(identities) != reached {
		h.t.Fatalf("audience holds %d members, wanted at least %d", len(identities), reached)
	}
	cursor := base64.RawURLEncoding.EncodeToString(
		[]byte(cursorAt.UTC().Format(time.RFC3339Nano) + "|" + cursorID.String()))
	// send_started_at is moved here rather than left at creation time: the
	// audience is anchored to when the run STARTED, and these contacts are
	// seeded after the campaign row exists. Left alone, every one of them
	// post-dates its own campaign and the cancelled half is correctly empty.
	if _, err := h.admin.Exec(ctx, `
		UPDATE campaigns SET status = 'cancelled', cancelled_at = now(),
		                     send_started_at = now(), dispatch_cursor = $1
		WHERE id = $2`, cursor, campaignID); err != nil {
		h.t.Fatalf("halt campaign: %v", err)
	}
	return identities
}
