package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// Per-tenant agent resolution: which brand a real message actually goes out
// under.
//
// Everything else in the RCS feature is a screen. This is the part a handset
// shows, and getting it wrong means one customer's message arriving under
// another customer's name — visible to the recipient, invisible to us, and
// indistinguishable from a successful send in every log and screen we have.

// launchAgentOnCarrier gives an agent the approved launch and carrier-issued id
// that a send resolves through. Written directly because the operator route
// that issues one belongs to the carrier in real life, not to us.
func (h *harness) launchAgentOnCarrier(acct account, agentID uuid.UUID,
	carrier, carrierAgentID string) {

	h.t.Helper()
	if _, err := h.admin.Exec(context.Background(), `
		INSERT INTO rcs_agent_carrier_launches
		    (agent_id, tenant_id, carrier, status, carrier_agent_id, submitted_at)
		VALUES ($1, $2, $3, 'approved', $4, now())
		ON CONFLICT (agent_id, carrier) DO UPDATE
		    SET status = 'approved', carrier_agent_id = EXCLUDED.carrier_agent_id`,
		agentID, acct.TenantID, carrier, carrierAgentID); err != nil {
		h.t.Fatalf("launch agent on %s: %v", carrier, err)
	}
}

// attachAgentToSender is what POST /v1/sender-ids will do once rcsAgentId is
// wired on registration. Written here so resolution can be proved before the
// registration path carries the field.
func (h *harness) attachAgentToSender(senderID, agentID uuid.UUID) {
	h.t.Helper()
	if _, err := h.admin.Exec(context.Background(),
		`UPDATE sender_ids SET rcs_agent_id = $2 WHERE id = $1`, senderID, agentID); err != nil {
		h.t.Fatalf("attach agent to sender: %v", err)
	}
}

// agentForSend seeds the whole chain a resolved send needs: an approved entity,
// an agent, an approved AIRTEL launch carrying the carrier's id, and a sender
// pointing at the agent.
func (h *harness) agentForSend(acct account, name, carrierAgentID string) (
	senderID, templateID uuid.UUID, carrierTemplateID string) {

	h.t.Helper()
	senderID, templateID, carrierTemplateID = h.approvedRCSTemplate(acct, name, "tmpl")

	// Overwrite the helper's own agent with one whose carrier id this test can
	// name, so the assertion is about a specific value rather than "something
	// non-empty".
	agentID := uuid.MustParse(h.createAgent(acct, name+" named").Id)
	h.launchAgentOnCarrier(acct, agentID, "AIRTEL", carrierAgentID)
	h.attachAgentToSender(senderID, agentID)
	return senderID, templateID, carrierTemplateID
}

// The message goes out under the CUSTOMER's agent, not the deployment's.
func TestASendResolvesTheSendersOwnAgent(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel"}
	h := newRCSSendHarness(t, carrier)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	senderID, templateID, _ := h.agentForSend(tenant, "Acme Orders", "acme_airtel_agent")

	res := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": senderID.String(), "templateId": templateID.String(),
		"to": "9876543210", "body": "Hi Priya, your order A-1 shipped.",
		"variables": map[string]string{"first_name": "Priya", "order_id": "A-1"},
	})
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", res.Code, res.Body)
	}
	if len(carrier.sawSubmissions) != 1 {
		t.Fatalf("carrier saw %d submissions, want 1", len(carrier.sawSubmissions))
	}
	if got := carrier.sawSubmissions[0].AgentID; got != "acme_airtel_agent" {
		t.Errorf("AgentID = %q, want the carrier id issued for THIS tenant's agent", got)
	}
}

// THE ONE THAT MATTERS.
//
// A tenant that owns an agent must never send under the deployment's shared
// one. The dangerous case is not "no agent at all" — it is an agent that exists
// and cannot be resolved: a launch still pending, a launch on a different
// carrier than the route chose, an agent suspended since registration. Falling
// back there would put this customer's message on a handset under the shared
// brand, and the send would report success.
func TestATenantWithAnAgentNeverFallsBackToTheSharedOne(t *testing.T) {
	cases := []struct {
		name string
		bend func(h *harness, acct account, agentID uuid.UUID)
	}{
		{"launch still pending", func(h *harness, _ account, agentID uuid.UUID) {
			if _, err := h.admin.Exec(context.Background(),
				`UPDATE rcs_agent_carrier_launches SET status = 'pending' WHERE agent_id = $1`,
				agentID); err != nil {
				h.t.Fatalf("bend: %v", err)
			}
		}},
		{"launched on another carrier", func(h *harness, _ account, agentID uuid.UUID) {
			if _, err := h.admin.Exec(context.Background(),
				`UPDATE rcs_agent_carrier_launches SET carrier = 'VI' WHERE agent_id = $1`,
				agentID); err != nil {
				h.t.Fatalf("bend: %v", err)
			}
		}},
		{"agent suspended after registration", func(h *harness, _ account, agentID uuid.UUID) {
			if _, err := h.admin.Exec(context.Background(),
				`UPDATE rcs_agents SET status = 'suspended' WHERE id = $1`, agentID); err != nil {
				h.t.Fatalf("bend: %v", err)
			}
		}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			carrier := &stubRegistrar{vendor: "airtel"}
			h := newRCSSendHarness(t, carrier)
			tenant := h.newAccount("owner")
			h.fundWallet(tenant)

			senderID, templateID, _ := h.agentForSend(tenant, "Acme Orders", "acme_airtel_agent")
			var agentID uuid.UUID
			if err := h.admin.QueryRow(context.Background(),
				`SELECT rcs_agent_id FROM sender_ids WHERE id = $1`,
				senderID).Scan(&agentID); err != nil {
				h.t.Fatalf("read sender agent: %v", err)
			}

			testCase.bend(h, tenant, agentID)

			res := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
				"senderId": senderID.String(), "templateId": templateID.String(),
				"to": "9876543210", "body": "Hi Priya, your order A-1 shipped.",
				"variables": map[string]string{"first_name": "Priya", "order_id": "A-1"},
			})

			// Refused at the gate, so nothing was charged and the carrier was
			// never called. The failing alternative is not an error — it is a
			// 202 and a delivered message under the wrong company's name.
			if len(carrier.sawSubmissions) != 0 {
				t.Fatalf("the carrier was called with agent %q — this tenant owns agent %s",
					carrier.sawSubmissions[0].AgentID, agentID)
			}
			var sent gen.SendMessageResult
			res.decode(t, &sent)
			if sent.Status != "rejected" {
				t.Errorf("status = %q, want rejected; body = %s", sent.Status, res.Body)
			}
			if sent.ErrorCode == nil || *sent.ErrorCode != "rcs_agent_not_resolved" {
				t.Errorf("errorCode = %v, want rcs_agent_not_resolved; body = %s",
					sent.ErrorCode, res.Body)
			}
			if sent.CostMinor != 0 {
				t.Errorf("costMinor = %d, want nothing charged", sent.CostMinor)
			}
		})
	}
}

// The isolation proof our counterparts could not run from outside, because
// every seeded account on the deployment is the same tenant.
//
// Two tenants, two agents, the same carrier, and one inbound event. The
// carrier's agent id is the only thing on that event that names anyone, so the
// question "whose message is this" has exactly one correct answer and one
// catastrophic one.
func TestAnInboundEventReachesOnlyTheTenantWhoseAgentItNames(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	acme := h.newAccount("owner")
	globex := h.newAccount("owner")
	if acme.TenantID == globex.TenantID {
		t.Fatal("both accounts are the same tenant — this test would prove nothing")
	}
	h.approveRegistration(acme, "IN")
	h.approveRegistration(globex, "IN")

	acmeAgent := uuid.MustParse(h.createAgent(acme, "Acme Orders").Id)
	globexAgent := uuid.MustParse(h.createAgent(globex, "Globex Alerts").Id)
	h.launchAgentOnCarrier(acme, acmeAgent, "AIRTEL", "acme_airtel_agent")
	h.launchAgentOnCarrier(globex, globexAgent, "AIRTEL", "globex_airtel_agent")

	tenantID, agentID, err := store.TenantForCarrierAgent(ctx, h.operatorPool,
		"AIRTEL", "globex_airtel_agent")
	if err != nil {
		t.Fatalf("attribute: %v", err)
	}
	if tenantID != globex.TenantID || agentID != globexAgent {
		t.Fatalf("attributed to tenant %s agent %s, want %s / %s — "+
			"an inbound message just reached the wrong customer",
			tenantID, agentID, globex.TenantID, globexAgent)
	}

	// A carrier id we hold no launch for is not-found, never a nearest match.
	if _, _, err := store.TenantForCarrierAgent(ctx, h.operatorPool,
		"AIRTEL", "someone_elses_agent"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown carrier agent = %v, want ErrNotFound", err)
	}

	// The same id on a DIFFERENT carrier is a different agent. Airtel and Vi
	// issue ids from separate namespaces, so the carrier is half of the key.
	if _, _, err := store.TenantForCarrierAgent(ctx, h.operatorPool,
		"VI", "globex_airtel_agent"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("same id on another carrier = %v, want ErrNotFound", err)
	}
}

// The index behind the attribution above, asserted directly.
//
// TenantForCarrierAgent answers with one row because the database cannot hold
// two. Without rcs_launch_carrier_identity this would insert happily, the query
// would return whichever row it felt like, and two customers would receive each
// other's inbound messages with nothing anywhere to show it.
func TestTwoTenantsCannotShareOneCarrierAgentID(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	acme := h.newAccount("owner")
	globex := h.newAccount("owner")
	h.approveRegistration(acme, "IN")
	h.approveRegistration(globex, "IN")
	acmeAgent := uuid.MustParse(h.createAgent(acme, "Acme Orders").Id)
	globexAgent := uuid.MustParse(h.createAgent(globex, "Globex Alerts").Id)

	h.launchAgentOnCarrier(acme, acmeAgent, "AIRTEL", "shared_airtel_agent")

	_, err := h.admin.Exec(ctx, `
		INSERT INTO rcs_agent_carrier_launches
		    (agent_id, tenant_id, carrier, status, carrier_agent_id, submitted_at)
		VALUES ($1, $2, 'AIRTEL', 'approved', 'shared_airtel_agent', now())`,
		globexAgent, globex.TenantID)
	if err == nil {
		t.Fatal("two tenants took the same carrier agent id — " +
			"each would now receive the other's inbound RCS")
	}
	if !strings.Contains(err.Error(), "rcs_launch_carrier_identity") {
		t.Errorf("refused by %v, want the carrier-identity index", err)
	}
}

// rcsAgentId on sender registration.
//
// This is the change nothing on this side would have reported. A brand-new
// OPTIONAL field on a REQUEST body is the silent direction of a contract
// change: make generate adds it to the struct, no existing code has to read it,
// the build stays green, and every registration quietly drops the agent the
// customer picked. The symptom arrives weeks later as "why is our brand not
// showing on the handset".
func TestRegisteringASenderKeepsTheAgentItNames(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	h.approveRegistration(acct, "IN")

	agentID := uuid.MustParse(h.createAgent(acct, "Acme Orders").Id)
	approveAgentVerification(h, agentID)

	res := h.do(http.MethodPost, "/v1/sender-ids", acct.Token, map[string]any{
		"header": "ACMERT", "channel": "RCS", "country": "IN",
		"rcsAgentId": agentID.String(),
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("register = %d: %s", res.Code, res.Body)
	}
	var sender gen.SenderId
	res.decode(t, &sender)
	if sender.RcsAgentId == nil || *sender.RcsAgentId != agentID {
		t.Fatalf("rcsAgentId = %v, want %s — the field arrived and was dropped",
			sender.RcsAgentId, agentID)
	}

	// Read back, not just echoed. A handler can return what it was given and
	// still never have written it.
	var stored *uuid.UUID
	if err := h.admin.QueryRow(context.Background(),
		`SELECT rcs_agent_id FROM sender_ids WHERE id = $1`, sender.Id).Scan(&stored); err != nil {
		h.t.Fatalf("read back: %v", err)
	}
	if stored == nil || *stored != agentID {
		t.Errorf("stored rcs_agent_id = %v, want %s", stored, agentID)
	}
}

// The refusals, each naming the thing the customer has to change.
func TestASenderCannotTakeAnAgentItIsNotEntitledTo(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	stranger := h.newAccount("owner")
	h.approveRegistration(acct, "IN")
	h.approveRegistration(stranger, "IN")

	approved := uuid.MustParse(h.createAgent(acct, "Acme Orders").Id)
	approveAgentVerification(h, approved)
	unverified := uuid.MustParse(h.createAgent(acct, "Acme Draft").Id)
	theirs := uuid.MustParse(h.createAgent(stranger, "Globex Alerts").Id)
	approveAgentVerification(h, theirs)

	cases := []struct {
		name    string
		channel string
		agent   uuid.UUID
		says    string
	}{
		{"an agent on an SMS sender", "SMS", approved, "only be attached to an RCS sender"},
		{"an agent that has not been verified", "RCS", unverified, "brand verification"},
		{"another tenant's agent", "RCS", theirs, "No such RCS agent"},
	}

	for i, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			res := h.do(http.MethodPost, "/v1/sender-ids", acct.Token, map[string]any{
				"header": fmt.Sprintf("ACME%02d", i), "channel": testCase.channel,
				"country": "IN", "rcsAgentId": testCase.agent.String(),
			})
			if res.Code != http.StatusUnprocessableEntity {
				t.Fatalf("register = %d, want 422: %s", res.Code, res.Body)
			}
			if !strings.Contains(string(res.Body), testCase.says) {
				t.Errorf("refusal does not say %q: %s", testCase.says, res.Body)
			}
		})
	}
}

// verifiedAgent is an agent this tenant owns that a sender may name.
func (h *harness) verifiedAgent(acct account) uuid.UUID {
	h.t.Helper()
	h.approveRegistration(acct, "IN")
	agentID := uuid.MustParse(h.createAgent(acct, "Agent "+uuid.NewString()[:6]).Id)
	approveAgentVerification(h, agentID)
	return agentID
}

// approveAgentVerification moves an agent to the state a sender may name it
// from. Written directly: the route that does this belongs to the operator, and
// what is under test here is the sender, not the review.
func approveAgentVerification(h *harness, agentID uuid.UUID) {
	h.t.Helper()
	if _, err := h.admin.Exec(context.Background(), `
		UPDATE rcs_agents
		   SET status = 'verification_approved', verification_status = 'approved',
		       verification_reviewed_at = now()
		 WHERE id = $1`, agentID); err != nil {
		h.t.Fatalf("approve agent verification: %v", err)
	}
}
