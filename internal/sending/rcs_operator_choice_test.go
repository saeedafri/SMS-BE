package sending_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"
)

// Several RCS operators at once: which one a message goes through, and what it
// carries when the operator holds no template of its own.

// namedGateway is an RCS operator that keeps what it was handed.
type namedGateway struct {
	vendor      string
	submissions []connector.Submission
}

func (g *namedGateway) Name() string   { return g.vendor }
func (g *namedGateway) Vendor() string { return g.vendor }

// Jio reviews the assistant rather than the message, so it holds no template;
// Airtel does hold one.
func (g *namedGateway) TemplatesReviewed() bool { return g.vendor != "jio" }

func (g *namedGateway) Health(context.Context) connector.Health {
	return connector.Health{Healthy: true}
}

func (g *namedGateway) Capability(context.Context, string, string) (connector.RCSCapability, error) {
	return connector.RCSCapability{}, nil
}

func (g *namedGateway) Reachable(context.Context, string, []string) ([]string, error) {
	return nil, nil
}

func (g *namedGateway) Submit(_ context.Context,
	submissions []connector.Submission) ([]connector.Receipt, error) {

	g.submissions = append(g.submissions, submissions...)
	receipts := make([]connector.Receipt, 0, len(submissions))
	for _, submission := range submissions {
		receipts = append(receipts, connector.Receipt{MessageID: submission.MessageID,
			Accepted: true, CarrierRef: "ref-" + submission.MessageID})
	}
	return receipts, nil
}

// twoOperators puts Jio and Airtel behind the router, as two enabled accounts
// would.
func (f *fixture) twoOperators(t *testing.T) (*namedGateway, *namedGateway) {
	t.Helper()
	jio, airtel := &namedGateway{vendor: "jio"}, &namedGateway{vendor: "airtel"}
	router := &connector.RCSRouter{}
	router.Sync([]connector.RCSAccount{
		{Carrier: "JIO", Version: "1", Build: func() (connector.RCSGateway, error) { return jio, nil }},
		{Carrier: "AIRTEL", Version: "1", Build: func() (connector.RCSGateway, error) { return airtel, nil }},
	})
	f.service.Carriers = connector.Registry{Default: f.service.Connector,
		ByChannel: map[string]connector.Connector{"RCS": router}}
	return jio, airtel
}

// launchOn adds an approved launch for a sender's agent on one more operator.
func (f *fixture) launchOn(senderID uuid.UUID, carrier string) {
	f.t.Helper()
	var agentID uuid.UUID
	if err := store.WithTenant(context.Background(), f.service.DB, f.identity.TenantID,
		func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(),
				`SELECT rcs_agent_id FROM sender_ids WHERE id = $1`, senderID).Scan(&agentID)
		}); err != nil {
		f.t.Fatalf("read agent: %v", err)
	}
	f.exec(`INSERT INTO rcs_agent_carrier_launches
	            (agent_id, tenant_id, carrier, status, carrier_agent_id, submitted_at)
	        VALUES ($1, $2, $3, 'approved', $4, now())
	        ON CONFLICT (agent_id, carrier) DO UPDATE SET status = 'approved'`,
		agentID, f.identity.TenantID, carrier, carrier+"-"+agentID.String())
}

func (f *fixture) sendRCS(senderID, templateID uuid.UUID, msisdn string) sending.SendResult {
	f.t.Helper()
	result, err := f.service.Send(context.Background(), f.identity, sending.SendRequest{
		SenderID: senderID, TemplateID: &templateID, Msisdn: msisdn,
		Variables: map[string]string{"first_name": "Priya"},
	})
	if err != nil {
		f.t.Fatalf("send: %v (%s)", err, result.FailureCode)
	}
	return result
}

// A brand reaches a handset only through an operator it is launched on, so the
// launch decides the operator — not whichever account happens to be first.
func TestAnRCSMessageGoesThroughAnOperatorItsAgentIsLaunchedOn(t *testing.T) {
	f := newFixture(t)
	jio, airtel := f.twoOperators(t)

	senderID := f.seedApprovedSender("RCSOP1", "RCS") // launched on AIRTEL
	templateID := f.seedCarrierApprovedRCSTemplate(senderID)
	result := f.sendRCS(senderID, templateID, "+919820000021")

	if len(airtel.submissions) != 1 || len(jio.submissions) != 0 {
		t.Fatalf("airtel got %d, jio got %d; want the operator the agent is launched on",
			len(airtel.submissions), len(jio.submissions))
	}
	if got := airtel.submissions[0].AgentID; got == "" || got[:6] != "airtel" {
		t.Errorf("agent id = %q, want Airtel's own id for this agent", got)
	}
	// The log has to name the operator that actually carried it, or the
	// deliverability report blames the wrong network.
	if carrier := f.messageCarrier(result.MessageID); carrier != "AIRTEL" {
		t.Errorf("message recorded carrier %q, want AIRTEL", carrier)
	}

	// Launched on Jio too, and Jio's corridor is cheaper: Jio takes it.
	f.launchOn(senderID, "JIO")
	f.seedRCSRoute("JIO", 1)
	f.seedRCSRoute("AIRTEL", 2)
	f.service.Hot = store.NewHotCache(0)

	second := f.sendRCS(senderID, templateID, "+919820000022")
	if len(jio.submissions) != 1 {
		t.Fatalf("jio got %d submissions, want the corridor's first operator",
			len(jio.submissions))
	}
	if carrier := f.messageCarrier(second.MessageID); carrier != "JIO" {
		t.Errorf("message recorded carrier %q, want JIO", carrier)
	}
}

// An operator with no account configured is not a silent fallback to another
// one: the message is refused, because the brand is not on that network.
func TestAMessageForAnOperatorWithNoAccountIsRefusedRatherThanRerouted(t *testing.T) {
	f := newFixture(t)
	jio := &namedGateway{vendor: "jio"}
	router := &connector.RCSRouter{}
	router.Sync([]connector.RCSAccount{{Carrier: "JIO", Version: "1",
		Build: func() (connector.RCSGateway, error) { return jio, nil }}})
	f.service.Carriers = connector.Registry{Default: f.service.Connector,
		ByChannel: map[string]connector.Connector{"RCS": router}}

	// Launched on Airtel only, and Airtel has no account here.
	senderID := f.seedApprovedSender("RCSOP2", "RCS")
	templateID := f.seedCarrierApprovedRCSTemplate(senderID)
	result, err := f.service.Send(context.Background(), f.identity, sending.SendRequest{
		SenderID: senderID, TemplateID: &templateID, Msisdn: "+919820000023",
		Variables: map[string]string{"first_name": "Priya"},
	})
	if err == nil || result.FailureCode != "rcs_agent_not_resolved" {
		t.Fatalf("send = %+v, %v; want refused rcs_agent_not_resolved", result, err)
	}
	if len(jio.submissions) != 0 {
		t.Error("the message was sent through an operator the agent is not launched on")
	}
}

// Jio and Google review the assistant, not the message: they hold no template
// and are handed the text itself. A campaign body is empty on RCS, so the
// template's own text is rendered from the same values a carrier-held template
// would have been filled with.
func TestAnOperatorThatHoldsNoTemplateIsSentTheRenderedText(t *testing.T) {
	f := newFixture(t)
	jio, _ := f.twoOperators(t)

	senderID := f.seedApprovedSender("RCSOP3", "RCS")
	f.launchOn(senderID, "JIO")
	f.seedRCSRoute("JIO", 1)
	templateID := f.seedCarrierApprovedRCSTemplate(senderID)
	listID := f.seedListWithNamedContacts(map[string]string{"919820000024": "Priya"})
	campaignID := f.seedRCSCampaign(senderID, templateID, listID)
	f.exec(`UPDATE campaigns SET recipients = 1 WHERE id = $1`, campaignID)

	campaign, err := store.GetCampaign(context.Background(), f.service.DB, f.identity, campaignID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.service.LaunchCampaign(context.Background(), f.identity, campaign); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if len(jio.submissions) != 1 {
		t.Fatalf("jio got %d submissions, want 1", len(jio.submissions))
	}
	if body := jio.submissions[0].Body; body != "Hi Priya, welcome." {
		t.Errorf("body = %q, want the template rendered for this contact", body)
	}
}

func (f *fixture) seedRCSRoute(carrier string, priority int) {
	f.t.Helper()
	id := uuid.New()
	// Priorities are unique per corridor, so this takes the number it wants.
	f.exec(`DELETE FROM routes WHERE country = 'IN' AND channel = 'RCS' AND priority = $1`, priority)
	if _, err := f.service.DB.Exec(context.Background(), `
		INSERT INTO routes (id, country, channel, carrier, label, priority,
		                    compliance_standing, cost_per_segment_minor, currency, status)
		VALUES ($1, 'IN', 'RCS', $2, $3, $4, 'registered', 45, 'INR', 'active')`,
		id, carrier, carrier+" RCS test", priority); err != nil {
		f.t.Fatalf("seed rcs route: %v", err)
	}
	f.t.Cleanup(func() {
		_, _ = f.service.DB.Exec(context.Background(), `DELETE FROM routes WHERE id = $1`, id)
	})
}
