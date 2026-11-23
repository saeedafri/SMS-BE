package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"
)

func (h *harness) emailSender(tenant account, domain string) gen.SenderId {
	h.t.Helper()
	res := h.do(http.MethodPost, "/v1/sender-ids", tenant.Token, map[string]any{
		"header": "Acme", "channel": "EMAIL", "country": "IN", "emailDomain": domain,
		"fromAddress": "hello@" + domain, "fromName": "Acme"})
	if res.Code != http.StatusCreated {
		h.t.Fatalf("email sender = %d %s", res.Code, res.Body)
	}
	var sender gen.SenderId
	res.decode(h.t, &sender)
	return sender
}

// The records a domain publishes, in order: three DKIM CNAMEs, the MAIL FROM
// MX and SPF, and a DMARC that is never required.
func TestAnEmailSenderPublishesTheMailFromPair(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tenant := h.newAccount("owner")
	sender := h.emailSender(tenant, "acme-"+uuid.NewString()[:6]+".example")
	var kinds []string
	for _, record := range *sender.DnsRecords {
		kind := string(record.Type)
		if record.Type == "MAIL_FROM_MX" && (record.Priority == nil || *record.Priority != 10 ||
			!strings.HasPrefix(record.Value, "feedback-smtp.ap-south-1.")) {
			t.Errorf("MX = %+v", record)
		}
		if record.Type == "DMARC" && (record.Required == nil || *record.Required) {
			t.Errorf("DMARC required = %v, want false", record.Required)
		}
		kinds = append(kinds, kind)
	}
	if strings.Join(kinds, ",") != "DKIM,DKIM,DKIM,MAIL_FROM_MX,MAIL_FROM_SPF,DMARC" {
		t.Errorf("records = %v", kinds)
	}
	if sender.EmailIdentity == nil || sender.EmailIdentity.Status != "pending" {
		t.Errorf("identity = %+v, want pending", sender.EmailIdentity)
	}
}

// Batch 2 rows 6 and 8: verify-dns answers 401, then 404, then 409, never
// creates a record, and every required record verified is enough.
func TestVerifyDNSJudgesInOrderAndDMARCNeverBlocks(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fake := connector.NewFakeEmail()
	h.server.Email, h.server.Domains = fake, &fakeDomains{txt: map[string][]string{}}
	tenant := h.newAccount("owner")
	domain := "acme-" + uuid.NewString()[:6] + ".example"
	sender := h.emailSender(tenant, domain)
	sms := h.approvedSender(tenant)

	if res := h.do(http.MethodPost, "/v1/sender-ids/"+uuid.NewString()+"/verify-dns", "", nil); res.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", res.Code)
	}
	if res := h.do(http.MethodPost, "/v1/sender-ids/"+uuid.NewString()+"/verify-dns", tenant.Token, nil); res.Code != http.StatusNotFound {
		t.Errorf("unknown sender = %d, want 404", res.Code)
	}
	if res := h.do(http.MethodPost, "/v1/sender-ids/"+sms+"/verify-dns", tenant.Token, nil); res.Code != http.StatusConflict {
		t.Errorf("SMS sender = %d, want 409", res.Code)
	}

	fake.SetIdentity(domain, connector.EmailIdentityState{DKIM: "verified", MailFrom: "verified"})
	res := h.do(http.MethodPost, "/v1/sender-ids/"+sender.Id.String()+"/verify-dns", tenant.Token, nil)
	var checked gen.SenderId
	res.decode(t, &checked)
	if res.Code != http.StatusOK || checked.EmailIdentity == nil || checked.EmailIdentity.Status != "verified" ||
		checked.EmailIdentity.LastCheckedAt == nil {
		t.Errorf("after verify = %d %s, want verified with DMARC still pending", res.Code, res.Body)
	}
	if len(*checked.DnsRecords) != len(*sender.DnsRecords) {
		t.Errorf("verify-dns changed the record count from %d to %d", len(*sender.DnsRecords), len(*checked.DnsRecords))
	}
}

// Batch 2 row 9: nothing sent means no rates, stated as null, not zero.
func TestSendingHealthIsNullNotZeroWithNothingSent(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	if res := h.do(http.MethodGet, "/v1/email/sending-health", tenant.Token, nil); res.Code != http.StatusNotFound {
		t.Errorf("no email sender = %d, want 404", res.Code)
	}
	h.emailSender(tenant, "acme-"+uuid.NewString()[:6]+".example")
	res := h.do(http.MethodGet, "/v1/email/sending-health", tenant.Token, nil)
	body := string(res.Body)
	if res.Code != http.StatusOK || !strings.Contains(body, `"bounceRate":null`) ||
		!strings.Contains(body, `"complaintRate":null`) || !strings.Contains(body, `"windowDays":7`) {
		t.Errorf("health = %d %s", res.Code, body)
	}
}

// Batch 2 row 10, and the operator's pause and resume.
func TestAPausedTenantCannotLaunchEmailButCanLaunchSMS(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	h.server.ChannelModes = func(string) gen.CapabilityMode { return gen.CapabilityModeLive }
	operator := h.operatorToken()
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	h.setTenantChannel(operator, tenant.TenantID, "EMAIL", map[string]any{"enabledForTenant": true})
	path := "/v1/operator/tenants/" + tenant.TenantID.String() + "/email"

	if res := h.do(http.MethodPost, path+"/resume", operator, nil); res.Code != http.StatusConflict {
		t.Errorf("resume while enabled = %d, want 409", res.Code)
	}
	if res := h.do(http.MethodPost, path+"/pause", operator, nil); res.Code != http.StatusOK ||
		!strings.Contains(string(res.Body), `"sendingStatus":"paused"`) {
		t.Fatalf("pause = %d %s", res.Code, res.Body)
	}
	if res := h.do(http.MethodPost, path+"/pause", operator, nil); res.Code != http.StatusConflict {
		t.Errorf("pause twice = %d, want 409", res.Code)
	}
	if res := h.do(http.MethodPost, "/v1/operator/tenants/"+uuid.NewString()+"/email/pause", operator, nil); res.Code != http.StatusNotFound {
		t.Errorf("unknown tenant = %d, want 404", res.Code)
	}

	email := h.emailSender(tenant, "acme-"+uuid.NewString()[:6]+".example")
	res := h.do(http.MethodPost, "/v1/campaigns", tenant.Token, map[string]any{"name": "Mail",
		"channel": "EMAIL", "country": "IN", "senderId": email.Id.String(), "templateId": uuid.NewString(),
		"emailTracking": map[string]any{"opens": false, "clicks": false}})
	if res.Code != http.StatusUnprocessableEntity || res.errorCode(t) != "sending_paused" {
		t.Errorf("email campaign while paused = %d %s, want 422 sending_paused", res.Code, res.Body)
	}
	sender, template := h.linkTemplate(tenant, "Your order has shipped")
	if res := h.campaignOn(tenant, sender, template, nil); res.Code != http.StatusCreated {
		t.Errorf("SMS campaign while email is paused = %d %s, want 201", res.Code, res.Body)
	}
	if res := h.do(http.MethodPost, path+"/resume", operator, nil); res.Code != http.StatusOK ||
		!strings.Contains(string(res.Body), `"sendingStatus":"reinstated"`) {
		t.Errorf("resume = %d %s", res.Code, res.Body)
	}
}

// Batch 2 row 12: tracking is two booleans or absent.
func TestEmailTrackingIsTwoBooleans(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tenant := h.newAccount("owner")
	for _, tracking := range []any{map[string]any{"opens": true}, map[string]any{"opens": "yes", "clicks": false},
		"on"} {
		res := h.do(http.MethodPost, "/v1/campaigns", tenant.Token, map[string]any{"name": "Mail",
			"channel": "EMAIL", "country": "IN", "senderId": uuid.NewString(), "templateId": uuid.NewString(),
			"emailTracking": tracking})
		if res.Code != http.StatusUnprocessableEntity {
			t.Errorf("tracking %v = %d, want 422", tracking, res.Code)
		}
	}
}

// Batch 2 row 7: one identical 404 for an unknown and an expired token, never
// the full address, and POST twice answers the same twice.
func TestThePublicUnsubscribe(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tenant := h.newAccount("owner")
	ctx := context.Background()
	address := "asha-" + uuid.NewString()[:6] + "@example.com"
	if _, err := h.admin.Exec(ctx, `INSERT INTO contacts (id, tenant_id, msisdn, country, fields, consent, email)
		VALUES ($1, $2, '+919800000001', 'IN', '{}', '{"EMAIL":"opted_in"}', $3)`,
		uuid.New(), tenant.TenantID, address); err != nil {
		t.Fatal(err)
	}
	tokens, err := store.UnsubscribeTokens(ctx, h.pool, store.Identity{TenantID: tenant.TenantID},
		[]string{address}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	token := tokens[address]
	if _, err := h.admin.Exec(ctx, `INSERT INTO email_unsubscribe_tokens (token, tenant_id, email, expires_at)
		VALUES ('expired-`+token[:8]+`', $1, $2, now() - interval '1 day')`, tenant.TenantID, address); err != nil {
		t.Fatal(err)
	}

	unknown := h.do(http.MethodGet, "/v1/public/unsubscribe/nope-"+token[:8], "", nil)
	expired := h.do(http.MethodGet, "/v1/public/unsubscribe/expired-"+token[:8], "", nil)
	if unknown.Code != http.StatusNotFound || string(unknown.Body) != string(expired.Body) {
		t.Errorf("unknown %d %s vs expired %d %s, want one identical 404", unknown.Code, unknown.Body,
			expired.Code, expired.Body)
	}
	got := h.do(http.MethodGet, "/v1/public/unsubscribe/"+token, "", nil)
	if got.Code != http.StatusOK || strings.Contains(string(got.Body), address) ||
		!strings.Contains(string(got.Body), `"maskedEmail":"a***@example.com"`) {
		t.Errorf("info = %d %s", got.Code, got.Body)
	}
	first := h.do(http.MethodPost, "/v1/public/unsubscribe/"+token, "", nil)
	second := h.do(http.MethodPost, "/v1/public/unsubscribe/"+token, "", nil)
	if first.Code != http.StatusOK || string(first.Body) != string(second.Body) ||
		!strings.Contains(string(first.Body), `"alreadyUnsubscribed":true`) {
		t.Errorf("POST twice = %s / %s", first.Body, second.Body)
	}
}

// Batch 2 rows 1, 3, 4, 5, 11 and 2, end to end through the fake provider:
// marketing mail carries the one-click header and transactional does not; a
// message is charged only on delivery; a permanent bounce suppresses the
// address for this tenant only; the bounced webhook carries the log's failure
// and no address; a redelivered event changes nothing.
func TestAnEmailCampaignThroughTheProvider(t *testing.T) {
	h := newSendHarness(t)
	fake := connector.NewFakeEmail()
	h.server.Email = fake
	h.server.ChannelModes = func(string) gen.CapabilityMode { return gen.CapabilityModeLive }
	h.server.Carriers = connector.Registry{ByChannel: map[string]connector.Connector{
		"EMAIL": sending.EmailConnector{Provider: fake}}}
	h.server.LinkBaseURL, h.server.AppBaseURL = "https://api.relay.test", "https://app.relay.test"
	ctx := context.Background()
	endpoint := startCustomerEndpoint(t)
	tenant, other := h.newAccount("owner"), h.newAccount("owner")
	h.subscribeEndpoint(tenant, endpoint, "message.bounced")
	h.fundWallet(tenant)
	h.registerEntity(tenant.TenantID, "IN")
	h.setTenantChannel(h.operatorToken(), tenant.TenantID, "EMAIL", map[string]any{"enabledForTenant": true})
	if _, err := h.admin.Exec(ctx, `INSERT INTO pricing_rates (country, channel, category, per_segment_minor, currency)
		VALUES ('IN', 'EMAIL', '', 10, 'INR') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}

	domain := "acme-" + uuid.NewString()[:6] + ".example"
	sender := h.emailSender(tenant, domain)
	if _, err := h.admin.Exec(ctx, `UPDATE sender_ids SET status = 'approved' WHERE id = $1`, sender.Id); err != nil {
		t.Fatal(err)
	}
	marketing := h.emailTemplate(tenant, sender.Id, "MARKETING")
	list := h.emailList(tenant, "bounce@simulator.amazonses.com", "success@simulator.amazonses.com")
	launch := func(template string) response {
		return h.do(http.MethodPost, "/v1/campaigns", tenant.Token, map[string]any{"name": "Mail",
			"channel": "EMAIL", "country": "IN", "senderId": sender.Id.String(), "templateId": template,
			"listId": list, "emailTracking": map[string]any{"opens": false, "clicks": false}})
	}

	// Row 1: an unverified domain sends nothing.
	launch(marketing)
	if len(fake.Sent) != 0 {
		t.Fatalf("row 1: %d emails left for an unverified domain", len(fake.Sent))
	}
	fake.SetIdentity(domain, connector.EmailIdentityState{DKIM: "verified", MailFrom: "verified"})
	h.do(http.MethodPost, "/v1/sender-ids/"+sender.Id.String()+"/verify-dns", tenant.Token, nil)

	if res := launch(marketing); res.Code != http.StatusCreated {
		t.Fatalf("marketing campaign = %d %s", res.Code, res.Body)
	}
	if len(fake.Sent) != 2 {
		t.Fatalf("sent %d, want 2", len(fake.Sent))
	}
	for _, mail := range fake.Sent {
		if mail.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" ||
			!strings.Contains(mail.Headers["List-Unsubscribe"], "/v1/public/unsubscribe/") ||
			!strings.Contains(mail.HTML, "Unsubscribe") {
			t.Errorf("row 3: marketing mail without the one-click header and footer: %+v", mail.Headers)
		}
	}
	transactional := h.emailTemplate(tenant, sender.Id, "TRANSACTIONAL")
	launch(transactional)
	if last := fake.Sent[len(fake.Sent)-1]; last.Headers["List-Unsubscribe"] != "" {
		t.Errorf("row 3: transactional mail carries %q", last.Headers["List-Unsubscribe"])
	}

	events := fake.DrainEvents()
	conn, err := h.server.ClickHouse.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	service := &sending.Service{DB: h.server.DB, ClickHouse: conn, Connector: h.server.Connector,
		Settled: h.server.MessageSettled, EmailEvents: h.server.EmailEventWebhook}
	var bounce connector.EmailEvent
	for _, event := range events {
		if err := service.ApplyEmailEvent(ctx, event); err != nil {
			t.Fatalf("apply %s: %v", event.Type, err)
		}
		if event.Type == "bounce" && bounce.EventID == "" {
			bounce = event
		}
	}
	// Row 2: the same event again changes nothing.
	if err := service.ApplyEmailEvent(ctx, bounce); err != nil {
		t.Fatal(err)
	}

	logged := map[string]loggedMessage{}
	for _, status := range []string{"failed", "delivered"} {
		for _, message := range h.messagesWith(tenant.Token, "channel=EMAIL&status="+status+"&limit=50") {
			logged[status] = message
		}
	}
	var costs struct {
		Messages []struct {
			Status    string `json:"status"`
			CostMinor int64  `json:"costMinor"`
		} `json:"messages"`
	}
	h.do(http.MethodGet, "/v1/messages?channel=EMAIL&limit=50", tenant.Token, nil).decode(t, &costs)
	for _, m := range costs.Messages {
		if (m.Status == "failed" && m.CostMinor != 0) || (m.Status == "delivered" && m.CostMinor <= 0) {
			t.Errorf("row 5: %s message cost %d", m.Status, m.CostMinor)
		}
	}

	if suppressed, _ := store.IsSuppressed(ctx, h.pool, store.Identity{TenantID: tenant.TenantID},
		"bounce@simulator.amazonses.com"); !suppressed {
		t.Error("a permanent bounce did not suppress the address")
	}
	if suppressed, _ := store.IsSuppressed(ctx, h.pool, store.Identity{TenantID: other.TenantID},
		"bounce@simulator.amazonses.com"); suppressed {
		t.Error("row 4: one tenant's bounce suppressed the address for another")
	}

	waitFor(t, "message.bounced", func() bool { return len(endpoint.received("message.bounced")) > 0 })
	time.Sleep(500 * time.Millisecond)
	hooks := endpoint.received("message.bounced")
	if len(hooks) != 1 {
		t.Errorf("row 2: %d bounced webhooks for one bounce, want 1", len(hooks))
	}
	var body struct {
		Data struct {
			MessageID string          `json:"messageId"`
			Failure   json.RawMessage `json:"failure"`
		} `json:"data"`
	}
	_ = json.Unmarshal(hooks[0].Body, &body)
	var logEntry struct {
		Failure json.RawMessage `json:"failure"`
	}
	h.do(http.MethodGet, "/v1/messages/"+body.Data.MessageID, tenant.Token, nil).decode(t, &logEntry)
	var fromHook, fromLog map[string]any
	_ = json.Unmarshal(body.Data.Failure, &fromHook)
	_ = json.Unmarshal(logEntry.Failure, &fromLog)
	a, _ := json.Marshal(fromHook)
	b, _ := json.Marshal(fromLog)
	if string(a) != string(b) || len(fromHook) == 0 || strings.Contains(string(hooks[0].Body), "@") {
		t.Errorf("row 11: webhook failure %s, log failure %s, body %s", a, b, hooks[0].Body)
	}
}

func (h *harness) emailTemplate(tenant account, sender uuid.UUID, category string) string {
	h.t.Helper()
	var id string
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO templates (tenant_id, sender_id, name, channel, country, status, category,
		    email_content, variables)
		VALUES ($1, $2, $3, 'EMAIL', 'IN', 'approved', $4,
		    '{"subject":"Hello","bodyHtml":"<p>Hi {{first_name}}</p>"}', ARRAY['first_name'])
		RETURNING id`, tenant.TenantID, sender, "Mail "+uuid.NewString()[:8], category).Scan(&id); err != nil {
		h.t.Fatalf("seed email template: %v", err)
	}
	return id
}

func (h *harness) emailList(tenant account, addresses ...string) string {
	h.t.Helper()
	ctx := context.Background()
	list := uuid.New()
	if _, err := h.admin.Exec(ctx, `INSERT INTO contact_lists (id, tenant_id, name) VALUES ($1, $2, 'Mail')`,
		list, tenant.TenantID); err != nil {
		h.t.Fatal(err)
	}
	for i, address := range addresses {
		contact := uuid.New()
		if _, err := h.admin.Exec(ctx, `INSERT INTO contacts (id, tenant_id, msisdn, country, fields, consent, email)
			VALUES ($1, $2, $3, 'IN', '{"first_name":"Asha"}', '{"EMAIL":"opted_in"}', $4)`,
			contact, tenant.TenantID, "+91980000010"+string(rune('0'+i)), address); err != nil {
			h.t.Fatal(err)
		}
		if _, err := h.admin.Exec(ctx, `INSERT INTO contact_list_members (list_id, contact_id, tenant_id)
			VALUES ($1, $2, $3)`, list, contact, tenant.TenantID); err != nil {
			h.t.Fatal(err)
		}
	}
	return list.String()
}
