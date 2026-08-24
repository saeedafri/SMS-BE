package connector

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Carrier webhooks.
//
// This is where the two vendors diverge most. Airtel POSTs its own JSON
// directly. Vi wraps Google's Pub/Sub envelope around a base64 payload, because
// its Google-style API forwards what Google would have sent. Neither signs the
// request, so authentication is the transport's problem — see the ingest
// handler — and this file is only about reading what they said.
//
// Correlation differs too, and it is the part most likely to break silently:
//
//	Airtel  Every field is theirs. Delivery events quote the messageRequestId
//	        issued at submit, so the only way back to a Relay message is the
//	        carrier reference stored beside it.
//	Vi      Accepts a caller-supplied messageId, so Relay sends its own uuid and
//	        gets it back verbatim.
//
// Both end at the same settlement code. Parsing is kept as pure functions over
// bytes so every one of these shapes can be tested without a carrier, an HTTP
// server, or a database.

type RCSEventKind string

const (
	// RCSEventDelivery moves a message towards a terminal state.
	RCSEventDelivery RCSEventKind = "delivery"
	// RCSEventTemplate is a carrier approving or rejecting a template.
	RCSEventTemplate RCSEventKind = "template"
	// RCSEventInbound is a user replying or tapping a suggestion.
	RCSEventInbound RCSEventKind = "inbound"
	// RCSEventIgnored is a real event with no consequence for Relay — a typing
	// indicator, a submit acknowledgement we already recorded, a billing
	// re-categorisation. Named rather than dropped so the ingest handler can
	// answer 200 and say why, instead of looking like it silently failed.
	RCSEventIgnored RCSEventKind = "ignored"
)

// RCSEvent is one thing a carrier said happened, in Relay's terms.
type RCSEvent struct {
	Kind   RCSEventKind
	Vendor string

	// Raw is the carrier's own event name, kept for the audit trail. A carrier
	// adding an event type should be visible in the logs rather than vanishing
	// into "ignored".
	Raw string

	// CarrierRef identifies the message. For Airtel it is their
	// messageRequestId; for Vi it is the uuid Relay supplied at submit.
	CarrierRef string

	// Delivered is true only for an event that proves a handset received it.
	// A read receipt counts: a message cannot be read without being delivered,
	// and carriers do sometimes skip the delivery event.
	Delivered bool

	// ErrorCode is set on a failure, in Relay's vocabulary rather than the
	// carrier's.
	ErrorCode string

	OccurredAt time.Time

	// Template events.
	CarrierTemplateID string
	TemplateStatus    string
	RejectionReason   string

	// Inbound events.
	Msisdn       string
	Text         string
	PostbackData string
	// ContextRef ties a suggestion tap back to the outbound message that
	// offered it. Airtel calls this contextMessageId; without it a postback is
	// an orphan and the conversation cannot be threaded.
	ContextRef string
}

// airtelWebhook is Airtel's payload, which is the same envelope for template
// decisions, delivery events and inbound messages alike — the eventType field
// is what separates them.
type airtelWebhook struct {
	MessageID  string `json:"messageId"`
	AgentID    string `json:"agentId"`
	Msisdn     string `json:"msisdn"`
	MsgStream  string `json:"msgStream"`
	TemplateID string `json:"templateId"`
	EventType  string `json:"eventType"`
	SendTime   string `json:"sendTime"`

	MessageContent *struct {
		TemplateStatus  string `json:"templateStatus"`
		RejectionReason string `json:"rejectionReason"`
		TemplateName    string `json:"templateName"`
		TemplateID      string `json:"templateId"`

		Text             string `json:"text"`
		Type             string `json:"type"`
		PostbackData     string `json:"postbackData"`
		ContextMessageID string `json:"contextMessageId"`
	} `json:"messageContent"`

	Error *struct {
		ErrorMessage string `json:"errorMessage"`
		ErrorType    string `json:"errorType"`
	} `json:"error"`
}

func ParseAirtelWebhook(payload []byte) (RCSEvent, error) {
	var body airtelWebhook
	if err := json.Unmarshal(payload, &body); err != nil {
		return RCSEvent{}, fmt.Errorf("airtel webhook: %w", err)
	}
	if body.EventType == "" {
		return RCSEvent{}, fmt.Errorf("airtel webhook: no eventType")
	}

	event := RCSEvent{
		Vendor:     "airtel",
		Raw:        body.EventType,
		CarrierRef: body.MessageID,
		Msisdn:     body.Msisdn,
		OccurredAt: parseCarrierTime(body.SendTime),
	}

	switch body.EventType {
	case "TEMPLATE_APPROVED", "TEMPLATE_REJECTED":
		event.Kind = RCSEventTemplate
		// The template id appears at the top level AND inside messageContent,
		// and they are the same value in every documented sample. Preferring
		// the nested one and falling back keeps this working if either moves.
		event.CarrierTemplateID = body.TemplateID
		status, reason := "", ""
		if body.MessageContent != nil {
			if body.MessageContent.TemplateID != "" {
				event.CarrierTemplateID = body.MessageContent.TemplateID
			}
			status = body.MessageContent.TemplateStatus
			reason = body.MessageContent.RejectionReason
		}
		if status == "" {
			status = strings.TrimPrefix(body.EventType, "TEMPLATE_")
		}
		event.TemplateStatus = normaliseCarrierTemplateStatus(status)
		event.RejectionReason = reason
		return event, nil

	case "DELIVERED", "READ":
		event.Kind = RCSEventDelivery
		event.Delivered = true
		return event, nil

	case "TTL_EXPIRATION_REVOKED", "TTL_EXPIRATION_REVOKE_FAILED":
		// The message expired before it reached the handset. Airtel refunds
		// these, so Relay must release its hold rather than charge for them —
		// which is what an undelivered outcome already does.
		event.Kind = RCSEventDelivery
		event.ErrorCode = "expired_before_delivery"
		return event, nil

	case "FAILED", "INTERNAL_ERROR":
		event.Kind = RCSEventDelivery
		event.ErrorCode = "carrier_failed"
		if body.Error != nil && body.Error.ErrorType == "VALIDATION_ERROR" {
			// Airtel validates at send time and reports the failure here rather
			// than in the send response, so a malformed template fill looks
			// identical to a network failure unless this is separated out.
			event.ErrorCode = "carrier_validation_failed"
		}
		return event, nil

	case "RECEIVED":
		event.Kind = RCSEventInbound
		if body.MessageContent != nil {
			event.Text = body.MessageContent.Text
			event.PostbackData = body.MessageContent.PostbackData
			event.ContextRef = body.MessageContent.ContextMessageID
		}
		return event, nil

	default:
		// SENT is already recorded from the submit response, BILLING_CATEGORY_
		// UPDATE changes only how Airtel invoices us, and anything new is
		// safer ignored than guessed at.
		event.Kind = RCSEventIgnored
		return event, nil
	}
}

// viPubSubEnvelope is Google's Pub/Sub push shape, which Vi reproduces.
type viPubSubEnvelope struct {
	Message *struct {
		Data       string            `json:"data"`
		Attributes map[string]string `json:"attributes"`
	} `json:"message"`
}

type viEvent struct {
	SenderPhoneNumber string `json:"senderPhoneNumber"`
	PhoneNumber       string `json:"phoneNumber"`
	MessageID         string `json:"messageId"`
	EventType         string `json:"eventType"`
	EventID           string `json:"eventId"`
	SendTime          string `json:"sendTime"`
	Text              string `json:"text"`
	Reason            string `json:"reason"`

	SuggestionResponse *struct {
		PostbackData string `json:"postbackData"`
		Text         string `json:"text"`
		Type         string `json:"type"`
		Metadata     string `json:"metadata"`
	} `json:"suggestionResponse"`
}

func ParseViWebhook(payload []byte) (RCSEvent, error) {
	// Vi sends the Pub/Sub envelope, but the doc also shows the bare event in
	// its examples. Accepting both costs one branch and avoids a whole class of
	// "works in the sample, fails in production".
	var envelope viPubSubEnvelope
	inner := payload
	if err := json.Unmarshal(payload, &envelope); err == nil &&
		envelope.Message != nil && envelope.Message.Data != "" {
		decoded, err := decodeBase64Either(envelope.Message.Data)
		if err != nil {
			return RCSEvent{}, fmt.Errorf("vi webhook: undecodable data field: %w", err)
		}
		inner = decoded
	}

	var body viEvent
	if err := json.Unmarshal(inner, &body); err != nil {
		return RCSEvent{}, fmt.Errorf("vi webhook: %w", err)
	}

	msisdn := body.SenderPhoneNumber
	if msisdn == "" {
		msisdn = body.PhoneNumber
	}
	event := RCSEvent{
		Vendor:     "vi",
		Raw:        body.EventType,
		CarrierRef: body.MessageID,
		Msisdn:     msisdn,
		OccurredAt: parseCarrierTime(body.SendTime),
	}

	// A suggestion tap is checked before eventType, because it arrives with no
	// eventType at all and would otherwise fall through to ignored.
	if body.SuggestionResponse != nil {
		event.Kind = RCSEventInbound
		event.Raw = "SUGGESTION_RESPONSE"
		event.Text = body.SuggestionResponse.Text
		event.PostbackData = body.SuggestionResponse.PostbackData
		return event, nil
	}

	switch body.EventType {
	case "DELIVERED", "READ":
		event.Kind = RCSEventDelivery
		event.Delivered = true
	case "FAILED":
		event.Kind = RCSEventDelivery
		event.ErrorCode = "carrier_failed"
		if strings.Contains(body.Reason, "requests allowed per second") {
			event.ErrorCode = "carrier_throttled"
		}
	case "TTL_EXPIRATION_REVOKED", "TTL_EXPIRATION_REVOKE_FAILED":
		event.Kind = RCSEventDelivery
		event.ErrorCode = "expired_before_delivery"
	case "":
		// No eventType and no suggestion means a plain inbound message.
		if body.Text != "" {
			event.Kind = RCSEventInbound
			event.Raw = "MESSAGE"
			event.Text = body.Text
			return event, nil
		}
		event.Kind = RCSEventIgnored
	default:
		// IS_TYPING and anything Vi adds later.
		event.Kind = RCSEventIgnored
	}
	return event, nil
}

// decodeBase64Either accepts both alphabets. Google's Pub/Sub uses standard
// base64, but URL-safe encodings turn up in the wild often enough that failing
// on one would drop every delivery report from an affected deployment.
func decodeBase64Either(value string) ([]byte, error) {
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(value)
}

// parseCarrierTime reads either carrier's timestamp, falling back to now.
//
// A zero time would sort a delivery report before the message it settles and
// make the event log read as though the handset received it before we sent it.
// Both vendors use RFC3339 with varying fractional precision, which time.Parse
// handles; Airtel's samples also contain spaces inside the time in at least one
// place, which it does not.
func parseCarrierTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Now().UTC()
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC()
	}
	if parsed, err := time.Parse(time.RFC3339Nano, strings.ReplaceAll(value, " ", "")); err == nil {
		return parsed.UTC()
	}
	return time.Now().UTC()
}
