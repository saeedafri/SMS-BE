package api_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

const testMetaSecret = "meta-test-secret"

// whatsAppLive makes WhatsApp live on the platform and on for this tenant.
func (h *harness) whatsAppLive(tenant account) {
	h.t.Helper()
	h.server.ChannelModes = func(string) gen.CapabilityMode { return gen.CapabilityModeLive }
	h.server.MetaAppSecret = testMetaSecret
	res := h.setTenantChannel(h.operatorToken(), tenant.TenantID, "WHATSAPP",
		map[string]any{"enabledForTenant": true})
	if res.Code != http.StatusOK {
		h.t.Fatalf("enable WhatsApp = %d %s", res.Code, res.Body)
	}
}

// whatsAppSender seeds a connected number. A ready one has every setup step
// done; one that is not still has no way to pay Meta.
func (h *harness) whatsAppSender(tenant account, ready bool) (senderID, wabaID string) {
	h.t.Helper()
	wabaID = fmt.Sprint(rand.Int64N(1 << 52))
	payment := "none"
	if ready {
		payment = "card"
	}
	// Sealed with the server's own key: a route that calls Meta decrypts it.
	sealed, err := h.server.Secrets.Encrypt("test-business-token")
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status, waba_id,
		    wa_phone_number_id, wa_token_sealed, wa_token_status, wa_subscribed, wa_registered,
		    wa_payment_method, wa_display_name_status, wa_messaging_limit)
		VALUES ($1, $2, 'WHATSAPP', 'IN', 'approved', $3, $2, $5, 'ok', true, true,
		    $4, 'approved', 'TIER_250') RETURNING id`,
		tenant.TenantID, fmt.Sprint(rand.Int64N(1<<52)), wabaID, payment, sealed).Scan(&senderID); err != nil {
		h.t.Fatalf("seed whatsapp sender: %v", err)
	}
	h.registerEntity(tenant.TenantID, "IN")
	return senderID, wabaID
}

// whatsAppTemplate seeds a template Meta has reviewed to the given status.
func (h *harness) whatsAppTemplate(tenant account, senderID, status string) (templateID, metaID string) {
	h.t.Helper()
	metaID = fmt.Sprint(rand.Int64N(1 << 52))
	definition := fmt.Sprintf(`{"name":"order_ready_%d","language":"en","category":"utility",
		"parameterFormat":"named","components":[{"type":"BODY","text":"Hi {{first_name}}, your order is ready.",
		"examples":[{"name":"first_name","value":"Asha"}]}]}`, h.nextSenderSeq())
	state := fmt.Sprintf(`{"status":%q,"qualityRating":"unknown","category":"utility","metaTemplateId":%q}`,
		status, metaID)
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO templates (tenant_id, sender_id, name, channel, country, status, category,
		    wa_definition, wa_state, variables)
		VALUES ($1, $2, $3, 'WHATSAPP', 'IN', 'pending_review', 'UTILITY', $4, $5, ARRAY['first_name'])
		RETURNING id`, tenant.TenantID, senderID, "Order "+uuid.NewString()[:8], definition, state).
		Scan(&templateID); err != nil {
		h.t.Fatalf("seed whatsapp template: %v", err)
	}
	return templateID, metaID
}

// whatsAppList is a list of opted-in people, numbered from the start.
func (h *harness) whatsAppList(tenant account, people int) string {
	h.t.Helper()
	list := uuid.New()
	ctx := context.Background()
	// Named and numbered by the list's own id, so one tenant can hold several.
	if _, err := h.admin.Exec(ctx, `INSERT INTO contact_lists (id, tenant_id, name) VALUES ($1, $2, $3)`,
		list, tenant.TenantID, "WA "+list.String()); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.admin.Exec(ctx, `
		WITH added AS (
		    INSERT INTO contacts (tenant_id, msisdn, country, fields, consent)
		    SELECT $1, '+9197' || lpad(($4 + g)::text, 8, '0'), 'IN', '{"first_name":"Asha"}', '{"WHATSAPP":"opted_in"}'
		    FROM generate_series(1, $3) g RETURNING id)
		INSERT INTO contact_list_members (list_id, contact_id, tenant_id) SELECT $2, id, $1 FROM added`,
		tenant.TenantID, list, people, h.nextSenderSeq()*100_000); err != nil {
		h.t.Fatal(err)
	}
	return list.String()
}

func (h *harness) whatsAppCampaign(tenant account, senderID, templateID, listID string) response {
	h.t.Helper()
	return h.do(http.MethodPost, "/v1/campaigns", tenant.Token, map[string]any{
		"name": "WA", "channel": "WHATSAPP", "country": "IN",
		"senderId": senderID, "templateId": templateID, "listId": listID})
}

// metaWebhook posts a Meta webhook signed with the app secret.
func (h *harness) metaWebhook(field, entryID string, value map[string]any) response {
	h.t.Helper()
	body, _ := json.Marshal(map[string]any{"object": "whatsapp_business_account",
		"entry": []any{map[string]any{"id": entryID,
			"changes": []any{map[string]any{"field": field, "value": value}}}}})
	mac := hmac.New(sha256.New, []byte(testMetaSecret))
	mac.Write(body)
	return h.doWithHeaders(http.MethodPost, "/v1/webhooks/whatsapp", "", json.RawMessage(body),
		map[string]string{"X-Hub-Signature-256": "sha256=" + hex.EncodeToString(mac.Sum(nil))})
}

// Batch 3 row 1: no send before setup is complete.
func TestAWhatsAppCampaignWaitsForSetup(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	h.whatsAppLive(tenant)
	sender, _ := h.whatsAppSender(tenant, false)
	template, _ := h.whatsAppTemplate(tenant, sender, "approved")
	res := h.whatsAppCampaign(tenant, sender, template, h.whatsAppList(tenant, 2))
	if res.Code != http.StatusUnprocessableEntity || res.errorCode(t) != "onboarding_incomplete" {
		t.Fatalf("campaign on an unfinished number = %d %s, want 422 onboarding_incomplete", res.Code, res.Body)
	}
}

// Batch 3 row 8, and the daily limit: refused whole, before anything is held.
func TestAWhatsAppCampaignNeedsMetasApprovalAndRoomInTheLimit(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	h.whatsAppLive(tenant)
	sender, _ := h.whatsAppSender(tenant, true)
	list := h.whatsAppList(tenant, 251)

	// Our own status reads pending_review in both; Meta's standing decides.
	pending, _ := h.whatsAppTemplate(tenant, sender, "paused")
	if res := h.whatsAppCampaign(tenant, sender, pending, list); res.Code != http.StatusUnprocessableEntity ||
		res.errorCode(t) != "template_not_approved" {
		t.Errorf("paused template = %d %s, want 422 template_not_approved", res.Code, res.Body)
	}
	approved, _ := h.whatsAppTemplate(tenant, sender, "approved")
	res := h.whatsAppCampaign(tenant, sender, approved, list)
	if res.Code != http.StatusUnprocessableEntity || res.errorCode(t) != "messaging_limit_reached" ||
		!strings.Contains(string(res.Body), "250 more people") {
		t.Errorf("251 people on TIER_250 = %d %s, want 422 messaging_limit_reached", res.Code, res.Body)
	}
	var campaigns int
	_ = h.admin.QueryRow(context.Background(), `SELECT count(*) FROM campaigns WHERE tenant_id = $1`,
		tenant.TenantID).Scan(&campaigns)
	if campaigns != 0 {
		t.Errorf("a refused campaign left %d rows", campaigns)
	}
	if res := h.whatsAppCampaign(tenant, sender, approved, h.whatsAppList(tenant, 3)); res.Code != http.StatusCreated {
		t.Errorf("3 people on TIER_250 = %d %s, want 201", res.Code, res.Body)
	}
}

// conversation seeds a WhatsApp thread whose customer last wrote `ago` ago.
func (h *harness) waConversation(tenant account, ago time.Duration) string {
	h.t.Helper()
	ctx := context.Background()
	var contact, conversation string
	if err := h.admin.QueryRow(ctx, `INSERT INTO contacts (tenant_id, msisdn, country)
		VALUES ($1, $2, 'IN') RETURNING id`, tenant.TenantID,
		fmt.Sprintf("+9196%08d", rand.IntN(1e8))).Scan(&contact); err != nil {
		h.t.Fatal(err)
	}
	if err := h.admin.QueryRow(ctx, `INSERT INTO conversations (tenant_id, contact_id, channel)
		VALUES ($1, $2, 'WHATSAPP') RETURNING id`, tenant.TenantID, contact).Scan(&conversation); err != nil {
		h.t.Fatal(err)
	}
	h.waInbound(tenant, conversation, ago)
	return conversation
}

func (h *harness) waInbound(tenant account, conversation string, ago time.Duration) {
	h.t.Helper()
	if _, err := h.admin.Exec(context.Background(), `INSERT INTO conversation_messages
		(tenant_id, conversation_id, direction, body, created_at) VALUES ($1, $2, 'inbound', 'hi', $3)`,
		tenant.TenantID, conversation, time.Now().Add(-ago)); err != nil {
		h.t.Fatal(err)
	}
}

func windowOf(t *testing.T, res response) (string, bool) {
	t.Helper()
	var raw map[string]json.RawMessage
	res.decode(t, &raw)
	value, present := raw["windowExpiresAt"]
	return string(value), present
}

// Batch 3 rows 9 and 10 and the reply rules: one of body or template; a
// template must be WhatsApp-approved with every variable; free text only
// inside the window; and only the customer writing reopens it.
func TestWhatsAppRepliesKeepTheCustomerWindow(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	h.whatsAppLive(tenant)
	conversation := h.waConversation(tenant, 25*time.Hour)
	path := "/v1/conversations/" + conversation
	reply := func(body map[string]any) response {
		return h.do(http.MethodPost, path+"/reply", tenant.Token, body)
	}

	if res := reply(map[string]any{"body": "hello"}); res.errorCode(t) != "onboarding_incomplete" {
		t.Errorf("no ready number = %d %s, want onboarding_incomplete", res.Code, res.Body)
	}
	sender, _ := h.whatsAppSender(tenant, true)
	pending, _ := h.whatsAppTemplate(tenant, sender, "pending")
	approved, _ := h.whatsAppTemplate(tenant, sender, "approved")

	if value, present := windowOf(t, h.do(http.MethodGet, path, tenant.Token, nil)); !present || value != "null" {
		t.Errorf("closed window = %q (present %v), want an explicit null", value, present)
	}
	for name, body := range map[string]map[string]any{
		"neither": {}, "both": {"body": "hi", "templateId": approved},
	} {
		if res := reply(body); res.Code != http.StatusUnprocessableEntity || res.errorCode(t) != "validation_failed" {
			t.Errorf("%s = %d %s, want 422 validation_failed", name, res.Code, res.Body)
		}
	}
	if res := reply(map[string]any{"body": "hello"}); res.errorCode(t) != "outside_customer_window" {
		t.Errorf("free text after 24h = %d %s, want outside_customer_window", res.Code, res.Body)
	}
	if res := reply(map[string]any{"templateId": pending, "variables": map[string]string{"first_name": "A"}}); res.errorCode(t) != "template_not_approved" {
		t.Errorf("pending template = %d %s, want template_not_approved", res.Code, res.Body)
	}
	if res := reply(map[string]any{"templateId": approved}); res.errorCode(t) != "variables_missing" {
		t.Errorf("no variables = %d %s, want variables_missing", res.Code, res.Body)
	}
	res := reply(map[string]any{"templateId": approved, "variables": map[string]string{"first_name": "Asha"}})
	if res.Code != http.StatusOK || !strings.Contains(string(res.Body), "Hi Asha, your order is ready.") {
		t.Fatalf("approved template = %d %s, want 200 with the filled body", res.Code, res.Body)
	}
	if value, _ := windowOf(t, res); value != "null" {
		t.Errorf("a template reply reopened the window: %s", value)
	}

	h.waInbound(tenant, conversation, time.Minute)
	value, _ := windowOf(t, h.do(http.MethodGet, path, tenant.Token, nil))
	expires, err := time.Parse(time.RFC3339Nano, strings.Trim(value, `"`))
	if err != nil || expires.Sub(time.Now().Add(23*time.Hour+59*time.Minute)).Abs() > time.Minute {
		t.Errorf("window after an inbound message = %s, want about 24h on", value)
	}
	if res := reply(map[string]any{"body": "hello"}); res.Code != http.StatusOK {
		t.Errorf("free text inside the window = %d %s, want 200", res.Code, res.Body)
	}
}

// Batch 3 rows 5, 6, 14 and 15: the cap refuses before Meta is called so the
// code survives; a code works once; nothing returns a token or PIN; a number
// belongs to one tenant.
func TestEmbeddedSignupRefusesBeforeSpendingTheCode(t *testing.T) {
	h := newHarness(t)
	h.server.WhatsApp = connector.NewFakeWhatsApp()
	tenant, other := h.newAccount("owner"), h.newAccount("owner")
	// At least one onboarding this week, so the cap below is a real number:
	// a cap of 0 means "use the default of 10", not "nobody".
	if _, err := h.admin.Exec(context.Background(),
		`INSERT INTO wa_onboardings (tenant_id) VALUES ($1)`, other.TenantID); err != nil {
		t.Fatal(err)
	}
	var onboarded int
	_ = h.admin.QueryRow(context.Background(), `SELECT count(*) FROM wa_onboardings
		WHERE onboarded_at > now() - interval '7 days'`).Scan(&onboarded)
	h.server.WhatsAppOnboardingCap = onboarded
	phone := fmt.Sprint(rand.Int64N(1 << 52))
	code := "code-" + uuid.NewString()
	complete := func(who account, code string) response {
		return h.do(http.MethodPost, "/v1/whatsapp/embedded-signup/complete", who.Token, map[string]any{
			"code": code, "event": "FINISH", "wabaId": fmt.Sprint(rand.Int64N(1 << 52)), "phoneNumberId": phone})
	}

	if res := complete(tenant, code); res.Code != http.StatusConflict || res.errorCode(t) != "onboarding_cap_reached" {
		t.Fatalf("at the cap = %d %s, want 409 onboarding_cap_reached", res.Code, res.Body)
	}
	h.server.WhatsAppOnboardingCap = onboarded + 10
	res := complete(tenant, code)
	if res.Code != http.StatusOK {
		t.Fatalf("same code once the cap lifts = %d %s, want 200", res.Code, res.Body)
	}
	for _, secret := range []string{"token-for-", "sealed", "pin"} {
		if strings.Contains(strings.ToLower(string(res.Body)), secret) {
			t.Errorf("signup response carries %q: %s", secret, res.Body)
		}
	}
	var sender gen.SenderId
	res.decode(t, &sender)
	got := h.do(http.MethodGet, "/v1/sender-ids/"+sender.Id.String(), tenant.Token, nil)
	if strings.Contains(string(got.Body), "token-for-") || strings.Contains(string(got.Body), "sealed") {
		t.Errorf("GET sender carries a secret: %s", got.Body)
	}

	if res := complete(tenant, code); res.errorCode(t) != "invalid_code" {
		t.Errorf("replayed code = %d %s, want 422 invalid_code", res.Code, res.Body)
	}
	if res := complete(other, "code-"+uuid.NewString()); res.Code != http.StatusUnprocessableEntity ||
		res.errorCode(t) != "conflict" {
		t.Errorf("the same number for another tenant = %d %s, want 422 conflict", res.Code, res.Body)
	}
	var senders int
	_ = h.admin.QueryRow(context.Background(), `SELECT count(*) FROM sender_ids WHERE wa_phone_number_id = $1`,
		phone).Scan(&senders)
	if senders != 1 {
		t.Errorf("%d senders hold the number, want 1", senders)
	}
}

// Batch 3 row 7: Meta's 133016 leaves the number blocked with no attempts left.
func TestTheLastRegistrationAttemptBlocksTheNumber(t *testing.T) {
	h := newHarness(t)
	fake := connector.NewFakeWhatsApp()
	h.server.WhatsApp, h.server.WhatsAppOnboardingCap = fake, 1<<20
	tenant := h.newAccount("owner")
	phone := fmt.Sprint(rand.Int64N(1 << 52))
	fake.RefuseRegistration(phone)
	res := h.do(http.MethodPost, "/v1/whatsapp/embedded-signup/complete", tenant.Token, map[string]any{
		"code": "code-" + uuid.NewString(), "event": "FINISH",
		"wabaId": fmt.Sprint(rand.Int64N(1 << 52)), "phoneNumberId": phone})
	var sender gen.SenderId
	res.decode(t, &sender)
	if res.Code != http.StatusOK || sender.RegistrationAttemptsLeft == nil || *sender.RegistrationAttemptsLeft != 0 {
		t.Fatalf("after 133016 = %d %s, want 200 with 0 attempts left", res.Code, res.Body)
	}
	blocked := false
	for _, step := range *sender.WaSetup {
		blocked = blocked || (step.Step == "number_registered" && step.Status == gen.SetupStepStatusBlocked)
	}
	if !blocked {
		t.Errorf("number_registered is not blocked: %s", res.Body)
	}
}

// Batch 3 row 4: a disconnected account sends nothing, and the tenant's
// WhatsApp reads sandbox with a reason.
func TestADisconnectedWhatsAppAccountStopsSending(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	h.whatsAppLive(tenant)
	sender, waba := h.whatsAppSender(tenant, true)
	template, _ := h.whatsAppTemplate(tenant, sender, "approved")

	if res := h.doWithHeaders(http.MethodPost, "/v1/webhooks/whatsapp", "", map[string]any{"entry": []any{}},
		map[string]string{"X-Hub-Signature-256": "sha256=00"}); res.Code != http.StatusUnauthorized {
		t.Errorf("bad signature = %d, want 401", res.Code)
	}
	if res := h.metaWebhook("account_update", waba, map[string]any{"event": "ACCOUNT_DISCONNECTED"}); res.Code != http.StatusOK {
		t.Fatalf("account_update = %d %s", res.Code, res.Body)
	}
	if res := h.whatsAppCampaign(tenant, sender, template, h.whatsAppList(tenant, 1)); res.errorCode(t) != "onboarding_incomplete" {
		t.Errorf("campaign after disconnect = %d %s, want onboarding_incomplete", res.Code, res.Body)
	}
	var capabilities gen.CapabilitiesResponse
	h.do(http.MethodGet, "/v1/capabilities", tenant.Token, nil).decode(t, &capabilities)
	for _, channel := range capabilities.Channels {
		if channel.Channel == gen.ChannelIdWHATSAPP && (channel.Mode != gen.CapabilityModeSandbox ||
			channel.Reason == nil || !strings.Contains(*channel.Reason, "disconnected")) {
			t.Errorf("WhatsApp capability = %s %v, want sandbox with a reason", channel.Mode, channel.Reason)
		}
	}
}

// Templates in Meta's model: created pending and unique per language; Meta's
// verdict, flag, lock, category and quality land in waState.
func TestWhatsAppTemplatesFollowMeta(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tenant := h.newAccount("owner")
	h.server.MetaAppSecret = testMetaSecret
	ready, _ := h.whatsAppSender(tenant, true)
	definition := map[string]any{"name": fmt.Sprintf("order_%d", h.nextSenderSeq()), "language": "en",
		"category": "utility", "parameterFormat": "named", "components": []any{map[string]any{
			"type": "BODY", "text": "Hi {{first_name}}",
			"examples": []any{map[string]any{"name": "first_name", "value": "Asha"}}}}}
	create := func(sender string, definition map[string]any) response {
		return h.do(http.MethodPost, "/v1/templates", tenant.Token, map[string]any{
			"name": "Order " + uuid.NewString()[:6], "senderId": sender, "waDefinition": definition})
	}

	res := create(ready, definition)
	var template gen.Template
	res.decode(t, &template)
	if res.Code != http.StatusCreated || template.WaState == nil || template.WaState.Status != "pending" ||
		template.WaState.QualityRating != "unknown" || template.WaState.MetaTemplateId == nil ||
		len(template.Variables) != 1 || template.Variables[0] != "first_name" {
		t.Fatalf("create = %d %s, want 201 pending with Meta's id", res.Code, res.Body)
	}
	if res := create(ready, definition); res.Code != http.StatusConflict {
		t.Errorf("same name and language = %d %s, want 409", res.Code, res.Body)
	}
	bad := map[string]any{"name": "Bad Name", "language": "en", "category": "utility",
		"parameterFormat": "named", "components": []any{}}
	if res := create(ready, bad); res.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(string(res.Body), "lowercase letters") {
		t.Errorf("bad name = %d %s, want 422 on the name first", res.Code, res.Body)
	}

	metaID := *template.WaState.MetaTemplateId
	for _, change := range []struct {
		field string
		value map[string]any
	}{
		{"message_template_status_update", map[string]any{"event": "APPROVED", "message_template_id": json.Number(metaID)}},
		{"message_template_status_update", map[string]any{"event": "FLAGGED", "message_template_id": json.Number(metaID)}},
		{"template_category_update", map[string]any{"message_template_id": json.Number(metaID),
			"previous_category": "UTILITY", "new_category": "MARKETING"}},
		{"message_template_quality_update", map[string]any{"message_template_id": json.Number(metaID),
			"new_quality_score": "YELLOW"}},
	} {
		if res := h.metaWebhook(change.field, "waba", change.value); res.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", change.field, res.Code, res.Body)
		}
	}
	h.do(http.MethodGet, "/v1/templates/"+template.Id.String(), tenant.Token, nil).decode(t, &template)
	state := template.WaState
	if state.Status != "approved" || state.AtRisk == nil || !*state.AtRisk || state.Category != "marketing" ||
		state.CategoryChangedByMeta == nil || !*state.CategoryChangedByMeta || state.QualityRating != "yellow" {
		t.Errorf("state after Meta's events = %+v", *state)
	}
}
