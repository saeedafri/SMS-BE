package api_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// India's operators do not judge whether a message looks reasonable. They match
// its content against a template registered on DLT and drop everything that
// does not match — all of it. So a send with no template, or with text that is
// not an instantiation of the one it names, cannot arrive on a real Indian
// route no matter what we do afterwards.
//
// Accepting, charging for and reporting such a message as sent is a worse
// failure than refusing it, because nothing surfaces until a customer complains
// that nothing arrived.

// registeredTemplate seeds an approved template with real fixed text, which is
// what an actual DLT content template looks like.
func (h *harness) registeredTemplate(tenant account, senderID, body string) string {
	h.t.Helper()
	var templateID string
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO templates (tenant_id, sender_id, name, channel, country, body,
		    status, external_id, dlt_category)
		VALUES ($1, $2, $3, 'SMS', 'IN', $4, 'approved', $5, 'TRANSACTIONAL')
		RETURNING id`,
		tenant.TenantID, senderID, fmt.Sprintf("Registered %d", h.nextSenderSeq()),
		body, "12070195011234567890").Scan(&templateID); err != nil {
		h.t.Fatalf("seed registered template: %v", err)
	}
	return templateID
}

type sendOutcome struct {
	ID        *string `json:"id"`
	Status    string  `json:"status"`
	ErrorCode *string `json:"errorCode"`
	CostMinor int64   `json:"costMinor"`
}

func (h *harness) sendIndia(tenant account, payload map[string]any) sendOutcome {
	h.t.Helper()
	response := h.do(http.MethodPost, "/v1/messages", tenant.Token, payload)
	if response.Code != http.StatusAccepted {
		h.t.Fatalf("send = %d\n%s", response.Code, response.Body)
	}
	var out sendOutcome
	response.decode(h.t, &out)
	return out
}

// The gap the document opens with: an India send needed no template at all.
func TestAnIndiaSendWithNoTemplateIsRefusedAtZeroCost(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	h.fundWallet(tenant)
	before := h.walletBalance(tenant)

	out := h.sendIndia(tenant, map[string]any{
		"senderId": sender, "to": "9876500011",
		"body": "Your Acme order 4821 has shipped.",
	})

	if out.Status == "sent" || out.Status == "queued" {
		t.Fatal("an India send with no registered template was accepted — an " +
			"Indian operator drops 100% of that traffic and the customer is " +
			"billed for messages that cannot arrive")
	}
	if out.ErrorCode == nil || *out.ErrorCode != "registered_template_required" {
		t.Fatalf("errorCode = %v, want registered_template_required", out.ErrorCode)
	}
	// Refuse before charging. A refused send that debits the wallet is worse
	// than no gate at all.
	if out.CostMinor != 0 {
		t.Fatalf("costMinor = %d on a refusal, want 0", out.CostMinor)
	}
	if after := h.walletBalance(tenant); after != before {
		t.Fatalf("the wallet moved by %d on a refused send", before-after)
	}
}

// The second probe in the document: a templateId that is carried but whose
// registered text has nothing to do with the body being sent.
func TestABodyThatIsNotAnInstantiationOfItsTemplateIsRefused(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.registeredTemplate(tenant, sender,
		"Hi {{first_name}}, your order {{order_id}} has shipped. Track: {{url}}")
	h.fundWallet(tenant)
	before := h.walletBalance(tenant)

	out := h.sendIndia(tenant, map[string]any{
		"senderId": sender, "templateId": template, "to": "9876500012",
		"body": "Totally unrelated text that matches no template.",
	})

	if out.Status == "sent" || out.Status == "queued" {
		t.Fatal("a body unrelated to its own registered template was accepted")
	}
	if out.ErrorCode == nil || *out.ErrorCode != "template_body_mismatch" {
		t.Fatalf("errorCode = %v, want template_body_mismatch", out.ErrorCode)
	}
	if after := h.walletBalance(tenant); after != before {
		t.Fatalf("the wallet moved by %d on a refused send", before-after)
	}
}

// A substring check is not enough: a message that merely opens with the
// template's words is a different message, and the operator drops it.
func TestAMessageThatMerelyOpensWithTheTemplateIsStillRefused(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.registeredTemplate(tenant, sender,
		"Hi {{first_name}}, your order has shipped.")
	h.fundWallet(tenant)

	out := h.sendIndia(tenant, map[string]any{
		"senderId": sender, "templateId": template, "to": "9876500013",
		"body": "Hi Priya, your order has shipped. WIN FREE CASH NOW",
	})
	if out.Status == "sent" || out.Status == "queued" {
		t.Fatal("text appended after the template's own body was accepted")
	}
	if out.ErrorCode == nil || *out.ErrorCode != "template_body_mismatch" {
		t.Fatalf("errorCode = %v, want template_body_mismatch", out.ErrorCode)
	}
}

// And the case that must keep working, or the gate is just an outage.
func TestALegalInstantiationOfARegisteredTemplateSends(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	template := h.registeredTemplate(tenant, sender,
		"Hi {{first_name}}, your order {{order_id}} has shipped.")
	h.fundWallet(tenant)

	out := h.sendIndia(tenant, map[string]any{
		"senderId": sender, "templateId": template, "to": "9876500014",
		"body": "Hi Priya, your order 4821 has shipped.",
	})
	if out.Status != "sent" && out.Status != "queued" {
		t.Fatalf("a legal instantiation was refused: status %q (%v)",
			out.Status, out.ErrorCode)
	}
	if out.CostMinor <= 0 {
		t.Fatalf("costMinor = %d on a real send", out.CostMinor)
	}
}

// A template approved for one sender header is not approved for another: on
// DLT they are separate registrations.
func TestATemplateRegisteredForAnotherSenderIsRefused(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	other := h.approvedSenderOn(tenant, "RCS")
	template := h.registeredTemplate(tenant, other, "Hello there.")
	h.fundWallet(tenant)

	out := h.sendIndia(tenant, map[string]any{
		"senderId": sender, "templateId": template, "to": "9876500015",
		"body": "Hello there.",
	})
	if out.Status == "sent" || out.Status == "queued" {
		t.Fatal("a template registered against a different sender header was accepted")
	}
}

// A template belonging to somebody else is not a template.
func TestATemplateFromAnotherTenantIsRefused(t *testing.T) {
	h := newSendHarness(t)
	mine := h.newAccount("owner")
	theirs := h.newAccount("owner")
	mySender := h.approvedSender(mine)
	theirSender := h.approvedSender(theirs)
	theirTemplate := h.registeredTemplate(theirs, theirSender, "Hello there.")
	h.fundWallet(mine)

	out := h.sendIndia(mine, map[string]any{
		"senderId": mySender, "templateId": theirTemplate, "to": "9876500016",
		"body": "Hello there.",
	})
	if out.Status == "sent" || out.Status == "queued" {
		t.Fatal("a template belonging to another tenant was accepted")
	}
}

// The rule is a property of the regime, not a global one. A country whose
// regulator does not register templates must be unaffected.
func TestACountryWithoutTheRuleStillSendsWithNoTemplate(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	var sender string
	if err := h.admin.QueryRow(context.Background(), `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, 'USSEND', 'SMS', 'US', 'approved') RETURNING id`,
		tenant.TenantID).Scan(&sender); err != nil {
		t.Fatalf("seed US sender: %v", err)
	}
	h.appendTopup(tenant, "USD", 1_000_000)

	out := h.sendIndia(tenant, map[string]any{
		"senderId": sender, "to": "+12025550123", "body": "Anything at all.",
	})
	if out.ErrorCode != nil &&
		(*out.ErrorCode == "registered_template_required" ||
			*out.ErrorCode == "template_body_mismatch") {
		t.Fatalf("a US send was refused by India's template rule (%s) — the rule "+
			"is a property of the regime, not a new global requirement", *out.ErrorCode)
	}
}

// Items A and C of the DLT spine document, checked end to end through the API
// rather than by seeding the columns.
//
// The frontend's probe read live templates, saw neither field, and concluded
// they were not built. Both are in fact accepted on create and stored verbatim
// — the fixtures they read simply had no value in either.
func TestATemplatesDltIdentifiersRoundTripThroughTheApi(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)

	const registrationID = "1207161234567890123"
	created := h.do(http.MethodPost, "/v1/templates", tenant.Token, map[string]any{
		"name": "DLT round trip", "channel": "SMS", "country": "IN",
		"senderId": sender, "body": "Hi {{first_name}}, your order has shipped.",
		"registrationId": registrationID,
		"dltCategory":    "TRANSACTIONAL",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create template = %d\n%s", created.Code, created.Body)
	}
	var template struct {
		ID             string  `json:"id"`
		RegistrationID *string `json:"registrationId"`
		DltCategory    *string `json:"dltCategory"`
	}
	created.decode(t, &template)
	if template.RegistrationID == nil || *template.RegistrationID != registrationID {
		t.Fatalf("registrationId on create = %v, want %q — nothing may generate "+
			"or drop the id the regulator issued to the customer",
			template.RegistrationID, registrationID)
	}
	if template.DltCategory == nil || *template.DltCategory != "TRANSACTIONAL" {
		t.Fatalf("dltCategory on create = %v, want TRANSACTIONAL", template.DltCategory)
	}

	// And it must survive a read, byte for byte.
	list := h.do(http.MethodGet, "/v1/templates", tenant.Token, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list templates = %d\n%s", list.Code, list.Body)
	}
	var templates []struct {
		ID             string  `json:"id"`
		RegistrationID *string `json:"registrationId"`
		DltCategory    *string `json:"dltCategory"`
	}
	list.decode(t, &templates)
	for _, row := range templates {
		if row.ID != template.ID {
			continue
		}
		if row.RegistrationID == nil || *row.RegistrationID != registrationID {
			t.Fatalf("registrationId on read = %v, want %q", row.RegistrationID, registrationID)
		}
		if row.DltCategory == nil || *row.DltCategory != "TRANSACTIONAL" {
			t.Fatalf("dltCategory on read = %v", row.DltCategory)
		}
		return
	}
	t.Fatal("the template just created is not in the list")
}

// An empty string is not a currency code, and it was what every early refusal
// returned — no such sender, no rate, content the country bans. The contract
// makes currency a required non-null string, so a refusal has to name one.
func TestARefusalStillNamesACurrency(t *testing.T) {
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSender(tenant)
	h.fundWallet(tenant)

	refusals := []struct {
		name    string
		payload map[string]any
	}{
		// An unknown sender id is a 422 from the handler with no result body at
		// all, which is right and is not this test's business. These are the
		// refusals that DO come back as a SendMessageResult.
		{"no template where the regime needs one", map[string]any{
			"senderId": sender, "to": "9876500022", "body": "Hello."}},
		{"content the country bans", map[string]any{
			"senderId": sender, "to": "9876500023",
			"body": "Claim now http://bit.ly/abc123"}},
	}

	for _, refusal := range refusals {
		t.Run(refusal.name, func(t *testing.T) {
			response := h.do(http.MethodPost, "/v1/messages", tenant.Token, refusal.payload)
			if response.Code != http.StatusAccepted {
				t.Fatalf("send = %d\n%s", response.Code, response.Body)
			}
			var out struct {
				Status   string `json:"status"`
				Currency string `json:"currency"`
			}
			response.decode(t, &out)
			if out.Status == "sent" || out.Status == "queued" {
				t.Fatalf("expected a refusal, got %q", out.Status)
			}
			if out.Currency == "" {
				t.Fatal(`currency = "" on a refusal — an empty string is not a currency code`)
			}
		})
	}
}
