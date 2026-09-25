package api_test

import (
	"context"
	"encoding/csv"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"
)

type opAuthor struct {
	UserID *string `json:"userId"`
	Name   *string `json:"name"`
	Email  *string `json:"email"`
}

type opCounts struct {
	Total, Queued, Sent, Delivered, Read, Failed, Rejected int
}

type opCampaign struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	TenantID   string    `json:"tenantId"`
	TenantName string    `json:"tenantName"`
	ListName   *string   `json:"listName"`
	Sender     string    `json:"sender"`
	CreatedBy  *opAuthor `json:"createdBy"`
	Messages   opCounts  `json:"messages"`
}

type opSentBy struct {
	Kind      string  `json:"kind"`
	Via       string  `json:"via"`
	Name      *string `json:"name"`
	Email     *string `json:"email"`
	KeyPrefix *string `json:"keyPrefix"`
}

type opMessage struct {
	ID          string    `json:"id"`
	TenantName  string    `json:"tenantName"`
	Source      string    `json:"source"`
	To          string    `json:"to"`
	Status      string    `json:"status"`
	State       string    `json:"state"`
	ErrorCode   *string   `json:"errorCode"`
	SentBy      *opSentBy `json:"sentBy"`
	DeliveredAt *string   `json:"deliveredAt"`
	ReadAt      *string   `json:"readAt"`
	Events      []struct {
		To     string `json:"to"`
		Detail string `json:"detail"`
	} `json:"events"`
}

func (h *harness) operatorGet(token, path string, into any) {
	h.t.Helper()
	res := h.do(http.MethodGet, path, token, nil)
	if res.Code != http.StatusOK {
		h.t.Fatalf("GET %s = %d\n%s", path, res.Code, res.Body)
	}
	res.decode(h.t, into)
}

func (h *harness) drainSandbox() {
	h.t.Helper()
	conn, err := h.server.ClickHouse.Conn(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	drainer := &sending.Service{DB: h.server.DB, ClickHouse: conn, Connector: h.server.Connector}
	if _, err := drainer.DrainSandboxReports(context.Background()); err != nil {
		h.t.Fatalf("drain: %v", err)
	}
}

// launchCampaign creates a real campaign through the API, the way the
// dashboard does, to a list of the given numbers, and waits for fan-out.
func (h *harness) launchCampaign(tenant account, ops, name string, numbers []string) string {
	h.t.Helper()
	sender, template := h.categorisedTemplate(tenant, "TRANSACTIONAL")
	created := h.do(http.MethodPost, "/v1/contact-lists", tenant.Token,
		map[string]any{"name": name + " list"})
	var list struct {
		ID string `json:"id"`
	}
	created.decode(h.t, &list)
	h.seedContacts(tenant, list.ID, numbers, "opted_in")
	res := h.do(http.MethodPost, "/v1/campaigns", tenant.Token, map[string]any{
		"name": name, "channel": "SMS", "country": "IN",
		"senderId": sender, "templateId": template, "listId": list.ID,
	})
	if res.Code != http.StatusCreated && res.Code != http.StatusAccepted && res.Code != http.StatusOK {
		h.t.Fatalf("create campaign = %d\n%s", res.Code, res.Body)
	}
	var campaign struct {
		ID string `json:"id"`
	}
	res.decode(h.t, &campaign)
	waitFor(h.t, "campaign fan-out", func() bool {
		var page struct {
			Total int `json:"total"`
		}
		h.operatorGet(ops, "/v1/operator/messages?campaignId="+campaign.ID, &page)
		return page.Total == len(numbers)
	})
	return campaign.ID
}

// The operator's question, whole: which tenant sent which campaign, who in
// that tenant created it, to whom each message went, and what became of it.
func TestOperatorSeesEveryTenantsCampaignWithItsAuthorAndOutcomes(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acme, other := h.newAccount("owner"), h.newAccount("owner")
	h.fundWallet(acme)
	h.fundWallet(other)
	ops := h.operatorToken()

	// The sandbox decides by the last digits: …010 is delivered, …001 fails
	// as an absent subscriber, …000 is refused by the carrier at submit.
	delivered, absent, refused := "+919877500010", "+919877500001", "+919877500000"
	campaignID := h.launchCampaign(acme, ops, "Diwali offer", []string{delivered, absent, refused})
	otherID := h.launchCampaign(other, ops, "Other tenant", []string{"+919877510010"})
	h.drainSandbox()

	var page struct {
		Campaigns []opCampaign `json:"campaigns"`
		Total     int          `json:"total"`
	}
	h.operatorGet(ops, "/v1/operator/campaigns?tenantId="+acme.TenantID.String(), &page)
	if page.Total != 1 || len(page.Campaigns) != 1 {
		t.Fatalf("tenant filter: total %d rows %d, want the one campaign", page.Total, len(page.Campaigns))
	}
	got := page.Campaigns[0]
	if got.ID != campaignID || got.Name != "Diwali offer" || got.TenantID != acme.TenantID.String() {
		t.Errorf("campaign = %+v", got)
	}
	if got.TenantName == "" || got.Sender == "" || got.ListName == nil {
		t.Errorf("tenant %q, sender %q, list %v — the console must name all three",
			got.TenantName, got.Sender, got.ListName)
	}
	if got.CreatedBy == nil || got.CreatedBy.Email == nil || *got.CreatedBy.Email != acme.Email ||
		got.CreatedBy.Name == nil || *got.CreatedBy.Name != "Test User" {
		t.Errorf("createdBy = %+v, want the user who created it (%s)", got.CreatedBy, acme.Email)
	}
	if m := got.Messages; m.Total != 3 || m.Delivered != 1 || m.Failed != 2 {
		t.Errorf("messages = %+v, want 3 total, 1 delivered, 2 failed", m)
	}

	// Unfiltered, both tenants' campaigns are there; a name search finds one.
	h.operatorGet(ops, "/v1/operator/campaigns?q=Other+tenant", &page)
	if page.Total != 1 || page.Campaigns[0].ID != otherID {
		t.Errorf("search found %d, want the other tenant's campaign", page.Total)
	}
	h.operatorGet(ops, "/v1/operator/campaigns?q="+acme.Email, &page)
	if page.Total != 1 || page.Campaigns[0].ID != campaignID {
		t.Errorf("searching the creator's email found %d, want their campaign", page.Total)
	}

	// Every message: to whom, what happened, and who is answerable for it.
	var messages struct {
		Messages []opMessage `json:"messages"`
		Total    int         `json:"total"`
	}
	h.operatorGet(ops, "/v1/operator/messages?campaignId="+campaignID, &messages)
	outcomes := map[string]string{}
	for _, m := range messages.Messages {
		outcomes[m.To] = m.Status
		if m.Source != "campaign" || m.TenantName == "" {
			t.Errorf("%s: source %q tenant %q", m.To, m.Source, m.TenantName)
		}
		if m.SentBy == nil || m.SentBy.Via != "campaign" || m.SentBy.Email == nil ||
			*m.SentBy.Email != acme.Email {
			t.Errorf("%s: sentBy = %+v, want the campaign's creator", m.To, m.SentBy)
		}
	}
	for to, want := range map[string]string{delivered: "delivered", absent: "failed", refused: "failed"} {
		if outcomes[to] != want {
			t.Errorf("%s = %q, want %q (all: %v)", to, outcomes[to], want, outcomes)
		}
	}

	// Filtering by outcome is how an operator answers "who did not get it".
	h.operatorGet(ops, "/v1/operator/messages?campaignId="+campaignID+"&status=failed", &messages)
	if messages.Total != 2 {
		t.Errorf("status=failed found %d, want 2", messages.Total)
	}
	h.operatorGet(ops, "/v1/operator/messages?tenantId="+acme.TenantID.String()+
		"&recipient=500010", &messages)
	if messages.Total != 1 || messages.Messages[0].To != delivered {
		t.Errorf("recipient suffix search found %d, want the one number", messages.Total)
	}

	var summary struct {
		Totals struct {
			Messages, Delivered, Failed int
			DeliveryRate                *float64 `json:"deliveryRate"`
		} `json:"totals"`
		ByTenant []struct {
			TenantID string `json:"tenantId"`
			Messages int    `json:"messages"`
		} `json:"byTenant"`
		ByChannel []struct {
			Channel  string `json:"channel"`
			Messages int    `json:"messages"`
		} `json:"byChannel"`
	}
	h.operatorGet(ops, "/v1/operator/messages/summary?tenantId="+acme.TenantID.String(), &summary)
	if summary.Totals.Messages != 3 || summary.Totals.Delivered != 1 || summary.Totals.Failed != 2 {
		t.Errorf("summary totals = %+v", summary.Totals)
	}
	if summary.Totals.DeliveryRate == nil || *summary.Totals.DeliveryRate != 33.33 {
		t.Errorf("deliveryRate = %v, want 33.33", summary.Totals.DeliveryRate)
	}
	if len(summary.ByChannel) != 1 || summary.ByChannel[0].Channel != "SMS" {
		t.Errorf("byChannel = %+v", summary.ByChannel)
	}

	// The CSV is the same rows as the screen.
	export := h.do(http.MethodGet, "/v1/operator/messages/export?campaignId="+campaignID, ops, nil)
	if export.Code != http.StatusOK {
		t.Fatalf("export = %d\n%s", export.Code, export.Body)
	}
	rows, err := csv.NewReader(strings.NewReader(string(export.Body))).ReadAll()
	if err != nil {
		t.Fatalf("export is not CSV: %v", err)
	}
	if len(rows) != 4 || rows[0][0] != "createdAt" {
		t.Errorf("export has %d rows (want header + 3)", len(rows))
	}

	// A tenant's own session is not an operator's.
	if res := h.do(http.MethodGet, "/v1/operator/campaigns", acme.Token, nil); res.Code != http.StatusUnauthorized {
		t.Errorf("tenant token on the operator view = %d, want 401", res.Code)
	}
}

// A message no campaign or journey owns is attributed to the person or the
// key that sent it — and a read receipt is recorded without moving money.
func TestOperatorSeesWhoSentADirectMessageAndWhetherItWasRead(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)
	secret := h.apiKey(tenant, []string{"send:sms"})
	ops := h.operatorToken()

	byUser, byKey := "9877600010", "9877600020"
	for to, token := range map[string]string{byUser: tenant.Token, byKey: secret} {
		res := h.do(http.MethodPost, "/v1/messages", token, map[string]any{
			"senderId": sender, "templateId": template, "to": to, "body": "Your order shipped."})
		if res.Code != http.StatusAccepted {
			t.Fatalf("send to %s = %d\n%s", to, res.Code, res.Body)
		}
	}
	h.drainSandbox()

	var page struct {
		Messages []opMessage `json:"messages"`
		Total    int         `json:"total"`
	}
	h.operatorGet(ops, "/v1/operator/messages?source=api&tenantId="+tenant.TenantID.String(), &page)
	if page.Total != 2 {
		t.Fatalf("direct sends = %d, want 2", page.Total)
	}
	var userMessage string
	for _, m := range page.Messages {
		if m.SentBy == nil || m.SentBy.Via != "direct" {
			t.Errorf("%s: sentBy = %+v", m.To, m.SentBy)
			continue
		}
		switch {
		case strings.HasSuffix(m.To, byUser):
			userMessage = m.ID
			if m.SentBy.Kind != "user" || m.SentBy.Email == nil || *m.SentBy.Email != tenant.Email {
				t.Errorf("dashboard send attributed to %+v, want %s", m.SentBy, tenant.Email)
			}
		case strings.HasSuffix(m.To, byKey):
			if m.SentBy.Kind != "api_key" || m.SentBy.Name == nil || *m.SentBy.Name != "test key" ||
				m.SentBy.KeyPrefix == nil {
				t.Errorf("API send attributed to %+v, want the key by name and prefix", m.SentBy)
			}
		}
		if m.Status != "delivered" || m.DeliveredAt == nil {
			t.Errorf("%s: status %q deliveredAt %v, want delivered with a time", m.To, m.Status, m.DeliveredAt)
		}
	}

	// The handset reports it read the message.
	conn, err := h.server.ClickHouse.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	service := &sending.Service{DB: h.server.DB, ClickHouse: conn, Connector: h.server.Connector}
	readAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	identity := store.Identity{TenantID: tenant.TenantID}
	balanceBefore := h.walletBalance(tenant)
	report := connector.DeliveryReport{MessageID: userMessage, Delivered: true, Read: true,
		OccurredAt: readAt}
	if err := service.ApplyDeliveryReport(context.Background(), identity, report); err != nil {
		t.Fatalf("apply read: %v", err)
	}
	// Carriers replay; a second READ must not move the time.
	report.OccurredAt = readAt.Add(time.Hour)
	if err := service.ApplyDeliveryReport(context.Background(), identity, report); err != nil {
		t.Fatalf("replay read: %v", err)
	}

	var detail opMessage
	h.operatorGet(ops, "/v1/operator/messages/"+userMessage, &detail)
	if detail.Status != "read" || detail.State != "delivered" {
		t.Errorf("status %q state %q, want read / delivered", detail.Status, detail.State)
	}
	if detail.ReadAt == nil || mustParseTime(t, *detail.ReadAt).UnixMilli() != readAt.UnixMilli() {
		t.Errorf("readAt = %v, want %s — and a replay must not move it", detail.ReadAt, readAt)
	}
	if detail.SentBy == nil || detail.SentBy.Kind != "user" {
		t.Errorf("the read receipt erased who sent it: %+v", detail.SentBy)
	}
	reads := 0
	for _, event := range detail.Events {
		if event.Detail == "read" {
			reads++
		}
	}
	if len(detail.Events) < 3 || reads != 1 {
		t.Errorf("timeline = %+v, want queued → sent → delivered and one read", detail.Events)
	}
	if after := h.walletBalance(tenant); after != balanceBefore {
		t.Errorf("wallet moved %d → %d on a read receipt", balanceBefore, after)
	}

	h.operatorGet(ops, "/v1/operator/messages?status=read&tenantId="+tenant.TenantID.String(), &page)
	if page.Total != 1 || page.Messages[0].ID != userMessage {
		t.Errorf("status=read found %d, want the one read message", page.Total)
	}
}

// A journey names who built it and who switched it on, and its messages are
// attributed to whoever switched it on.
func TestOperatorSeesWhoCreatedAndActivatedAJourney(t *testing.T) {
	t.Parallel()
	r := newJourneyRig(t)
	ops := r.h.operatorToken()
	r.h.seedContacts(r.acct, r.listID, []string{"+919877700010"}, "opted_in")
	id := r.start(map[string]any{"type": "list_entry", "listId": r.listID}, r.send("s1"))
	r.run()

	var page struct {
		Journeys []struct {
			ID          string    `json:"id"`
			Channels    []string  `json:"channels"`
			Enrolled    int       `json:"enrolled"`
			CreatedBy   *opAuthor `json:"createdBy"`
			ActivatedBy *opAuthor `json:"activatedBy"`
			Messages    opCounts  `json:"messages"`
		} `json:"journeys"`
		Total int `json:"total"`
	}
	r.h.operatorGet(ops, "/v1/operator/journeys?tenantId="+r.acct.TenantID.String(), &page)
	if page.Total != 1 {
		t.Fatalf("journeys = %d, want 1", page.Total)
	}
	got := page.Journeys[0]
	if got.ID != id || len(got.Channels) != 1 || got.Channels[0] != "SMS" || got.Enrolled != 1 {
		t.Errorf("journey = %+v", got)
	}
	for label, author := range map[string]*opAuthor{"createdBy": got.CreatedBy, "activatedBy": got.ActivatedBy} {
		if author == nil || author.Email == nil || *author.Email != r.acct.Email {
			t.Errorf("%s = %+v, want %s", label, author, r.acct.Email)
		}
	}
	if got.Messages.Total != 1 {
		t.Errorf("journey messages = %+v, want 1", got.Messages)
	}

	var messages struct {
		Messages []opMessage `json:"messages"`
	}
	r.h.operatorGet(ops, "/v1/operator/messages?journeyId="+id, &messages)
	if len(messages.Messages) != 1 || messages.Messages[0].SentBy == nil ||
		messages.Messages[0].SentBy.Via != "journey" || messages.Messages[0].Source != "journey" {
		t.Errorf("journey message = %+v", messages.Messages)
	}
}

// A filter the endpoint cannot apply is refused, never ignored.
func TestOperatorSendFiltersRefuseWhatTheyCannotApply(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	ops := h.operatorToken()
	for _, path := range []string{
		"/v1/operator/messages?status=bounced",
		"/v1/operator/messages?tenantId=acme",
		"/v1/operator/messages?source=carrier",
		"/v1/operator/messages?from=2026-01-01&to=2026-06-01",
		"/v1/operator/messages?from=2026-09-10&to=2026-09-01",
		"/v1/operator/messages?from=yesterday",
		"/v1/operator/messages?limit=500",
		"/v1/operator/campaigns?status=done",
		"/v1/operator/campaigns?channel=FAX",
		"/v1/operator/journeys?page=0",
	} {
		if res := h.do(http.MethodGet, path, ops, nil); res.Code != http.StatusUnprocessableEntity {
			t.Errorf("GET %s = %d, want 422\n%s", path, res.Code, res.Body)
		}
	}
	if res := h.do(http.MethodGet, "/v1/operator/messages/"+uuid.NewString(), ops, nil); res.Code != http.StatusNotFound {
		t.Errorf("unknown message = %d, want 404", res.Code)
	}
	if res := h.do(http.MethodGet, "/v1/operator/messages", "", nil); res.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", res.Code)
	}
}
