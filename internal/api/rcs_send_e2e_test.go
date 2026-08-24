package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// The whole RCS path in one test each: an approved carrier template, a send
// that reaches the carrier with the right template and variables, and a
// delivery webhook that settles the money. Every layer is real except the
// carrier itself.

func newRCSSendHarness(t *testing.T, carrier *stubRegistrar) *harness {
	t.Helper()
	h := newSendHarness(t)
	h.server.RCSCarrier = carrier
	h.server.Carriers = connector.Registry{
		Default:   h.server.Connector,
		ByChannel: map[string]connector.Connector{"RCS": carrier},
	}
	h.server.CarrierWebhookToken = webhookToken
	h.rebuildRouter()
	return h
}

// approvedRCSTemplate seeds a template the carrier has also approved, which is
// the only state a send is permitted from.
func (h *harness) approvedRCSTemplate(tenant account, name, carrierTemplateID string) (senderID, templateID uuid.UUID, carrierID string) {
	h.t.Helper()
	// Unique per run. The carrier template id is unique across the whole table
	// by design — it is the only key an approval webhook can match on — so a
	// fixed literal collides with its own previous run the moment a tenant
	// fails to be cleaned up, which is exactly what used to happen.
	carrierTemplateID += "-" + uuid.NewString()[:8]
	templateID = h.rcsTemplate(tenant, name, []string{"first_name", "order_id"}, "UTILITY")
	if _, err := h.admin.Exec(context.Background(), `
		UPDATE templates
		   SET carrier_vendor = 'airtel', carrier_template_id = $2,
		       carrier_status = 'approved', carrier_submitted_at = now()
		 WHERE id = $1`, templateID, carrierTemplateID); err != nil {
		h.t.Fatalf("approve carrier template: %v", err)
	}
	if err := h.admin.QueryRow(context.Background(),
		`SELECT sender_id FROM templates WHERE id = $1`, templateID).Scan(&senderID); err != nil {
		h.t.Fatalf("read sender: %v", err)
	}
	return senderID, templateID, carrierTemplateID
}

func TestAnRCSSendReachesTheCarrierWithItsTemplateAndVariables(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel"}
	h := newRCSSendHarness(t, carrier)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	senderID, templateID, carrierID := h.approvedRCSTemplate(tenant, "E2E send", "carrier-tmpl-e2e")

	res := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId":   senderID.String(),
		"templateId": templateID.String(),
		"to":         "9876543210",
		"body":       "Hi Priya, your order A-1 shipped.",
		"variables":  map[string]string{"first_name": "Priya", "order_id": "A-1"},
	})
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", res.Code, res.Body)
	}
	var sent gen.SendMessageResult
	res.decode(t, &sent)
	if sent.Status != "sent" && sent.Status != "accepted" {
		t.Fatalf("status = %q; body = %s", sent.Status, res.Body)
	}

	if len(carrier.sawSubmissions) != 1 {
		t.Fatalf("carrier saw %d submissions, want 1", len(carrier.sawSubmissions))
	}
	submission := carrier.sawSubmissions[0]

	// The CARRIER's template id, not Relay's. Sending Relay's would be refused
	// at the gateway with "Template not found".
	if submission.CarrierTemplateID != carrierID {
		t.Errorf("CarrierTemplateID = %q, want the carrier's own id", submission.CarrierTemplateID)
	}
	// In the template's declared order, because Airtel's placeholders are
	// positional — the wrong order puts the order number in the greeting.
	if len(submission.TemplateVariables) != 2 {
		t.Fatalf("TemplateVariables = %v", submission.TemplateVariables)
	}
	if submission.TemplateVariables[0].Name != "first_name" ||
		submission.TemplateVariables[0].Value != "Priya" ||
		submission.TemplateVariables[1].Name != "order_id" ||
		submission.TemplateVariables[1].Value != "A-1" {
		t.Errorf("TemplateVariables = %+v, want the declared order filled", submission.TemplateVariables)
	}
	if submission.Channel != "RCS" {
		t.Errorf("Channel = %q", submission.Channel)
	}
}

// A variable the caller forgot is sent empty rather than dropped. Dropping it
// would shift every later position by one, and Airtel refuses a send with fewer
// values than the template declares.
func TestAMissingVariableIsSentEmptySoPositionsDoNotShift(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel"}
	h := newRCSSendHarness(t, carrier)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	senderID, templateID, _ := h.approvedRCSTemplate(tenant, "Partial fill", "carrier-tmpl-partial")

	res := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": senderID.String(), "templateId": templateID.String(),
		"to": "9876543211", "body": "Hi, your order shipped.",
		"variables": map[string]string{"order_id": "A-2"},
	})
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", res.Code, res.Body)
	}

	submission := carrier.sawSubmissions[0]
	if len(submission.TemplateVariables) != 2 {
		t.Fatalf("TemplateVariables = %v, want both slots present", submission.TemplateVariables)
	}
	if submission.TemplateVariables[0].Value != "" {
		t.Errorf("slot 1 = %q, want empty", submission.TemplateVariables[0].Value)
	}
	if submission.TemplateVariables[1].Value != "A-2" {
		t.Errorf("slot 2 = %q, want the value that WAS supplied", submission.TemplateVariables[1].Value)
	}
}

// The refusal that this whole feature exists to make possible: the money must
// not move for a template the carrier has not approved, because the gateway
// would refuse it after the hold was taken.
func TestASendIsRefusedBeforeAnyMoneyMovesWhenTheCarrierHasNotApproved(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel"}
	h := newRCSSendHarness(t, carrier)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	// Approved in Relay, never submitted to the carrier.
	templateID := h.rcsTemplate(tenant, "Unregistered", []string{"first_name", "order_id"}, "UTILITY")
	var senderID uuid.UUID
	if err := h.admin.QueryRow(context.Background(),
		`SELECT sender_id FROM templates WHERE id = $1`, templateID).Scan(&senderID); err != nil {
		t.Fatalf("read sender: %v", err)
	}

	before := h.walletBalance(tenant)

	res := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": senderID.String(), "templateId": templateID.String(),
		"to": "9876543212", "body": "Hi Priya",
		"variables": map[string]string{"first_name": "Priya", "order_id": "A-3"},
	})
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 with a refusal in the body; body = %s", res.Code, res.Body)
	}
	var sent gen.SendMessageResult
	res.decode(t, &sent)
	if sent.Status != "failed" {
		t.Fatalf("status = %q, want failed; body = %s", sent.Status, res.Body)
	}
	// Distinct from template_not_approved, which is OUR review. Collapsing them
	// sends the customer arguing with the wrong team.
	if sent.ErrorCode == nil || *sent.ErrorCode != "carrier_template_not_approved" {
		t.Errorf("errorCode = %v, want carrier_template_not_approved", sent.ErrorCode)
	}
	if len(carrier.sawSubmissions) != 0 {
		t.Error("a message with no carrier template still reached the carrier")
	}
	if after := h.walletBalance(tenant); after != before {
		t.Errorf("balance moved from %d to %d on a refused send", before, after)
	}
}

// The other half of the send path: nothing settles without a delivery report,
// and both carriers deliver theirs on a webhook.
func TestADeliveryWebhookSettlesTheMessageItNames(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel"}
	h := newRCSSendHarness(t, carrier)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	senderID, templateID, _ := h.approvedRCSTemplate(tenant, "Settle me", "carrier-tmpl-settle")

	send := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": senderID.String(), "templateId": templateID.String(),
		"to": "9876543213", "body": "Hi Priya, your order A-4 shipped.",
		"variables": map[string]string{"first_name": "Priya", "order_id": "A-4"},
	})
	if send.Code != http.StatusAccepted {
		t.Fatalf("send status = %d; body = %s", send.Code, send.Body)
	}
	var sent gen.SendMessageResult
	send.decode(t, &sent)
	if sent.Id == nil {
		t.Fatal("the send returned no message id")
	}

	// Airtel's payload contains nothing we control: the only key back to a
	// Relay message is the reference the carrier issued at submit.
	carrierRef := "carrier-ref-" + sent.Id.String()
	res := h.postWebhook("airtel", webhookToken, map[string]any{
		"messageId": carrierRef, "msisdn": "+919876543213",
		"msgStream": "OUTBOUND", "eventType": "DELIVERED",
		"sendTime": "2026-08-24T13:06:50.763386Z",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("webhook status = %d; body = %s", res.Code, res.Body)
	}

	if status := h.messageStatus(tenant, *sent.Id); status != "delivered" {
		t.Errorf("message status = %q, want delivered", status)
	}
}

// Carriers retry, so the same report arrives more than once. A terminal message
// must not move again, and it must not be charged again.
func TestAReplayedDeliveryWebhookChangesNothing(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel"}
	h := newRCSSendHarness(t, carrier)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	senderID, templateID, _ := h.approvedRCSTemplate(tenant, "Replay me", "carrier-tmpl-replay")

	send := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": senderID.String(), "templateId": templateID.String(),
		"to": "9876543214", "body": "Hi Priya, your order A-5 shipped.",
		"variables": map[string]string{"first_name": "Priya", "order_id": "A-5"},
	})
	var sent gen.SendMessageResult
	send.decode(t, &sent)
	if sent.Id == nil {
		t.Fatalf("no message id; body = %s", send.Body)
	}

	payload := map[string]any{
		"messageId": "carrier-ref-" + sent.Id.String(), "eventType": "DELIVERED",
	}
	if res := h.postWebhook("airtel", webhookToken, payload); res.Code != http.StatusOK {
		t.Fatalf("first webhook: %d", res.Code)
	}
	balanceAfterFirst := h.walletBalance(tenant)

	if res := h.postWebhook("airtel", webhookToken, payload); res.Code != http.StatusOK {
		t.Fatalf("replayed webhook: %d", res.Code)
	}
	if after := h.walletBalance(tenant); after != balanceAfterFirst {
		t.Errorf("balance moved from %d to %d on a replayed report", balanceAfterFirst, after)
	}
	if status := h.messageStatus(tenant, *sent.Id); status != "delivered" {
		t.Errorf("message status = %q after a replay, want delivered", status)
	}
}

// An expired message is refunded by both carriers, so Relay must release its
// hold rather than keep the charge.
func TestAnExpiredMessageWebhookReleasesTheHold(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel"}
	h := newRCSSendHarness(t, carrier)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	senderID, templateID, _ := h.approvedRCSTemplate(tenant, "Expire me", "carrier-tmpl-expire")

	before := h.walletBalance(tenant)
	send := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": senderID.String(), "templateId": templateID.String(),
		"to": "9876543215", "body": "Hi Priya, your order A-6 shipped.",
		"variables": map[string]string{"first_name": "Priya", "order_id": "A-6"},
	})
	var sent gen.SendMessageResult
	send.decode(t, &sent)
	if sent.Id == nil {
		t.Fatalf("no message id; body = %s", send.Body)
	}
	if held := h.walletBalance(tenant); held >= before {
		t.Fatalf("balance %d did not fall from %d — nothing was held", held, before)
	}

	res := h.postWebhook("airtel", webhookToken, map[string]any{
		"messageId": "carrier-ref-" + sent.Id.String(),
		"eventType": "TTL_EXPIRATION_REVOKED",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("webhook status = %d; body = %s", res.Code, res.Body)
	}

	if after := h.walletBalance(tenant); after != before {
		t.Errorf("balance = %d after an expired message, want the hold released back to %d",
			after, before)
	}
	if status := h.messageStatus(tenant, *sent.Id); status != "undelivered" {
		t.Errorf("message status = %q, want undelivered", status)
	}
}

// A report naming a message we never sent must be dropped, not applied to
// whatever happens to be nearest.
func TestADeliveryWebhookForAnUnknownReferenceSettlesNothing(t *testing.T) {
	h := newRCSSendHarness(t, &stubRegistrar{vendor: "airtel"})

	res := h.postWebhook("airtel", webhookToken, map[string]any{
		"messageId": "a-reference-we-never-issued", "eventType": "DELIVERED",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a non-200 starts a retry loop", res.Code)
	}
}

// A settled message replaces its previous row, so anything the reload does not
// read is erased. Carrier attribution used to vanish exactly here — which
// emptied the deliverability-by-carrier report of everything that had actually
// been delivered, and broke every SECOND webhook for a message, because an
// Airtel callback carries no id of ours and the carrier reference is the only
// way back.
func TestSettlingAMessageKeepsItsCarrierAttribution(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel"}
	h := newRCSSendHarness(t, carrier)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	senderID, templateID, _ := h.approvedRCSTemplate(tenant, "Keep carrier", "carrier-tmpl-keep")

	send := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": senderID.String(), "templateId": templateID.String(),
		"to": "9876543216", "body": "Hi Priya, your order A-7 shipped.",
		"variables": map[string]string{"first_name": "Priya", "order_id": "A-7"},
	})
	var sent gen.SendMessageResult
	send.decode(t, &sent)
	if sent.Id == nil {
		t.Fatalf("no message id; body = %s", send.Body)
	}
	carrierRef := "carrier-ref-" + sent.Id.String()

	if res := h.postWebhook("airtel", webhookToken, map[string]any{
		"messageId": carrierRef, "eventType": "DELIVERED",
	}); res.Code != http.StatusOK {
		t.Fatalf("delivery webhook: %d", res.Code)
	}

	storedRef, storedTemplate := h.messageCarrierRef(tenant, *sent.Id)
	if storedRef != carrierRef {
		t.Errorf("carrier_ref = %q after settling, want it carried forward", storedRef)
	}
	if storedTemplate != templateID.String() {
		t.Errorf("template_id = %q after settling, want it carried forward", storedTemplate)
	}

	// The consequence, not just the column: a later event for the same message
	// must still find it. Airtel sends READ after DELIVERED.
	if res := h.postWebhook("airtel", webhookToken, map[string]any{
		"messageId": carrierRef, "eventType": "READ",
	}); res.Code != http.StatusOK {
		t.Fatalf("read webhook: %d", res.Code)
	}
	if contains(h.logs.String(), "delivery report for a message we did not send") {
		t.Error("a second webhook could not find a message it had already settled")
	}
}

// The message log must name the gateway that actually carried it.
//
// Route selection picks the highest-priority carrier in a corridor, but an RCS
// send goes to whichever of Airtel or Vi this deployment holds credentials for,
// whatever the routes table would have chosen. Recording the routes table's
// answer meant the message went to Airtel and the log said Jio — so the
// deliverability-by-carrier report blamed the wrong network for every failure.
func TestTheRecordedCarrierIsTheGatewayThatActuallySent(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel"}
	h := newRCSSendHarness(t, carrier)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	senderID, templateID, _ := h.approvedRCSTemplate(tenant, "Attribution", "carrier-tmpl-attrib")

	// A higher-priority route for a DIFFERENT carrier in the same corridor,
	// which is exactly the situation that produced the wrong attribution.
	if _, err := h.admin.Exec(context.Background(), `
		INSERT INTO routes (country, channel, carrier, label, priority,
		    compliance_standing, cost_per_segment_minor, currency, status)
		VALUES ('IN', 'RCS', 'JIO', 'Jio RCS test', 1, 'registered', 30, 'INR', 'active')
		ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed competing route: %v", err)
	}

	send := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": senderID.String(), "templateId": templateID.String(),
		"to": "9876543217", "body": "Hi Priya, your order A-8 shipped.",
		"variables": map[string]string{"first_name": "Priya", "order_id": "A-8"},
	})
	var sent gen.SendMessageResult
	send.decode(t, &sent)
	if sent.Id == nil {
		t.Fatalf("no message id; body = %s", send.Body)
	}

	if recorded := h.messageCarrier(tenant, *sent.Id); recorded != "AIRTEL" {
		t.Errorf("recorded carrier = %q, want AIRTEL — the gateway it actually went through", recorded)
	}
}

// A deployment whose only connector is the sandbox has no carrier to have
// approved anything, and the message is going nowhere near one. Requiring a
// carrier's approval there would make RCS unusable in test mode and on every
// deployment without a commercial agreement — which is most of them, most of
// the time.
func TestWithNoRCSCarrierASendDoesNotWaitForACarriersApproval(t *testing.T) {
	h := newSendHarness(t) // sandbox only: no registry, no RCS carrier
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	templateID := h.rcsTemplate(tenant, "Sandbox RCS", []string{"first_name", "order_id"}, "UTILITY")
	var senderID uuid.UUID
	if err := h.admin.QueryRow(context.Background(),
		`SELECT sender_id FROM templates WHERE id = $1`, templateID).Scan(&senderID); err != nil {
		t.Fatalf("read sender: %v", err)
	}

	res := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": senderID.String(), "templateId": templateID.String(),
		"to": "9876543218", "body": "Welcome, Priya.",
		"variables": map[string]string{"first_name": "Priya", "order_id": "A-9"},
	})
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", res.Code, res.Body)
	}
	var sent gen.SendMessageResult
	res.decode(t, &sent)
	if sent.Status == "failed" {
		t.Fatalf("status = failed (%v) — an unregistered template must not block "+
			"a send that never reaches a carrier", sent.ErrorCode)
	}
}
