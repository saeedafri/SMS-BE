package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/gen/api"
)

// newSender registers a sender and returns it, so each test below starts from a
// sender nothing references.
func newSender(t *testing.T, h *harness, token, header, channel, country string) gen.SenderId {
	t.Helper()
	body := map[string]any{"header": header, "channel": channel, "country": country}
	if channel == "SMS" || channel == "RCS" {
		body["registrationId"] = "1101" + uuid.NewString()[:12]
	}
	res := h.do(http.MethodPost, "/v1/sender-ids", token, body)
	if res.Code != http.StatusCreated {
		t.Fatalf("create sender = %d: %s", res.Code, res.Body)
	}
	var sender gen.SenderId
	res.decode(t, &sender)
	return sender
}

// A typo in a header was permanent for the life of the account.
func TestAnUnverifiedSenderCanBeCorrected(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	sender := newSender(t, h, acct.Token, "ACMERT", "SMS", "IN")

	res := h.do(http.MethodPatch, "/v1/sender-ids/"+sender.Id.String(), acct.Token,
		map[string]any{"header": "ACMECO"})
	if res.Code != http.StatusOK {
		t.Fatalf("patch = %d, want 200; body = %s", res.Code, res.Body)
	}
	var updated gen.SenderId
	res.decode(t, &updated)
	if updated.Header != "ACMECO" {
		t.Fatalf("header = %q, want ACMECO", updated.Header)
	}
}

// An empty object asks for nothing and is not an error.
func TestAnEmptyPatchIsANoOp(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	sender := newSender(t, h, acct.Token, "NOOPHD", "SMS", "IN")

	res := h.do(http.MethodPatch, "/v1/sender-ids/"+sender.Id.String(), acct.Token,
		map[string]any{})
	if res.Code != http.StatusOK {
		t.Fatalf("empty patch = %d, want 200; body = %s", res.Code, res.Body)
	}
	var updated gen.SenderId
	res.decode(t, &updated)
	if updated.Header != "NOOPHD" {
		t.Fatalf("an empty patch changed the header to %q", updated.Header)
	}
}

// A verified sender's header is bound to the registry entry that granted it.
// Changing it would leave the platform sending under a header no registry
// approved.
func TestAVerifiedSenderCannotBeEdited(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	operator := h.operatorToken()
	sender := newSender(t, h, acct.Token, "BOUNDH", "SMS", "IN")

	approve := h.do(http.MethodPost, "/v1/operator/senders/"+sender.Id.String()+"/approve",
		operator, map[string]any{})
	if approve.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", approve.Code, approve.Body)
	}

	res := h.do(http.MethodPatch, "/v1/sender-ids/"+sender.Id.String(), acct.Token,
		map[string]any{"header": "CHANGED"})
	if res.Code != http.StatusConflict {
		t.Fatalf("editing a verified sender = %d, want 409; body = %s", res.Code, res.Body)
	}
}

// displayName is a WhatsApp Business concept. Storing it on an SMS sender
// records a field that channel has no meaning for.
func TestDisplayNameIsWhatsAppOnly(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	sms := newSender(t, h, acct.Token, "SMSHDR", "SMS", "IN")

	res := h.do(http.MethodPatch, "/v1/sender-ids/"+sms.Id.String(), acct.Token,
		map[string]any{"displayName": "Acme Retail"})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("displayName on an SMS sender = %d, want 422; body = %s", res.Code, res.Body)
	}

	wa := newSender(t, h, acct.Token, "+919820000123", "WHATSAPP", "IN")
	ok := h.do(http.MethodPatch, "/v1/sender-ids/"+wa.Id.String(), acct.Token,
		map[string]any{"displayName": "Acme Retail"})
	if ok.Code != http.StatusOK {
		t.Fatalf("displayName on a WhatsApp sender = %d, want 200; body = %s", ok.Code, ok.Body)
	}
}

// Clearing is a distinct request from omitting, and both arrive as a nil
// pointer. Where the regime issues a registration id, clearing it must be
// refused — otherwise an edit reaches a state the create path would refuse.
func TestClearingARequiredRegistrationIdIsRefused(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	sender := newSender(t, h, acct.Token, "DLTHDR", "SMS", "IN")

	clear := h.do(http.MethodPatch, "/v1/sender-ids/"+sender.Id.String(), acct.Token,
		map[string]any{"registrationId": nil})
	if clear.Code != http.StatusUnprocessableEntity {
		t.Fatalf("clearing a required registrationId = %d, want 422; body = %s",
			clear.Code, clear.Body)
	}

	// Omitting the same field is not a clear, and must succeed.
	omit := h.do(http.MethodPatch, "/v1/sender-ids/"+sender.Id.String(), acct.Token,
		map[string]any{"header": "DLTHD2"})
	if omit.Code != http.StatusOK {
		t.Fatalf("omitting registrationId = %d, want 200; body = %s", omit.Code, omit.Body)
	}
	var updated gen.SenderId
	omit.decode(t, &updated)
	if updated.RegistrationId == nil || *updated.RegistrationId == "" {
		t.Fatal("omitting registrationId cleared it — omit and null must differ")
	}
}

// A sender nothing uses can be retired, including a verified one: what gates
// delete is use, not standing.
func TestAVerifiedSenderNothingUsesCanBeDeleted(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	operator := h.operatorToken()
	sender := newSender(t, h, acct.Token, "RETIRE", "SMS", "IN")

	if res := h.do(http.MethodPost, "/v1/operator/senders/"+sender.Id.String()+"/approve",
		operator, map[string]any{}); res.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", res.Code, res.Body)
	}

	res := h.do(http.MethodDelete, "/v1/sender-ids/"+sender.Id.String(), acct.Token, nil)
	if res.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204; body = %s", res.Code, res.Body)
	}
	after := h.do(http.MethodGet, "/v1/sender-ids/"+sender.Id.String(), acct.Token, nil)
	if after.Code != http.StatusNotFound {
		t.Fatalf("the sender still resolves after delete: %d", after.Code)
	}
}

// Each reference type alone.
//
// The frontend shipped this check with only templates and campaigns covered,
// then proved by mutation that the journey branch was untested — the test
// passed with that branch deleted, because the seeded sender was referenced by
// all three at once. So every case here builds a sender referenced by exactly
// one thing, and deleting the journey branch fails the journey case only.
func TestDeleteIsRefusedByEachKindOfReferenceOnItsOwn(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	ctx := context.Background()

	for _, ref := range []struct {
		name    string
		attach  func(t *testing.T, senderID uuid.UUID)
		expects string
	}{
		{
			name: "template",
			attach: func(t *testing.T, senderID uuid.UUID) {
				res := h.do(http.MethodPost, "/v1/templates", acct.Token, map[string]any{
					"name": "T " + uuid.NewString()[:8], "senderId": senderID.String(),
					"body": "Hello {{name}}",
				})
				if res.Code != http.StatusCreated {
					t.Fatalf("create template = %d: %s", res.Code, res.Body)
				}
			},
			expects: "1 template",
		},
		{
			name: "campaign",
			attach: func(t *testing.T, senderID uuid.UUID) {
				template := newTemplateFor(t, h, acct.Token, senderID)
				if _, err := h.admin.Exec(ctx, `
					INSERT INTO campaigns (tenant_id, name, channel, country, sender_id,
					                       template_id, status, recipients,
					                       segments_per_message, cost_minor_min,
					                       cost_minor_max, currency)
					VALUES ($1, $2, 'SMS', 'IN', $3, $4, 'scheduled', 0, 1, 0, 0, 'INR')`,
					acct.TenantID, "C "+uuid.NewString()[:8], senderID, template); err != nil {
					t.Fatalf("insert campaign: %v", err)
				}
			},
			expects: "1 campaign",
		},
		{
			name: "campaign fallback",
			attach: func(t *testing.T, senderID uuid.UUID) {
				other := newSender(t, h, acct.Token, "FB"+uuid.NewString()[:4], "SMS", "IN")
				template := newTemplateFor(t, h, acct.Token, other.Id)
				if _, err := h.admin.Exec(ctx, `
					INSERT INTO campaigns (tenant_id, name, channel, country, sender_id,
					                       template_id, fallback_sender_id, status,
					                       recipients, segments_per_message,
					                       cost_minor_min, cost_minor_max, currency)
					VALUES ($1, $2, 'SMS', 'IN', $3, $4, $5, 'scheduled', 0, 1, 0, 0, 'INR')`,
					acct.TenantID, "F "+uuid.NewString()[:8], other.Id, template,
					senderID); err != nil {
					t.Fatalf("insert fallback campaign: %v", err)
				}
			},
			expects: "1 campaign fallback",
		},
		{
			name: "journey",
			attach: func(t *testing.T, senderID uuid.UUID) {
				if _, err := h.admin.Exec(ctx, `
					INSERT INTO journeys (tenant_id, name, status, trigger_type, steps)
					VALUES ($1, $2, 'draft', 'list_entry', $3::jsonb)`,
					acct.TenantID, "J "+uuid.NewString()[:8],
					`[{"id":"s1","type":"send","channel":"SMS","senderId":"`+
						senderID.String()+`"}]`); err != nil {
					t.Fatalf("insert journey: %v", err)
				}
			},
			expects: "1 journey",
		},
		{
			name: "Verify service",
			attach: func(t *testing.T, senderID uuid.UUID) {
				if _, err := h.admin.Exec(ctx, `
					INSERT INTO verify_services (tenant_id, name, channels, fallback_order)
					VALUES ($1, $2, $3::jsonb, ARRAY['SMS'])`,
					acct.TenantID, "V "+uuid.NewString()[:8],
					`[{"channel":"SMS","senderId":"`+senderID.String()+
						`","body":"{{code}}"}]`); err != nil {
					t.Fatalf("insert verify service: %v", err)
				}
			},
			expects: "1 Verify service",
		},
	} {
		t.Run(ref.name, func(t *testing.T) {
			sender := newSender(t, h, acct.Token, "R"+uuid.NewString()[:5], "SMS", "IN")
			ref.attach(t, sender.Id)

			res := h.do(http.MethodDelete, "/v1/sender-ids/"+sender.Id.String(), acct.Token, nil)
			if res.Code != http.StatusConflict {
				t.Fatalf("delete with a %s referencing it = %d, want 409; body = %s",
					ref.name, res.Code, res.Body)
			}
			if !contains(string(res.Body), ref.expects) {
				t.Errorf("refusal does not name what is using it.\n got: %s\nwant it to contain: %q",
					string(res.Body), ref.expects)
			}
		})
	}
}

// newTemplateFor creates a template on a sender, for fixtures that need a
// campaign — campaigns.template_id is NOT NULL.
func newTemplateFor(t *testing.T, h *harness, token string, senderID uuid.UUID) uuid.UUID {
	t.Helper()
	res := h.do(http.MethodPost, "/v1/templates", token, map[string]any{
		"name": "TF " + uuid.NewString()[:8], "senderId": senderID.String(),
		"body": "Hello {{name}}",
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("create template for fixture = %d: %s", res.Code, res.Body)
	}
	var template struct {
		ID uuid.UUID `json:"id"`
	}
	res.decode(t, &template)
	return template.ID
}
