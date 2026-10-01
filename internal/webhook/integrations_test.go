package webhook

import (
	"encoding/json"
	"testing"
	"time"
)

var delivered = []byte(`{"event":"message.delivered","data":{"messageId":"m-1",
	"msisdn":"+919810000001","status":"delivered","segments":1,"errorCode":null,
	"nested":{"carrier":"VIDEOCON","hops":[1,2]}}}`)

var noon = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("not json: %v: %s", err, raw)
	}
	return out
}

func TestCleverTapShape(t *testing.T) {
	raw, err := Transform(IntegrationCleverTap, delivered, noon)
	if err != nil {
		t.Fatal(err)
	}
	row := decode(t, raw)["d"].([]any)[0].(map[string]any)
	data := row["evtData"].(map[string]any)
	if row["identity"] != "+919810000001" || row["type"] != "event" ||
		row["evtName"] != "Relay Message Delivered" || row["ts"] != float64(noon.Unix()) {
		t.Errorf("row = %v", row)
	}
	if data["status"] != "delivered" || data["nested_carrier"] != "VIDEOCON" ||
		data["nested_hops"] != "[1,2]" {
		t.Errorf("evtData = %v, want flat scalars with arrays as text", data)
	}
	if _, present := data["errorCode"]; present {
		t.Errorf("a null was sent: %v", data)
	}
}

func TestWebEngageShape(t *testing.T) {
	raw, err := Transform(IntegrationWebEngage, delivered, noon)
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, raw)
	if got["userId"] != "+919810000001" || got["eventName"] != "Relay Message Delivered" ||
		got["eventTime"] != "2026-10-02T12:00:00+0000" {
		t.Errorf("event = %v", got)
	}
	if got["eventData"].(map[string]any)["messageId"] != "m-1" {
		t.Errorf("eventData = %v", got["eventData"])
	}
}

func TestMoEngageShape(t *testing.T) {
	raw, err := Transform(IntegrationMoEngage, delivered, noon)
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, raw)
	action := got["actions"].([]any)[0].(map[string]any)
	if got["type"] != "event" || got["customer_id"] != "+919810000001" ||
		action["action"] != "Relay Message Delivered" || action["current_time"] != float64(noon.Unix()) {
		t.Errorf("event = %v", got)
	}
}

func TestAnEventWithNoPhoneIsAboutTheMessage(t *testing.T) {
	raw, err := Transform(IntegrationCleverTap, []byte(`{"event":"wallet.low_balance","data":{"messageId":"x"}}`), noon)
	if err != nil {
		t.Fatal(err)
	}
	if id := decode(t, raw)["d"].([]any)[0].(map[string]any)["identity"]; id != "x" {
		t.Errorf("identity = %v", id)
	}
}

func TestWhatIsNotAnEventOrAnIntegrationIsRefused(t *testing.T) {
	for name, payload := range map[string][]byte{
		"not json": []byte("nope"), "no event": []byte(`{"data":{}}`), "empty": nil,
	} {
		if _, err := Transform(IntegrationCleverTap, payload, noon); err == nil {
			t.Errorf("%s was transformed", name)
		}
	}
	if _, err := Transform("salesforce", delivered, noon); err == nil {
		t.Error("an unknown integration was accepted")
	}
}

func TestReservedHeaders(t *testing.T) {
	for _, name := range []string{"X-Relay-Signature", "x-relay-event", "Host", "Content-Length",
		"content-type", "Transfer-Encoding", "Connection"} {
		if !ReservedHeader(name) {
			t.Errorf("%s may be overridden", name)
		}
	}
	for _, name := range []string{"Authorization", "X-CleverTap-Passcode", "X-CleverTap-Account-Id"} {
		if ReservedHeader(name) {
			t.Errorf("%s is refused but a vendor needs it", name)
		}
	}
}
