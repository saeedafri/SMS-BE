package connector

import (
	"encoding/base64"
	"testing"
	"time"
)

// Payloads transcribed from the vendors' own sample sections rather than
// invented, so a parser that passes here parses what the carriers actually send.

func TestAirtelTemplateApprovalIsRecognised(t *testing.T) {
	event, err := ParseAirtelWebhook([]byte(`{
	  "messageId": "01kpz85rangx4j0sfwdrkjahtc",
	  "agentId": "airtel_iq_x_agent",
	  "messageType": "RCS_MESSAGE",
	  "templateId": "01kpz85rangx4j0sfwdrkjahtc",
	  "messageContent": {
	    "templateStatus": "APPROVED",
	    "templateName": "Sample Test Template 1",
	    "templateId": "01kpz85rangx4j0sfwdrkjahtc"
	  },
	  "eventType": "TEMPLATE_APPROVED",
	  "sendTime": "2026-04-24T09:58:40.558999671Z"
	}`))
	if err != nil {
		t.Fatalf("ParseAirtelWebhook: %v", err)
	}
	if event.Kind != RCSEventTemplate {
		t.Fatalf("Kind = %q, want template", event.Kind)
	}
	if event.CarrierTemplateID != "01kpz85rangx4j0sfwdrkjahtc" {
		t.Errorf("CarrierTemplateID = %q", event.CarrierTemplateID)
	}
	if event.TemplateStatus != RCSTemplateApproved {
		t.Errorf("TemplateStatus = %q, want approved", event.TemplateStatus)
	}
	if event.OccurredAt.Year() != 2026 {
		t.Errorf("OccurredAt = %v, want the carrier's own timestamp", event.OccurredAt)
	}
}

func TestAirtelTemplateRejectionCarriesTheReason(t *testing.T) {
	event, err := ParseAirtelWebhook([]byte(`{
	  "messageId": "01kq9cran603qxppvevgs7fn42",
	  "templateId": "01kq9cran603qxppvevgs7fn42",
	  "messageContent": {
	    "templateStatus": "REJECTED",
	    "templateId": "01kq9cran603qxppvevgs7fn42",
	    "rejectionReason": "The content is not appropriate."
	  },
	  "eventType": "TEMPLATE_REJECTED",
	  "sendTime": "2026-04-28T06:35:39.629656792Z"
	}`))
	if err != nil {
		t.Fatalf("ParseAirtelWebhook: %v", err)
	}
	if event.TemplateStatus != RCSTemplateRejected {
		t.Errorf("TemplateStatus = %q, want rejected", event.TemplateStatus)
	}
	// Without the reason a customer is told "rejected" and has to guess what to
	// change, which is exactly the opaque behaviour this product replaces.
	if event.RejectionReason != "The content is not appropriate." {
		t.Errorf("RejectionReason = %q", event.RejectionReason)
	}
}

func TestAirtelDeliveryCorrelatesOnTheCarriersOwnReference(t *testing.T) {
	event, err := ParseAirtelWebhook([]byte(`{
	  "messageId": "01kns4smzq7zewfbqy6t7zjd0k",
	  "msisdn": "+919820000002",
	  "msgStream": "OUTBOUND",
	  "templateId": "01kns4qtx79gs9q6zjznhmpy7y",
	  "eventType": "DELIVERED",
	  "sendTime": "2026-04-09T13:06:50.763386Z"
	}`))
	if err != nil {
		t.Fatalf("ParseAirtelWebhook: %v", err)
	}
	if event.Kind != RCSEventDelivery || !event.Delivered {
		t.Fatalf("event = %+v, want a delivery", event)
	}
	// messageId here is Airtel's messageRequestId, NOT a Relay id — nothing in
	// this payload is ours, which is the whole reason carrier_ref is indexed.
	if event.CarrierRef != "01kns4smzq7zewfbqy6t7zjd0k" {
		t.Errorf("CarrierRef = %q", event.CarrierRef)
	}
}

// A message cannot be read without being delivered, and carriers do skip the
// delivery event. Treating READ as anything less would leave the message in
// flight until the reconciler expired it and refunded a message that arrived.
func TestAReadReceiptCountsAsDelivered(t *testing.T) {
	for _, vendor := range []string{"airtel", "vi"} {
		var event RCSEvent
		var err error
		if vendor == "airtel" {
			event, err = ParseAirtelWebhook([]byte(`{"messageId":"m1","eventType":"READ"}`))
		} else {
			event, err = ParseViWebhook([]byte(`{"messageId":"m1","eventType":"READ"}`))
		}
		if err != nil {
			t.Fatalf("%s: %v", vendor, err)
		}
		if !event.Delivered {
			t.Errorf("%s: a read receipt did not count as delivered", vendor)
		}
	}
}

// Both carriers refund an expired message, so Relay must release its hold
// rather than charge for it.
func TestAnExpiredMessageIsAFailureNotADelivery(t *testing.T) {
	event, err := ParseAirtelWebhook([]byte(`{
	  "messageId": "01kvzgp5rm06043ha50ydec3y3",
	  "eventType": "TTL_EXPIRATION_REVOKED",
	  "sendTime": "2026-06-25T13:49:57.077405Z"
	}`))
	if err != nil {
		t.Fatalf("ParseAirtelWebhook: %v", err)
	}
	if event.Kind != RCSEventDelivery {
		t.Fatalf("Kind = %q, want delivery", event.Kind)
	}
	if event.Delivered {
		t.Error("an expired message was reported delivered")
	}
	if event.ErrorCode != "expired_before_delivery" {
		t.Errorf("ErrorCode = %q", event.ErrorCode)
	}
}

// Airtel validates at send time but reports the failure on the webhook, so
// without this a malformed template fill is indistinguishable from a network
// failure and nobody knows to go fix the variables.
func TestAirtelValidationFailureIsSeparableFromAnyOtherFailure(t *testing.T) {
	event, err := ParseAirtelWebhook([]byte(`{
	  "messageId": "01kt997fm9ms2gx54t2chrgqq7",
	  "eventType": "INTERNAL_ERROR",
	  "messageContent": null,
	  "error": { "errorMessage": "Values for all the variables not provided!!",
	             "errorType": "VALIDATION_ERROR" }
	}`))
	if err != nil {
		t.Fatalf("ParseAirtelWebhook: %v", err)
	}
	if event.ErrorCode != "carrier_validation_failed" {
		t.Errorf("ErrorCode = %q, want carrier_validation_failed", event.ErrorCode)
	}
}

func TestAirtelInboundCarriesThePostbackAndItsContext(t *testing.T) {
	event, err := ParseAirtelWebhook([]byte(`{
	  "messageId": "Mxa0-dYUObROCiPaBJisZr6A",
	  "msisdn": "+919820000002",
	  "msgStream": "INBOUND",
	  "messageContent": {
	    "postbackData": "user_clicked_yes",
	    "text": "Okay",
	    "type": "REPLY",
	    "contextMessageId": "01kva5hk3sazfqkxxrtsdnd2py"
	  },
	  "eventType": "RECEIVED"
	}`))
	if err != nil {
		t.Fatalf("ParseAirtelWebhook: %v", err)
	}
	if event.Kind != RCSEventInbound {
		t.Fatalf("Kind = %q, want inbound", event.Kind)
	}
	if event.Text != "Okay" || event.PostbackData != "user_clicked_yes" {
		t.Errorf("event = %+v", event)
	}
	// Without the context reference a suggestion tap is an orphan and the
	// conversation cannot be threaded back to what offered it.
	if event.ContextRef != "01kva5hk3sazfqkxxrtsdnd2py" {
		t.Errorf("ContextRef = %q", event.ContextRef)
	}
}

func TestEventsWithNoConsequenceAreIgnoredRatherThanFailing(t *testing.T) {
	for _, eventType := range []string{"SENT", "BILLING_CATEGORY_UPDATE", "SOMETHING_NEW_IN_2027"} {
		event, err := ParseAirtelWebhook([]byte(`{"messageId":"m","eventType":"` + eventType + `"}`))
		if err != nil {
			t.Fatalf("%s: %v", eventType, err)
		}
		if event.Kind != RCSEventIgnored {
			t.Errorf("%s → %q, want ignored", eventType, event.Kind)
		}
		// The carrier's own name survives so a new event type shows up in the
		// logs instead of vanishing.
		if event.Raw != eventType {
			t.Errorf("Raw = %q, want the carrier's own name", event.Raw)
		}
	}
}

func TestAPayloadWithNoEventTypeIsRejected(t *testing.T) {
	if _, err := ParseAirtelWebhook([]byte(`{"messageId":"m"}`)); err == nil {
		t.Fatal("a payload with no eventType was accepted")
	}
	if _, err := ParseAirtelWebhook([]byte(`not json`)); err == nil {
		t.Fatal("malformed JSON was accepted")
	}
}

// --- Vi ---

func TestViDeliveryArrivesInsideGooglesPubSubEnvelope(t *testing.T) {
	inner := `{
	  "senderPhoneNumber": "+914253136789",
	  "eventType": "DELIVERED",
	  "eventId": "fa2fe5a2-d9a9-4d83-87d3-302ae1014610",
	  "messageId": "57bed79e-55ba-46fe-b88a-2755aaee77fc",
	  "sendTime": "2026-08-24T15:01:23.045123456Z"
	}`
	envelope := `{"subscription":"projects/x/subscriptions/y","message":{"data":"` +
		base64.StdEncoding.EncodeToString([]byte(inner)) +
		`","attributes":{"business_id":"OsQ0GwNvUdLTV9Bd","type":"message"}}}`

	event, err := ParseViWebhook([]byte(envelope))
	if err != nil {
		t.Fatalf("ParseViWebhook: %v", err)
	}
	if event.Kind != RCSEventDelivery || !event.Delivered {
		t.Fatalf("event = %+v, want a delivery", event)
	}
	// Vi accepts a caller-supplied messageId, so this is Relay's own uuid
	// coming back — the opposite of Airtel, where nothing in the payload is
	// ours.
	if event.CarrierRef != "57bed79e-55ba-46fe-b88a-2755aaee77fc" {
		t.Errorf("CarrierRef = %q", event.CarrierRef)
	}
	if event.Msisdn != "+914253136789" {
		t.Errorf("Msisdn = %q", event.Msisdn)
	}
}

// Vi's own examples show the bare event as well as the envelope. Accepting only
// one of the two is a whole class of "works in the sample, fails in production".
func TestViAcceptsTheBareEventAsWellAsTheEnvelope(t *testing.T) {
	event, err := ParseViWebhook([]byte(
		`{"senderPhoneNumber":"+914253136789","eventType":"DELIVERED","messageId":"m-1"}`))
	if err != nil {
		t.Fatalf("ParseViWebhook: %v", err)
	}
	if !event.Delivered || event.CarrierRef != "m-1" {
		t.Errorf("event = %+v", event)
	}
}

func TestViURLSafeBase64IsAccepted(t *testing.T) {
	inner := `{"eventType":"DELIVERED","messageId":"m-1","text":"a+b/c?d"}`
	envelope := `{"message":{"data":"` +
		base64.URLEncoding.EncodeToString([]byte(inner)) + `"}}`
	if _, err := ParseViWebhook([]byte(envelope)); err != nil {
		t.Fatalf("a URL-safe payload was rejected: %v", err)
	}
}

func TestViThrottlingFailureIsRecognisedFromItsReason(t *testing.T) {
	event, err := ParseViWebhook([]byte(`{
	  "senderPhoneNumber": "+919686960276",
	  "eventType": "FAILED",
	  "reason": "Number of API requests allowed per second exceeded. Please retry",
	  "messageId": "e4d4529f-0336-4292-8a39-34964cad2bd3"
	}`))
	if err != nil {
		t.Fatalf("ParseViWebhook: %v", err)
	}
	if event.ErrorCode != "carrier_throttled" {
		t.Errorf("ErrorCode = %q, want carrier_throttled", event.ErrorCode)
	}
}

// A suggestion tap arrives with no eventType at all and would otherwise fall
// through to ignored — losing every button press in the product.
func TestViSuggestionResponseIsInboundDespiteHavingNoEventType(t *testing.T) {
	event, err := ParseViWebhook([]byte(`{
	  "suggestionResponse": {
	    "postbackData": "user_clicked_Reach_Us",
	    "text": "Reach Us",
	    "type": "ACTION"
	  },
	  "senderPhoneNumber": "+919986473361",
	  "messageId": "MxNsfgtg86T",
	  "sendTime": "2026-08-20T15:39:30.475341Z"
	}`))
	if err != nil {
		t.Fatalf("ParseViWebhook: %v", err)
	}
	if event.Kind != RCSEventInbound {
		t.Fatalf("Kind = %q, want inbound", event.Kind)
	}
	if event.PostbackData != "user_clicked_Reach_Us" || event.Text != "Reach Us" {
		t.Errorf("event = %+v", event)
	}
}

func TestViPlainInboundMessageIsInbound(t *testing.T) {
	event, err := ParseViWebhook([]byte(`{
	  "senderPhoneNumber": "+914253136789",
	  "messageId": "0a99d150-aae7-4247-aa07-a92cdaaf8ed3",
	  "sendTime": "2026-08-20T15:01:23.045123456Z",
	  "text": "Hello, world!"
	}`))
	if err != nil {
		t.Fatalf("ParseViWebhook: %v", err)
	}
	if event.Kind != RCSEventInbound || event.Text != "Hello, world!" {
		t.Errorf("event = %+v", event)
	}
}

func TestViTypingIndicatorIsIgnored(t *testing.T) {
	event, err := ParseViWebhook([]byte(
		`{"senderPhoneNumber":"+91","eventType":"IS_TYPING","eventId":"e1"}`))
	if err != nil {
		t.Fatalf("ParseViWebhook: %v", err)
	}
	if event.Kind != RCSEventIgnored {
		t.Errorf("Kind = %q, want ignored", event.Kind)
	}
}

// A zero timestamp sorts a delivery report before the message it settles, so
// the event log reads as though the handset received it before we sent it.
func TestAMissingOrUnparseableTimestampFallsBackToNow(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	for _, payload := range []string{
		`{"messageId":"m","eventType":"DELIVERED"}`,
		`{"messageId":"m","eventType":"DELIVERED","sendTime":"not a time"}`,
	} {
		event, err := ParseAirtelWebhook([]byte(payload))
		if err != nil {
			t.Fatalf("%s: %v", payload, err)
		}
		if event.OccurredAt.Before(before) {
			t.Errorf("%s → OccurredAt %v, want roughly now", payload, event.OccurredAt)
		}
	}
}

// Airtel's own billing sample contains spaces inside the timestamp.
func TestAirtelTimestampWithSpacesStillParses(t *testing.T) {
	event, err := ParseAirtelWebhook([]byte(
		`{"messageId":"m","eventType":"DELIVERED","sendTime":"2026-05-15T05: 38: 35.974817722Z"}`))
	if err != nil {
		t.Fatalf("ParseAirtelWebhook: %v", err)
	}
	if event.OccurredAt.Year() != 2026 || event.OccurredAt.Month() != time.May {
		t.Errorf("OccurredAt = %v, want the carrier's own timestamp", event.OccurredAt)
	}
}
