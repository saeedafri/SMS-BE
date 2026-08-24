package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// stubRegistrar stands in for a carrier that has a template API (Airtel) or one
// that does not (Vi). The vendors' own wire shapes are covered against fake HTTP
// servers in internal/connector; what these prove is what the ENDPOINT does with
// each answer.
type stubRegistrar struct {
	vendor string
	// issued is the id the carrier hands back; err short-circuits instead.
	issued string
	err    error

	sawSpec        connector.RCSTemplateSpec
	calls          int
	sawSubmissions []connector.Submission
	mu             sync.Mutex
}

func (r *stubRegistrar) Vendor() string { return r.vendor }
func (r *stubRegistrar) Name() string   { return r.vendor }

func (r *stubRegistrar) Health(context.Context) connector.Health {
	return connector.Health{Healthy: true}
}

func (r *stubRegistrar) Submit(_ context.Context, submissions []connector.Submission) ([]connector.Receipt, error) {
	r.mu.Lock()
	r.sawSubmissions = append(r.sawSubmissions, submissions...)
	r.mu.Unlock()

	receipts := make([]connector.Receipt, 0, len(submissions))
	for _, submission := range submissions {
		receipts = append(receipts, connector.Receipt{
			MessageID: submission.MessageID, Accepted: true,
			CarrierRef: "carrier-ref-" + submission.MessageID,
		})
	}
	return receipts, nil
}

func (r *stubRegistrar) Capability(_ context.Context, msisdn string) (connector.RCSCapability, error) {
	return connector.RCSCapability{Msisdn: msisdn, Reachable: true, Vendor: r.vendor}, nil
}

func (r *stubRegistrar) Reachable(_ context.Context, msisdns []string) ([]string, error) {
	return msisdns, nil
}

func (r *stubRegistrar) RegisterTemplate(_ context.Context,
	spec connector.RCSTemplateSpec) (connector.RCSTemplateRegistration, error) {
	r.calls++
	r.sawSpec = spec
	if r.err != nil {
		return connector.RCSTemplateRegistration{}, r.err
	}
	return connector.RCSTemplateRegistration{
		CarrierTemplateID: r.issued, Status: connector.RCSTemplatePending,
	}, nil
}

func (r *stubRegistrar) TemplateStatus(context.Context, string) (connector.RCSTemplateRegistration, error) {
	return connector.RCSTemplateRegistration{
		CarrierTemplateID: r.issued, Status: connector.RCSTemplatePending,
	}, nil
}

const webhookToken = "test-webhook-token"

// newCarrierHarness gives the server an RCS carrier and mounts the callbacks.
func newCarrierHarness(t *testing.T, carrier *stubRegistrar) *harness {
	t.Helper()
	h := newHarness(t)
	h.server.RCSCarrier = carrier
	h.server.Carriers = connector.Registry{
		ByChannel: map[string]connector.Connector{"RCS": carrier},
	}
	h.server.CarrierWebhookToken = webhookToken
	// The webhook routes are mounted at router-build time, so this variant
	// rebuilds rather than mutating — the one dependency that cannot be swapped
	// in place.
	h.rebuildRouter()
	return h
}

// rcsTemplate seeds an RCS sender and an RCS text template, approved in Relay.
// The carrier's own approval is the thing under test, so ours is set up front.
func (h *harness) rcsTemplate(tenant account, name string, variables []string, category string) uuid.UUID {
	h.t.Helper()
	ctx := context.Background()

	var senderID uuid.UUID
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, $2, 'RCS', 'IN', 'approved') RETURNING id`,
		tenant.TenantID, "RCS"+name[:3]).Scan(&senderID); err != nil {
		h.t.Fatalf("seed rcs sender: %v", err)
	}

	content, err := json.Marshal(map[string]any{
		"kind":        "text",
		"text":        "Hi {{first_name}}, your order {{order_id}} shipped.",
		"suggestions": []any{},
	})
	if err != nil {
		h.t.Fatalf("marshal rcs content: %v", err)
	}

	var templateID uuid.UUID
	var nullableCategory any
	if category != "" {
		nullableCategory = category
	}
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO templates (tenant_id, sender_id, name, channel, country,
		    variables, status, category, rcs_content)
		VALUES ($1, $2, $3, 'RCS', 'IN', $4, 'approved', $5, $6)
		RETURNING id`,
		tenant.TenantID, senderID, name, variables, nullableCategory, content).Scan(&templateID); err != nil {
		h.t.Fatalf("seed rcs template: %v", err)
	}
	return templateID
}

func (h *harness) templateCarrierState(templateID uuid.UUID) (vendor, carrierID, status, reason string) {
	h.t.Helper()
	var v, c, r *string
	if err := h.admin.QueryRow(context.Background(), `
		SELECT carrier_vendor, carrier_template_id, carrier_status, carrier_rejection_reason
		  FROM templates WHERE id = $1`, templateID).Scan(&v, &c, &status, &r); err != nil {
		h.t.Fatalf("read carrier state: %v", err)
	}
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return deref(v), deref(c), status, deref(r)
}

func TestSubmittingAnRCSTemplateToTheCarrierLeavesItPending(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel", issued: "01kct02npb5demxdk62wxjqmqb"}
	h := newCarrierHarness(t, carrier)
	tenant := h.newAccount("owner")
	templateID := h.rcsTemplate(tenant, "Order shipped",
		[]string{"first_name", "order_id"}, "UTILITY")

	res := h.do(http.MethodPost,
		"/v1/templates/"+templateID.String()+"/carrier-registration", tenant.Token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}

	var registration gen.CarrierTemplateRegistration
	res.decode(t, &registration)
	if registration.Status != "pending" {
		t.Errorf("status = %q, want pending — Airtel reviews for up to 24 hours", registration.Status)
	}
	if registration.CarrierTemplateId == nil || *registration.CarrierTemplateId != carrier.issued {
		t.Errorf("carrierTemplateId = %v, want the id the carrier issued", registration.CarrierTemplateId)
	}

	// The template's own variable ORDER is what the carrier is told, because
	// its placeholders are positional and the send path fills them in that
	// same order.
	if len(carrier.sawSpec.Variables) != 2 ||
		carrier.sawSpec.Variables[0] != "first_name" ||
		carrier.sawSpec.Variables[1] != "order_id" {
		t.Errorf("spec.Variables = %v, want the declared order", carrier.sawSpec.Variables)
	}
	// UTILITY is not a use case Airtel knows; it has to be mapped.
	if carrier.sawSpec.UseCase != "TRANSACTIONAL" {
		t.Errorf("spec.UseCase = %q, want TRANSACTIONAL", carrier.sawSpec.UseCase)
	}
	// The submitter is recorded in Airtel's event log and is the only audit
	// trail of who sent what.
	if carrier.sawSpec.SubmittedBy != tenant.Email {
		t.Errorf("spec.SubmittedBy = %q, want the caller's address", carrier.sawSpec.SubmittedBy)
	}

	vendor, carrierID, status, _ := h.templateCarrierState(templateID)
	if vendor != "airtel" || carrierID != carrier.issued || status != "pending" {
		t.Errorf("stored %q/%q/%q", vendor, carrierID, status)
	}
}

// Vi has no template API at all. The endpoint has to say where to go instead of
// failing with something that reads like an outage.
func TestACarrierWithNoTemplateAPISendsTheCustomerToItsPortal(t *testing.T) {
	carrier := &stubRegistrar{vendor: "vi", err: connector.ErrTemplateRegistrationManual}
	h := newCarrierHarness(t, carrier)
	tenant := h.newAccount("owner")
	templateID := h.rcsTemplate(tenant, "Vi promo", []string{"first_name"}, "MARKETING")

	res := h.do(http.MethodPost,
		"/v1/templates/"+templateID.String()+"/carrier-registration", tenant.Token, nil)
	if res.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", res.Code, res.Body)
	}
	if !contains(string(res.Body), "portal") {
		t.Errorf("body = %s, want it to name the portal", res.Body)
	}

	_, _, status, _ := h.templateCarrierState(templateID)
	if status != "not_submitted" {
		t.Errorf("carrier_status = %q, want it untouched", status)
	}
}

// Attaching a code obtained in the carrier's portal is the only route for Vi,
// and it records approved because the customer's assertion is the only source
// of truth Vi offers.
func TestAttachingACodeFromTheCarriersPortalUnblocksSending(t *testing.T) {
	carrier := &stubRegistrar{vendor: "vi", err: connector.ErrTemplateRegistrationManual}
	h := newCarrierHarness(t, carrier)
	tenant := h.newAccount("owner")
	templateID := h.rcsTemplate(tenant, "Vi attach", []string{"first_name"}, "MARKETING")

	res := h.do(http.MethodPost,
		"/v1/templates/"+templateID.String()+"/carrier-registration", tenant.Token,
		map[string]any{"carrierTemplateId": "vi_template_code_9"})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}

	var registration gen.CarrierTemplateRegistration
	res.decode(t, &registration)
	if registration.Status != "approved" {
		t.Errorf("status = %q, want approved", registration.Status)
	}
	if carrier.calls != 0 {
		t.Error("attaching a portal code still called the carrier's API")
	}

	vendor, carrierID, status, _ := h.templateCarrierState(templateID)
	if vendor != "vi" || carrierID != "vi_template_code_9" || status != "approved" {
		t.Errorf("stored %q/%q/%q", vendor, carrierID, status)
	}
}

// Two tenants sharing one carrier template id would each receive the other's
// approval webhooks, because the webhook matches on that id and nothing else.
func TestTheSameCarrierCodeCannotBeAttachedTwice(t *testing.T) {
	carrier := &stubRegistrar{vendor: "vi"}
	h := newCarrierHarness(t, carrier)
	first := h.newAccount("owner")
	second := h.newAccount("owner")
	firstTemplate := h.rcsTemplate(first, "Shared one", []string{"a"}, "MARKETING")
	secondTemplate := h.rcsTemplate(second, "Shared two", []string{"a"}, "MARKETING")

	body := map[string]any{"carrierTemplateId": "collision"}
	if res := h.do(http.MethodPost,
		"/v1/templates/"+firstTemplate.String()+"/carrier-registration", first.Token, body); res.Code != http.StatusOK {
		t.Fatalf("first attach: status = %d; body = %s", res.Code, res.Body)
	}
	res := h.do(http.MethodPost,
		"/v1/templates/"+secondTemplate.String()+"/carrier-registration", second.Token, body)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("second attach: status = %d, want 422; body = %s", res.Code, res.Body)
	}
}

func TestOnlyApprovedRCSTextTemplatesCanBeRegistered(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel", issued: "x"}
	h := newCarrierHarness(t, carrier)
	tenant := h.newAccount("owner")

	// Not RCS.
	smsSender := createSender(t, h, tenant.Token, "SMSHDR", "SMS", "IN")
	smsRes := h.do(http.MethodPost, "/v1/templates", tenant.Token, map[string]any{
		"name": "An SMS one", "senderId": smsSender.Id.String(), "body": "Hi {{name}}",
	})
	var smsTemplate gen.Template
	smsRes.decode(t, &smsTemplate)
	res := h.do(http.MethodPost,
		"/v1/templates/"+smsTemplate.Id.String()+"/carrier-registration", tenant.Token, nil)
	if res.Code != http.StatusUnprocessableEntity {
		t.Errorf("an SMS template: status = %d, want 422", res.Code)
	}

	// Approved in Relay first: the carrier's review is a second opinion on
	// content we already stand behind.
	pending := h.rcsTemplate(tenant, "Pending one", []string{"a"}, "MARKETING")
	if _, err := h.admin.Exec(context.Background(),
		`UPDATE templates SET status = 'pending_review' WHERE id = $1`, pending); err != nil {
		t.Fatalf("demote template: %v", err)
	}
	res = h.do(http.MethodPost,
		"/v1/templates/"+pending.String()+"/carrier-registration", tenant.Token, nil)
	if res.Code != http.StatusUnprocessableEntity {
		t.Errorf("an unapproved template: status = %d, want 422", res.Code)
	}

	// No category: Airtel needs a use case and a promotional template under a
	// transactional agent is auto-rejected, so there is no safe default.
	uncategorised := h.rcsTemplate(tenant, "No category", []string{"a"}, "")
	res = h.do(http.MethodPost,
		"/v1/templates/"+uncategorised.String()+"/carrier-registration", tenant.Token, nil)
	if res.Code != http.StatusUnprocessableEntity {
		t.Errorf("an uncategorised template: status = %d, want 422; body = %s", res.Code, res.Body)
	}
	if carrier.calls != 0 {
		t.Errorf("the carrier was called %d times for templates that could never be registered", carrier.calls)
	}
}

func TestATemplateCarriesItsCarrierRegistrationOnRead(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel", issued: "tmpl-77"}
	h := newCarrierHarness(t, carrier)
	tenant := h.newAccount("owner")
	templateID := h.rcsTemplate(tenant, "Readback", []string{"first_name"}, "UTILITY")

	if res := h.do(http.MethodPost,
		"/v1/templates/"+templateID.String()+"/carrier-registration", tenant.Token, nil); res.Code != http.StatusOK {
		t.Fatalf("register: status = %d; body = %s", res.Code, res.Body)
	}

	read := h.do(http.MethodGet, "/v1/templates/"+templateID.String(), tenant.Token, nil)
	if read.Code != http.StatusOK {
		t.Fatalf("read: status = %d; body = %s", read.Code, read.Body)
	}
	var template gen.Template
	read.decode(t, &template)
	if template.CarrierRegistration == nil {
		t.Fatal("an RCS template came back with no carrierRegistration")
	}
	registration, err := template.CarrierRegistration.AsCarrierTemplateRegistration()
	if err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	if registration.Status != "pending" || registration.CarrierTemplateId == nil {
		t.Errorf("registration = %+v", registration)
	}
}

// Showing it on a channel that can never have one puts a field on screen that
// is permanently meaningless.
func TestANonRCSTemplateHasNoCarrierRegistrationField(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	sender := createSender(t, h, tenant.Token, "PLAINS", "SMS", "IN")
	res := h.do(http.MethodPost, "/v1/templates", tenant.Token, map[string]any{
		"name": "Plain SMS", "senderId": sender.Id.String(), "body": "Hi {{name}}",
	})
	var template gen.Template
	res.decode(t, &template)
	if template.CarrierRegistration != nil {
		t.Error("an SMS template carries a carrier registration")
	}
}

// --- webhooks ---

func (h *harness) postWebhook(vendor, token string, payload any) response {
	h.t.Helper()
	return h.do(http.MethodPost,
		fmt.Sprintf("/v1/carrier-webhooks/rcs/%s/%s", vendor, token), "", payload)
}

// 404, not 401: a wrong token must not confirm the endpoint exists.
func TestACarrierWebhookWithTheWrongTokenIsNotFound(t *testing.T) {
	h := newCarrierHarness(t, &stubRegistrar{vendor: "airtel"})

	res := h.postWebhook("airtel", "wrong-token",
		map[string]any{"messageId": "m", "eventType": "DELIVERED"})
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.Code)
	}
	if code := res.errorCode(t); code != "not_found" {
		t.Errorf("error.code = %q, want not_found", code)
	}
}

func TestAnUnknownCarrierIsNotFound(t *testing.T) {
	h := newCarrierHarness(t, &stubRegistrar{vendor: "airtel"})

	res := h.postWebhook("jio", webhookToken,
		map[string]any{"messageId": "m", "eventType": "DELIVERED"})
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.Code)
	}
}

// The routes are not mounted at all without a token, for the same reason the
// dev hooks are not: an endpoint that settles messages and approves templates
// should not exist where no carrier can call it.
func TestCarrierWebhooksAreAbsentWithoutAToken(t *testing.T) {
	h := newHarness(t)

	res := h.postWebhook("airtel", "anything",
		map[string]any{"messageId": "m", "eventType": "DELIVERED"})
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.Code)
	}
}

func TestATemplateApprovalWebhookUnblocksTheTemplate(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel", issued: "01kpz85rangx4j0sfwdrkjahtc"}
	h := newCarrierHarness(t, carrier)
	tenant := h.newAccount("owner")
	templateID := h.rcsTemplate(tenant, "Webhook approve", []string{"first_name"}, "UTILITY")

	if res := h.do(http.MethodPost,
		"/v1/templates/"+templateID.String()+"/carrier-registration", tenant.Token, nil); res.Code != http.StatusOK {
		t.Fatalf("register: %s", res.Body)
	}

	res := h.postWebhook("airtel", webhookToken, map[string]any{
		"messageId":  carrier.issued,
		"templateId": carrier.issued,
		"eventType":  "TEMPLATE_APPROVED",
		"messageContent": map[string]any{
			"templateStatus": "APPROVED", "templateId": carrier.issued,
		},
		"sendTime": "2026-08-24T09:58:40.558999671Z",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("webhook status = %d; body = %s", res.Code, res.Body)
	}

	_, _, status, _ := h.templateCarrierState(templateID)
	if status != "approved" {
		t.Errorf("carrier_status = %q, want approved", status)
	}
}

func TestATemplateRejectionWebhookKeepsTheCarriersReason(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel", issued: "01kq9cran603qxppvevgs7fn42"}
	h := newCarrierHarness(t, carrier)
	tenant := h.newAccount("owner")
	templateID := h.rcsTemplate(tenant, "Webhook reject", []string{"first_name"}, "MARKETING")

	if res := h.do(http.MethodPost,
		"/v1/templates/"+templateID.String()+"/carrier-registration", tenant.Token, nil); res.Code != http.StatusOK {
		t.Fatalf("register: %s", res.Body)
	}

	res := h.postWebhook("airtel", webhookToken, map[string]any{
		"messageId":  carrier.issued,
		"templateId": carrier.issued,
		"eventType":  "TEMPLATE_REJECTED",
		"messageContent": map[string]any{
			"templateStatus":  "REJECTED",
			"templateId":      carrier.issued,
			"rejectionReason": "The content is not appropriate.",
		},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("webhook status = %d; body = %s", res.Code, res.Body)
	}

	_, _, status, reason := h.templateCarrierState(templateID)
	if status != "rejected" {
		t.Errorf("carrier_status = %q, want rejected", status)
	}
	// Without the reason the customer is told "rejected" and has to guess.
	if reason != "The content is not appropriate." {
		t.Errorf("rejection reason = %q", reason)
	}
}

// A carrier that gets a non-200 retries, and retrying will never make us hold a
// template registered by some other system.
func TestAWebhookForSomethingWeDoNotHoldIsStillAcknowledged(t *testing.T) {
	h := newCarrierHarness(t, &stubRegistrar{vendor: "airtel"})

	res := h.postWebhook("airtel", webhookToken, map[string]any{
		"messageId": "someone-elses-template", "templateId": "someone-elses-template",
		"eventType":      "TEMPLATE_APPROVED",
		"messageContent": map[string]any{"templateStatus": "APPROVED"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a non-200 starts a retry loop", res.Code)
	}
}

// The one case worth refusing: a body we cannot parse is a bug on their side or
// ours, and answering 200 would hide it forever.
func TestAnUnparseableWebhookIsRefused(t *testing.T) {
	h := newCarrierHarness(t, &stubRegistrar{vendor: "airtel"})

	res := h.do(http.MethodPost,
		"/v1/carrier-webhooks/rcs/airtel/"+webhookToken, "",
		map[string]any{"messageId": "m"}) // no eventType
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", res.Code, res.Body)
	}
}

func TestAnInboundWebhookIsAcknowledgedEvenThoughTheInboxIsNotWiredYet(t *testing.T) {
	h := newCarrierHarness(t, &stubRegistrar{vendor: "airtel"})

	res := h.postWebhook("airtel", webhookToken, map[string]any{
		"messageId": "Mxa0", "msisdn": "+919820000002", "msgStream": "INBOUND",
		"eventType": "RECEIVED",
		"messageContent": map[string]any{
			"text": "Okay", "postbackData": "user_yes", "type": "REPLY",
		},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}
	// Logged rather than dropped, so the traffic is visible while the inbox
	// integration is built and nobody concludes the carrier is not sending it.
	if !contains(h.logs.String(), "inbound RCS received but not yet threaded") {
		t.Error("inbound RCS was dropped without a trace")
	}
}
