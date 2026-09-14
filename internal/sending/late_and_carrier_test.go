package sending_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/store"
)

func (f *fixture) seedAcceptedMessage(carrier, ref string, version uint64) uuid.UUID {
	f.t.Helper()
	id, now := uuid.New(), time.Now().UTC()
	if err := store.InsertMessages(context.Background(), f.service.ClickHouse, []store.MessageRecord{{
		TenantID: f.identity.TenantID, ID: id, Channel: "SMS", Country: "IN",
		SenderHeader: "SENDCO", Msisdn: "919876543210", Status: "accepted", FraudFlag: "none",
		Segments: 1, CostMinor: 12, Currency: "INR", Carrier: carrier, CarrierRef: &ref,
		CreatedAt: now, UpdatedAt: now, Version: version,
	}}); err != nil {
		f.t.Fatalf("seed message: %v", err)
	}
	return id
}

// Ask 35 §2.1. Airtel and Jio number their messages independently, so both can
// issue 4471902. A receipt on the Airtel bind settles the Airtel message and
// leaves another tenant's Jio message untouched — even when the Jio row is the
// one written last, and including the other-base spelling of the same id.
func TestAReceiptSettlesOnlyItsOwnCarriersMessage(t *testing.T) {
	airtel, jio := newFixture(t), newFixture(t)
	airtelMessage := airtel.seedAcceptedMessage("AIRTEL", "4471902", 2)
	jioSame := jio.seedAcceptedMessage("JIO", "4471902", 3)
	jioHex := jio.seedAcceptedMessage("JIO", "443c5e", 3) // 4471902 in hex

	if err := airtel.service.SettleCarrierReport(context.Background(), connector.DeliveryReport{
		Carrier: "AIRTEL", CarrierRef: "4471902", Delivered: true}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if got := airtel.message(airtelMessage).Status; got != "delivered" {
		t.Errorf("the Airtel message is %s, want delivered", got)
	}
	for name, id := range map[string]uuid.UUID{"same id": jioSame, "hex spelling": jioHex} {
		if got := jio.message(id).Status; got != "accepted" {
			t.Errorf("Jio %s message is %s after an Airtel receipt, want untouched", name, got)
		}
	}
}

// Ask 35 §2.2. A late submit_sm_resp reaches a message the send path left
// pending: an acceptance records the reference, a refusal releases the hold.
func TestALateSubmitOutcomeIsApplied(t *testing.T) {
	f := newFixture(t)
	before := f.balance()
	for path, id := range f.sendOneEachWay(scriptedCarrier{answer: answering("SUBMIT_TIMEOUT", false)}) {
		accepted := path != "campaign"
		ref := ""
		if accepted {
			ref = "late-" + path
		}
		if err := f.service.ApplyLateSubmit(context.Background(), connector.LateSubmit{
			MessageID: id.String(), Carrier: "", Accepted: accepted, CarrierRef: ref,
			ErrorCode: map[bool]string{true: "", false: "0x00000045"}[accepted],
		}); err != nil {
			t.Fatalf("%s: apply late submit: %v", path, err)
		}
		record := f.message(id)
		switch {
		case accepted && (record.Status != "accepted" || record.CarrierRef == nil || *record.CarrierRef != ref):
			t.Errorf("%s: status=%s ref=%v, want accepted with %s", path, record.Status, deref(record.CarrierRef), ref)
		case !accepted && (record.Status != "carrier_rejected" || record.CostMinor != 0):
			t.Errorf("%s: status=%s cost=%d, want carrier_rejected at 0", path, record.Status, record.CostMinor)
		}
	}
	if spent := before - f.balance(); spent != 24 {
		t.Errorf("spent %d, want two accepted holds (24) and the refused one released", spent)
	}
}
