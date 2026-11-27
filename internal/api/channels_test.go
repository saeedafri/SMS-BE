package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

type capabilityRow struct {
	Channel          string  `json:"channel"`
	Mode             string  `json:"mode"`
	Reason           *string `json:"reason"`
	EnabledForTenant *bool   `json:"enabledForTenant"`
}

func (h *harness) capabilities(token string) map[string]capabilityRow {
	h.t.Helper()
	res := h.do(http.MethodGet, "/v1/capabilities", token, nil)
	if res.Code != http.StatusOK {
		h.t.Fatalf("capabilities = %d\n%s", res.Code, res.Body)
	}
	var body struct {
		Channels []capabilityRow `json:"channels"`
	}
	res.decode(h.t, &body)
	rows := map[string]capabilityRow{}
	order := []string{}
	for _, row := range body.Channels {
		rows[row.Channel] = row
		order = append(order, row.Channel)
	}
	if strings.Join(order, ",") != "SMS,RCS,WHATSAPP,EMAIL,VOICE" {
		h.t.Fatalf("channels = %v, want every channel once in order", order)
	}
	return rows
}

func (h *harness) setTenantChannel(operator string, tenant uuid.UUID, channel string, body any) response {
	h.t.Helper()
	return h.do(http.MethodPatch, "/v1/operator/tenants/"+tenant.String()+"/channels/"+channel,
		operator, body)
}

func TestCapabilitiesSayWhatEachChannelDoesForThisTenant(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tenant := h.newAccount("owner")
	rows := h.capabilities(tenant.Token)

	sms := rows["SMS"]
	if sms.Mode != "live" || sms.EnabledForTenant == nil || !*sms.EnabledForTenant || sms.Reason != nil {
		t.Errorf("SMS = %+v, want live, enabled, no reason", sms)
	}
	whatsapp := rows["WHATSAPP"]
	if whatsapp.Mode != "sandbox" || whatsapp.Reason == nil || *whatsapp.Reason == "" ||
		whatsapp.EnabledForTenant == nil || *whatsapp.EnabledForTenant {
		t.Errorf("WHATSAPP = %+v, want sandbox, not enabled, with a reason", whatsapp)
	}
	if rows["VOICE"].Mode != "disabled" {
		t.Errorf("VOICE mode = %s, want disabled", rows["VOICE"].Mode)
	}
}

// Batch 1 row 7: the tenant is looked up before the body is judged.
func TestAnUnknownTenantIs404WhateverTheChannelBodySays(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	operator := h.operatorToken()
	tenant := h.newAccount("owner")
	bad := []any{map[string]any{"enabledForTenant": "yes"}, map[string]any{"bogus": true},
		map[string]any{}, "not an object"}

	for _, body := range bad {
		if res := h.setTenantChannel(operator, uuid.New(), "SMS", body); res.Code != http.StatusNotFound {
			t.Errorf("unknown tenant, body %v = %d, want 404\n%s", body, res.Code, res.Body)
		}
		if res := h.setTenantChannel(operator, tenant.TenantID, "SMS", body); res.Code != http.StatusUnprocessableEntity {
			t.Errorf("known tenant, body %v = %d, want 422\n%s", body, res.Code, res.Body)
		}
	}
	if res := h.setTenantChannel(operator, tenant.TenantID, "FAX",
		map[string]any{"enabledForTenant": true}); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown channel = %d, want 422", res.Code)
	}
	if rows := h.capabilities(tenant.Token); !*rows["SMS"].EnabledForTenant {
		t.Error("a refused request changed the tenant's SMS")
	}
}

// A channel switched off for a tenant refuses every send: campaign create,
// test send and the API, with channel_unavailable, before anything is held.
// Switching it back on restores sending.
func TestASwitchedOffChannelRefusesEverySend(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)
	secret := h.apiKey(tenant, []string{"send:sms"})
	operator := h.operatorToken()

	off := h.setTenantChannel(operator, tenant.TenantID, "SMS", map[string]any{"enabledForTenant": false})
	if off.Code != http.StatusOK {
		t.Fatalf("switch off = %d\n%s", off.Code, off.Body)
	}
	var capability capabilityRow
	off.decode(t, &capability)
	if capability.EnabledForTenant == nil || *capability.EnabledForTenant || capability.Reason == nil {
		t.Errorf("switched-off capability = %+v, want not enabled with a reason", capability)
	}
	before := h.walletBalance(tenant)

	campaign := h.do(http.MethodPost, "/v1/campaigns", tenant.Token, map[string]any{
		"name": "Off", "channel": "SMS", "country": "IN", "senderId": sender, "templateId": template})
	if campaign.Code != http.StatusUnprocessableEntity || campaign.errorCode(t) != "channel_unavailable" {
		t.Errorf("campaign create = %d %s, want 422 channel_unavailable", campaign.Code, campaign.Body)
	}

	var sent struct {
		Status    string  `json:"status"`
		CostMinor int64   `json:"costMinor"`
		ErrorCode *string `json:"errorCode"`
	}
	res := h.do(http.MethodPost, "/v1/messages", secret, map[string]any{
		"senderId": sender, "templateId": template, "to": "9876543210", "body": "Hello"})
	res.decode(t, &sent)
	if sent.Status != "rejected" || sent.ErrorCode == nil || *sent.ErrorCode != "channel_unavailable" ||
		sent.CostMinor != 0 {
		t.Errorf("API send = %+v, want rejected channel_unavailable at cost 0", sent)
	}

	test := h.do(http.MethodPost, "/v1/messages/test", tenant.Token, map[string]any{
		"senderId": sender, "templateId": template, "body": "Hello", "recipients": []string{"9876543210"}})
	if !strings.Contains(string(test.Body), `"failureCode":"channel_unavailable"`) {
		t.Errorf("test send = %d %s, want channel_unavailable", test.Code, test.Body)
	}
	if after := h.walletBalance(tenant); after != before {
		t.Errorf("balance moved from %d to %d on refused sends", before, after)
	}

	if on := h.setTenantChannel(operator, tenant.TenantID, "SMS",
		map[string]any{"enabledForTenant": true}); on.Code != http.StatusOK {
		t.Fatalf("switch on = %d", on.Code)
	}
	sent.ErrorCode = nil
	h.do(http.MethodPost, "/v1/messages", secret, map[string]any{
		"senderId": sender, "templateId": template, "to": "9876543211", "body": "Hello"}).decode(t, &sent)
	if sent.Status == "rejected" {
		t.Errorf("after switching back on, send = %+v, want it to go out", sent)
	}

	var audited int
	if err := h.admin.QueryRow(context.Background(), `SELECT count(*) FROM operator_audit_log
		WHERE tenant_id = $1 AND action IN ('tenant.channel_disable', 'tenant.channel_enable')`,
		tenant.TenantID).Scan(&audited); err != nil || audited != 2 {
		t.Errorf("audit rows = %d (%v), want 2", audited, err)
	}
}

// Batch 1 row 3: a simulated message never costs anything. A test key on a
// sandbox channel is accepted, simulated, free, and labelled so; a live key on
// the same channel is refused.
func TestATestKeySendOnASandboxChannelIsSimulatedAndFree(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	// A number whose WhatsApp setup is finished: an unfinished one is refused
	// onboarding_incomplete before the channel's mode is ever consulted.
	sender, _ := h.whatsAppSender(tenant, true)
	template := h.wildcardTemplate(tenant, sender)
	h.fundWallet(tenant)
	before := h.walletBalance(tenant)
	testKey := h.apiKey(tenant, []string{"send:whatsapp"})

	var sent struct {
		ID        *string `json:"id"`
		Status    string  `json:"status"`
		CostMinor int64   `json:"costMinor"`
		ErrorCode *string `json:"errorCode"`
	}
	res := h.do(http.MethodPost, "/v1/messages", testKey, map[string]any{
		"senderId": sender, "templateId": template, "to": "9876543210", "body": "Hello"})
	res.decode(t, &sent)
	if sent.Status == "rejected" || sent.CostMinor != 0 || sent.ID == nil {
		t.Fatalf("test-key send on sandbox = %d %s, want accepted at cost 0", res.Code, res.Body)
	}
	var logged struct {
		Simulated *bool `json:"simulated"`
		CostMinor int64 `json:"costMinor"`
	}
	h.do(http.MethodGet, "/v1/messages/"+*sent.ID, tenant.Token, nil).decode(t, &logged)
	if logged.Simulated == nil || !*logged.Simulated || logged.CostMinor != 0 {
		t.Errorf("logged = simulated %v cost %d, want simulated at 0", logged.Simulated, logged.CostMinor)
	}
	if after := h.walletBalance(tenant); after != before {
		t.Errorf("balance moved from %d to %d for a simulated message", before, after)
	}

	liveKey, err := store.CreateAPIKey(context.Background(), h.pool,
		store.Identity{TenantID: tenant.TenantID}, "live key", "live", []string{"send:whatsapp"})
	if err != nil {
		t.Fatal(err)
	}
	sent.ErrorCode = nil
	h.do(http.MethodPost, "/v1/messages", liveKey.Secret, map[string]any{
		"senderId": sender, "templateId": template, "to": "9876543211", "body": "Hello"}).decode(t, &sent)
	if sent.Status != "rejected" || sent.ErrorCode == nil || *sent.ErrorCode != "channel_unavailable" {
		t.Errorf("live-key send on sandbox = %+v, want rejected channel_unavailable", sent)
	}
}

// Batch 1 row 8: configuration is listed by name and presence, never a value.
func TestOperatorChannelsListKeysByNameOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.server.ConfigKeys = map[string]bool{"META_APP_SECRET": true}
	res := h.do(http.MethodGet, "/v1/operator/channels", h.operatorToken(), nil)
	if res.Code != http.StatusOK {
		t.Fatalf("operator channels = %d\n%s", res.Code, res.Body)
	}
	var body struct {
		Channels []struct {
			Channel    string           `json:"channel"`
			ConfigKeys []map[string]any `json:"configKeys"`
		} `json:"channels"`
	}
	res.decode(t, &body)
	found := false
	for _, channel := range body.Channels {
		for _, key := range channel.ConfigKeys {
			if len(key) != 2 || key["name"] == nil || key["present"] == nil {
				t.Errorf("%s config key %v carries more than a name and a flag", channel.Channel, key)
			}
			if key["name"] == "META_APP_SECRET" && key["present"] == true {
				found = true
			}
		}
	}
	if !found {
		t.Error("a present key was not reported present")
	}
}

// The check stops at the first step that cannot pass, reports every later
// step skipped, and the operator view shows the result.
func TestGoLiveCheckStopsAtTheFirstStepThatCannotPass(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	operator := h.operatorToken()
	res := h.do(http.MethodPost, "/v1/operator/channels/SMS/go-live-check", operator, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("go-live check = %d\n%s", res.Code, res.Body)
	}
	var result struct {
		Passed bool `json:"passed"`
		Steps  []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"steps"`
	}
	res.decode(t, &result)
	got := []string{}
	for _, step := range result.Steps {
		got = append(got, step.Step+":"+step.Status)
	}
	want := "bind_present:fail,test_message_accepted:skipped,delivery_receipt_received:skipped," +
		"failure_classified:skipped"
	if result.Passed || strings.Join(got, ",") != want {
		t.Errorf("steps = %v (passed %v), want %s", got, result.Passed, want)
	}
	if res := h.do(http.MethodPost, "/v1/operator/channels/FAX/go-live-check", operator, nil); res.Code != http.StatusNotFound {
		t.Errorf("unknown channel = %d, want 404", res.Code)
	}
	if !strings.Contains(string(h.do(http.MethodGet, "/v1/operator/channels", operator, nil).Body),
		`"lastCheck":{"at":`) {
		t.Error("the operator view does not show the check that just ran")
	}
}

func TestTenantDetailCarriesItsChannels(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tenant := h.newAccount("owner")
	var detail struct {
		Channels []capabilityRow `json:"channels"`
	}
	h.do(http.MethodGet, "/v1/operator/tenants/"+tenant.TenantID.String(), h.operatorToken(), nil).
		decode(t, &detail)
	if len(detail.Channels) != 5 {
		t.Fatalf("tenant detail channels = %d, want 5", len(detail.Channels))
	}
}
