package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// Ask 32: RCS scoped to the customer's own agent. The carrier is STATED on a
// registration, and the agent is DERIVED from the template's sender; a
// capability check names its agent explicitly, because there is no sender in
// that request to derive one from.

// The two new required REQUEST fields are the direction nothing in Go catches.
// Both are non-pointer in the generated body, so an omitted key decodes to a
// zero value and a handler that never reads the field still compiles and still
// passes every test that sends it. These are the tests that would go red.

func TestARegistrationThatNamesNoCarrierIsRefused(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel", issued: "should-never-issue"}
	h := newCarrierHarness(t, carrier)
	tenant := h.newAccount("owner")
	templateID := h.rcsTemplate(tenant, "No vendor", []string{"first_name"}, "UTILITY")
	path := "/v1/templates/" + templateID.String() + "/carrier-registration"

	cases := []struct {
		name string
		body string
		says string
	}{
		// Refused by the binder before the handler runs, in its own words.
		{"no body at all", ``, "decode"},
		// These three reach the handler. Each must be refused for NAMING NO
		// CARRIER, not by some later check that happens to trip over an empty
		// vendor: with the vendor check removed, "" still fails the launch lookup
		// further down and answers 422 — the right status for the wrong reason,
		// and a test asserting only the status would never notice the guard went.
		{"an empty object", `{}`, "vendor must be airtel or vi"},
		{"a code but no carrier", `{"carrierTemplateId":"pasted-from-a-portal"}`, "vendor must be airtel or vi"},
		{"a carrier we hold no integration for", `{"vendor":"jio"}`, "vendor must be airtel or vi"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			res := h.doRaw(http.MethodPost, path, tenant.Token, "application/json",
				[]byte(testCase.body))
			if res.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", res.Code, res.Body)
			}
			if !strings.Contains(string(res.Body), testCase.says) {
				t.Errorf("refused, but not for naming no carrier — want %q in: %s",
					testCase.says, res.Body)
			}
		})
	}

	if carrier.calls != 0 {
		t.Errorf("the carrier was called %d times for requests that named no carrier", carrier.calls)
	}
	// The guess this field retired would have filled vendor in from the
	// deployment. Nothing may be written at all.
	vendor, code, _, _ := h.templateCarrierState(templateID)
	if vendor != "" || code != "" {
		t.Errorf("stored vendor %q / code %q from a request that named no carrier", vendor, code)
	}
}

func TestACapabilityCheckThatNamesNoAgentIsRefused(t *testing.T) {
	h := newHarness(t)
	carrier := &stubCarrier{vendor: "airtel", reachable: map[string]bool{"+919820000001": true}}
	h.server.RCSCarrier = carrier
	acct := h.newAccount("admin")

	res := h.do(http.MethodPost, "/v1/rcs/capabilities", acct.Token,
		map[string]any{"msisdns": []string{"+919820000001"}})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", res.Code, res.Body)
	}
	if !strings.Contains(string(res.Body), "rcsAgentId") {
		t.Errorf("refusal does not name the missing field: %s", res.Body)
	}
	// Answering anyway, under no agent, is the shared-agent answer this field
	// exists to end.
	if carrier.singleCalls+carrier.bulkCalls != 0 {
		t.Error("the carrier was asked about handsets for a request that named no agent")
	}
}

// Another tenant's agent is the same answer as one that does not exist: a
// distinct refusal would confirm that a given id is somebody's.
func TestACapabilityCheckForAnotherTenantsAgentIsNotFound(t *testing.T) {
	h := newHarness(t)
	carrier := &stubCarrier{vendor: "airtel"}
	h.server.RCSCarrier = carrier
	owner := h.newAccount("admin")
	stranger := h.newAccount("admin")
	theirs, _ := h.launchedAgent(owner)

	for name, agentID := range map[string]uuid.UUID{
		"another tenant's agent":      theirs,
		"an agent that never existed": uuid.New(),
	} {
		t.Run(name, func(t *testing.T) {
			res := h.do(http.MethodPost, "/v1/rcs/capabilities", stranger.Token, map[string]any{
				"rcsAgentId": agentID.String(), "msisdns": []string{"+919820000001"},
			})
			if res.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", res.Code, res.Body)
			}
		})
	}
	if carrier.singleCalls+carrier.bulkCalls != 0 {
		t.Error("the carrier was called with an agent the caller does not own")
	}
}

// Refused, rather than answered with reachableCount 0 — a zero reads as a fact
// about the handsets, and the truth is a fact about the agent.
func TestAnAgentWithNoLaunchOnTheCheckingCarrierIsRefused(t *testing.T) {
	h := newHarness(t)
	carrier := &stubCarrier{vendor: "airtel"}
	h.server.RCSCarrier = carrier
	acct := h.newAccount("admin")
	h.approveRegistration(acct, "IN")
	agentID := uuid.MustParse(h.createAgent(acct, "Vi only").Id)
	// Live on Vi. This deployment checks reach through Airtel.
	h.launchAgentOnCarrier(acct, agentID, "VI", "vi-only-"+uuid.NewString()[:8])

	res := h.do(http.MethodPost, "/v1/rcs/capabilities", acct.Token, map[string]any{
		"rcsAgentId": agentID.String(), "msisdns": []string{"+919820000001"},
	})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", res.Code, res.Body)
	}
	if !strings.Contains(string(res.Body), "AIRTEL") {
		t.Errorf("refusal does not name the carrier it checked: %s", res.Body)
	}
	if carrier.singleCalls+carrier.bulkCalls != 0 {
		t.Error("the carrier was called for an agent it has never admitted")
	}
}

// The agent is the template's OWN sender's — not the tenant's first agent, not
// its newest, not one the request could name. A tenant with two agents is the
// case that tells those apart.
func TestARegistrationIsMadeUnderTheTemplatesOwnSendersAgent(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel", issued: "scoped-" + uuid.NewString()[:8]}
	h := newCarrierHarness(t, carrier)
	tenant := h.newAccount("owner")
	templateID := h.rcsTemplate(tenant, "Scoped", []string{"first_name"}, "UTILITY")

	// A second agent on the same tenant, created AFTER the template's, launched
	// on the same carrier. A handler that picked "the tenant's agent" would have
	// a fifty-fifty chance of passing without this.
	decoy := uuid.MustParse(h.createAgent(tenant, "Decoy agent").Id)
	h.launchAgentOnCarrier(tenant, decoy, "AIRTEL", "decoy-"+uuid.NewString()[:8])

	var want string
	if err := h.admin.QueryRow(context.Background(), `
		SELECT l.carrier_agent_id
		  FROM templates t
		  JOIN sender_ids s ON s.id = t.sender_id
		  JOIN rcs_agent_carrier_launches l ON l.agent_id = s.rcs_agent_id
		 WHERE t.id = $1 AND l.carrier = 'AIRTEL'`, templateID).Scan(&want); err != nil {
		t.Fatalf("read the sender's agent: %v", err)
	}

	res := h.do(http.MethodPost, "/v1/templates/"+templateID.String()+"/carrier-registration",
		tenant.Token, map[string]any{"vendor": "airtel"})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body)
	}
	if carrier.sawAgent != want {
		t.Errorf("registered under agent %q, want %q — the template's own sender's", carrier.sawAgent, want)
	}
}

func TestARegistrationWithNoAgentToScopeItToIsRefused(t *testing.T) {
	cases := []struct {
		name string
		bend string
		says string
		body map[string]any
	}{
		{
			name: "the sender has no agent",
			bend: `UPDATE sender_ids SET rcs_agent_id = NULL
			        WHERE id = (SELECT sender_id FROM templates WHERE id = $1)`,
			says: "sender has no RCS agent",
			body: map[string]any{"vendor": "airtel"},
		},
		{
			name: "the agent is not launched on the named carrier",
			bend: `DELETE FROM rcs_agent_carrier_launches
			        WHERE carrier = 'AIRTEL' AND agent_id = (
			          SELECT s.rcs_agent_id FROM templates t
			            JOIN sender_ids s ON s.id = t.sender_id WHERE t.id = $1)`,
			says: "no approved launch on AIRTEL",
			body: map[string]any{"vendor": "airtel", "carrierTemplateId": "attach-" + uuid.NewString()[:8]},
		},
		{
			name: "the agent was suspended",
			bend: `UPDATE rcs_agents SET status = 'suspended'
			        WHERE id = (SELECT s.rcs_agent_id FROM templates t
			                      JOIN sender_ids s ON s.id = t.sender_id WHERE t.id = $1)`,
			says: "no approved launch",
			body: map[string]any{"vendor": "airtel"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			carrier := &stubRegistrar{vendor: "airtel", issued: "never"}
			h := newCarrierHarness(t, carrier)
			tenant := h.newAccount("owner")
			templateID := h.rcsTemplate(tenant, "Unscoped", []string{"first_name"}, "UTILITY")
			if _, err := h.admin.Exec(context.Background(), testCase.bend, templateID); err != nil {
				t.Fatalf("bend: %v", err)
			}

			res := h.do(http.MethodPost, "/v1/templates/"+templateID.String()+"/carrier-registration",
				tenant.Token, testCase.body)
			if res.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", res.Code, res.Body)
			}
			if !strings.Contains(string(res.Body), testCase.says) {
				t.Errorf("refusal does not say %q: %s", testCase.says, res.Body)
			}
			if carrier.calls != 0 {
				t.Error("the carrier was asked to register a template no send could use")
			}
			if _, code, _, _ := h.templateCarrierState(templateID); code != "" {
				t.Errorf("stored carrier code %q for a registration that was refused", code)
			}
		})
	}
}

// The stated vendor is what is stored — including when it is not the carrier
// this deployment is configured for, and including when no carrier is
// configured at all. A code pasted from a portal already exists at that carrier.
func TestAnAttachedCodeKeepsTheCarrierItWasStatedFrom(t *testing.T) {
	h := newHarness(t) // no RCS carrier configured
	tenant := h.newAccount("owner")
	templateID := h.rcsTemplate(tenant, "Portal code", []string{"first_name"}, "MARKETING")
	code := "vi-portal-" + uuid.NewString()[:8]

	res := h.do(http.MethodPost, "/v1/templates/"+templateID.String()+"/carrier-registration",
		tenant.Token, map[string]any{"vendor": "vi", "carrierTemplateId": code})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body)
	}
	var registration gen.CarrierTemplateRegistration
	res.decode(t, &registration)
	if registration.Vendor == nil || *registration.Vendor != "vi" {
		t.Errorf("vendor = %v, want vi", registration.Vendor)
	}
	// Read back rather than trusted: the column is half of the unique index
	// that keeps two tenants' codes apart, and it must never be null.
	if vendor, stored, _, _ := h.templateCarrierState(templateID); vendor != "vi" || stored != code {
		t.Errorf("stored %q / %q, want vi / %s", vendor, stored, code)
	}
}

// Now that the vendor is stated rather than guessed from this deployment's own
// gateway, a code from one carrier's portal can sit on a deployment that sends
// through the other. Every send would quote Vi's template id to Airtel and come
// back "Template not found" at the gateway, after the hold. The gate refuses it
// first, as a template the carrying carrier has never been given.
func TestATemplateRegisteredWithOneCarrierIsNotSentThroughAnother(t *testing.T) {
	carrier := &stubRegistrar{vendor: "airtel"}
	h := newRCSSendHarness(t, carrier)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	senderID, templateID, _ := h.approvedRCSTemplate(tenant, "Wrong carrier", "tmpl-wrong")
	if _, err := h.admin.Exec(context.Background(),
		`UPDATE templates SET carrier_vendor = 'vi' WHERE id = $1`, templateID); err != nil {
		t.Fatalf("re-label template: %v", err)
	}

	res := h.do(http.MethodPost, "/v1/messages", tenant.Token, map[string]any{
		"senderId": senderID.String(), "templateId": templateID.String(),
		"to": "9876543210", "body": "Hi Priya, your order A-9 shipped.",
		"variables": map[string]string{"first_name": "Priya", "order_id": "A-9"},
	})
	var sent gen.SendMessageResult
	res.decode(t, &sent)
	if len(carrier.sawSubmissions) != 0 {
		t.Fatalf("Airtel was handed a template registered with Vi")
	}
	if sent.ErrorCode == nil || *sent.ErrorCode != "carrier_template_not_approved" {
		t.Errorf("errorCode = %v, want carrier_template_not_approved: %s", sent.ErrorCode, res.Body)
	}
	if sent.CostMinor != 0 {
		t.Errorf("costMinor = %d, want nothing charged", sent.CostMinor)
	}
}

// India only today, and derived: a country opens when one of its carriers has
// an adapter, not when someone edits a list.
func TestAnAgentCannotBeCreatedWhereNoCarrierIsIntegrated(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	// An approved entity in the refused country, so the refusal cannot be the
	// entity check wearing a different status code.
	h.approveRegistration(acct, "US")

	res := h.do(http.MethodPost, "/v1/rcs/agents", acct.Token, map[string]any{
		"displayName": "Acme US", "country": "US", "useCase": "TRANSACTIONAL",
	})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", res.Code, res.Body)
	}
	// Says where it IS available, not only where it is not.
	if !strings.Contains(string(res.Body), "can be created in IN") {
		t.Errorf("refusal does not say where RCS is available: %s", res.Body)
	}
}

func TestCreatingAnAgentRefusesAFieldTheContractDoesNotDeclare(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	h.approveRegistration(acct, "IN")

	res := h.do(http.MethodPost, "/v1/rcs/agents", acct.Token, map[string]any{
		"displayName": "Acme Orders", "country": "IN", "useCase": "TRANSACTIONAL",
		"primaryColor": "#1A3D7C",
	})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 — the same key is refused on PATCH: %s", res.Code, res.Body)
	}
	if !strings.Contains(string(res.Body), "primaryColor") {
		t.Errorf("refusal does not name the field: %s", res.Body)
	}
}

// "Last edited" moves when something was edited, and only then. A form that
// re-saves every field unchanged is the ordinary case, so the same-value patch
// matters more than the empty one.
func TestANoOpEditLeavesUpdatedAtAlone(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	h.approveRegistration(acct, "IN")
	agent := h.createAgent(acct, "Acme Orders")
	h.patchAgent(acct, agent.Id, `{"website":"https://acme.test"}`)
	before := h.agentUpdatedAt(agent.Id)

	// Far enough apart that now() cannot land on the same microsecond.
	time.Sleep(20 * time.Millisecond)
	for _, body := range []string{`{}`, `{"displayName":"Acme Orders","website":"https://acme.test"}`} {
		h.patchAgent(acct, agent.Id, body)
		if after := h.agentUpdatedAt(agent.Id); !after.Equal(before) {
			t.Errorf("PATCH %s moved updatedAt from %s to %s without changing anything",
				body, before, after)
		}
	}

	h.patchAgent(acct, agent.Id, `{"website":"https://acme.example"}`)
	if after := h.agentUpdatedAt(agent.Id); !after.After(before) {
		t.Errorf("a real edit left updatedAt at %s", after)
	}
}

func (h *harness) agentUpdatedAt(agentID string) time.Time {
	h.t.Helper()
	var updatedAt time.Time
	if err := h.admin.QueryRow(context.Background(),
		`SELECT updated_at FROM rcs_agents WHERE id = $1`, agentID).Scan(&updatedAt); err != nil {
		h.t.Fatalf("read updated_at: %v", err)
	}
	return updatedAt
}

// Decision 2: an RCS sender with no agent is a sender whose templates can never
// register and never send. Refused while the form is still on screen.
func TestAnRCSSenderCannotBeCreatedWithoutAnAgent(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPost, "/v1/sender-ids", acct.Token, map[string]any{
		"header": "NOAGNT", "channel": "RCS", "country": "IN",
	})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", res.Code, res.Body)
	}
	if !strings.Contains(string(res.Body), "needs an RCS agent") {
		t.Errorf("refusal does not say what is missing: %s", res.Body)
	}
}

// Decision 1: a sender's agent cannot be re-pointed. Registrations are derived
// from it, so moving it would strand every one of them. The PATCH never declared
// the field; this proves it is REFUSED rather than silently dropped, which
// would report a re-point that did not happen.
func TestASendersAgentCannotBeRepointed(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	original := h.verifiedAgent(acct)
	other := h.verifiedAgent(acct)

	created := h.do(http.MethodPost, "/v1/sender-ids", acct.Token, map[string]any{
		"header": "REPNTR", "channel": "RCS", "country": "IN", "rcsAgentId": original.String(),
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create sender = %d: %s", created.Code, created.Body)
	}
	var sender gen.SenderId
	created.decode(t, &sender)

	res := h.do(http.MethodPatch, "/v1/sender-ids/"+sender.Id.String(), acct.Token,
		map[string]any{"rcsAgentId": other.String()})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("re-point = %d, want 422: %s", res.Code, res.Body)
	}
	var stored uuid.UUID
	if err := h.admin.QueryRow(context.Background(),
		`SELECT rcs_agent_id FROM sender_ids WHERE id = $1`, sender.Id).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != original {
		t.Errorf("rcs_agent_id = %s after a refused re-point, want %s", stored, original)
	}
}
