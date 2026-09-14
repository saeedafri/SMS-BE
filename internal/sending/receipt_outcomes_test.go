package sending_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/store"
)

// scriptedCarrier answers every submission the same way. A nil receipt means
// no receipt at all: the message never left a wait.
type scriptedCarrier struct {
	connector.Connector
	answer func(connector.Submission) *connector.Receipt
}

func (c scriptedCarrier) Submit(_ context.Context, submissions []connector.Submission) ([]connector.Receipt, error) {
	var out []connector.Receipt
	for _, s := range submissions {
		if r := c.answer(s); r != nil {
			out = append(out, *r)
		}
	}
	return out, nil
}

func answering(code string, accepted bool) func(connector.Submission) *connector.Receipt {
	return func(s connector.Submission) *connector.Receipt {
		return &connector.Receipt{MessageID: s.MessageID, Accepted: accepted, ErrorCode: code}
	}
}

// sendOneEachWay sends one message through the single-send path, the
// coalescer and a campaign, and returns each message's id.
func (f *fixture) sendOneEachWay(carrier connector.Connector) map[string]uuid.UUID {
	f.t.Helper()
	f.service.Connector = carrier
	ids := map[string]uuid.UUID{}

	direct, err := f.send("9876543210", "Your order has shipped.")
	if err != nil && direct.MessageID == uuid.Nil {
		f.t.Fatalf("direct send: %v", err)
	}
	ids["direct"] = direct.MessageID

	f.withCoalescer()
	results, errs := f.sendAll(f.requests(1, "Your order has shipped."))
	if errs[0] != nil && results[0].MessageID == uuid.Nil {
		f.t.Fatalf("coalesced send: %v", errs[0])
	}
	ids["coalesced"] = results[0].MessageID
	f.service.Coalescer = nil

	listID, _ := f.seedList("Receipt outcomes", 1)
	campaign := f.seedCampaign(f.templateID, listID, "sending", 1)
	if _, _, err := f.service.LaunchCampaign(context.Background(), f.identity, campaign); err != nil {
		f.t.Fatalf("campaign: %v", err)
	}
	var campaignMessage uuid.UUID
	if err := f.service.ClickHouse.QueryRow(context.Background(),
		`SELECT id FROM messages WHERE tenant_id = ? AND campaign_id = ? LIMIT 1`,
		f.identity.TenantID, campaign.ID).Scan(&campaignMessage); err != nil {
		f.t.Fatalf("campaign message: %v", err)
	}
	ids["campaign"] = campaignMessage
	return ids
}

func (f *fixture) message(id uuid.UUID) store.MessageRecord {
	f.t.Helper()
	record, err := store.GetMessage(context.Background(), f.service.ClickHouse, f.identity.TenantID, id)
	if err != nil {
		f.t.Fatalf("read message %s: %v", id, err)
	}
	return record
}

// Ask 35 §2.8. NO_OPERATOR_BIND and DLT_IDS_MISSING are decided by us before any
// operator sees the message. That is a refusal: status rejected, a lowercase
// refusal code, no error class, cost 0 and the hold released — on every path.
func TestOurRefusalsAreRecordedAsRefusals(t *testing.T) {
	for code, want := range map[string]string{
		"NO_OPERATOR_BIND": "no_operator_bind",
		"DLT_IDS_MISSING":  "dlt_ids_missing",
	} {
		t.Run(code, func(t *testing.T) {
			f := newFixture(t)
			before := f.balance()
			for path, id := range f.sendOneEachWay(scriptedCarrier{answer: answering(code, false)}) {
				record := f.message(id)
				if record.Status != "rejected" || record.ErrorCode == nil || *record.ErrorCode != want ||
					record.ErrorClass != nil && *record.ErrorClass != "" || record.CostMinor != 0 {
					t.Errorf("%s: status=%s code=%v class=%v cost=%d, want rejected/%s/no class/0",
						path, record.Status, deref(record.ErrorCode), deref(record.ErrorClass),
						record.CostMinor, want)
				}
			}
			if after := f.balance(); after != before {
				t.Errorf("balance %d after three refusals, want %d", after, before)
			}
		})
	}
}

// Ask 35 §2.2. No submit_sm_resp means we do not know whether the operator took
// it. The message stays sent and keeps its hold; the reconciler decides later.
func TestATimedOutSubmitKeepsItsHoldAndStaysSent(t *testing.T) {
	f := newFixture(t)
	before := f.balance()
	for path, id := range f.sendOneEachWay(scriptedCarrier{answer: answering("SUBMIT_TIMEOUT", false)}) {
		record := f.message(id)
		if record.Status != "submitted" || record.CostMinor != 12 {
			t.Errorf("%s: status=%s cost=%d code=%v, want submitted holding 12",
				path, record.Status, record.CostMinor, deref(record.ErrorCode))
		}
	}
	if spent := before - f.balance(); spent != 36 {
		t.Errorf("spent %d on three timed-out submits, want the three holds kept (36)", spent)
	}
}

// Ask 35 §2.2 addition. A message that never left the wait has no receipt. It
// stays queued with its hold, to be sent when the bind has room.
func TestAWaitThatNeverSentLeavesTheMessagePending(t *testing.T) {
	f := newFixture(t)
	before := f.balance()
	for path, id := range f.sendOneEachWay(scriptedCarrier{
		answer: func(connector.Submission) *connector.Receipt { return nil }}) {
		record := f.message(id)
		if record.Status != "queued" || record.CostMinor != 12 {
			t.Errorf("%s: status=%s cost=%d code=%v, want queued holding 12",
				path, record.Status, record.CostMinor, deref(record.ErrorCode))
		}
	}
	if spent := before - f.balance(); spent != 36 {
		t.Errorf("spent %d on three unsent messages, want the three holds kept (36)", spent)
	}
}

func deref(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}
