package webhook

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Native integrations: the same event, in the shape each product's own events
// API takes. What is sent is the vendor's contract, not ours, so none of this
// carries Relay's envelope.
const (
	IntegrationWebEngage = "webengage"
	IntegrationMoEngage  = "moengage"
	IntegrationCleverTap = "clevertap"
)

// Integrations is every type a customer may choose, besides the default.
var Integrations = []string{IntegrationWebEngage, IntegrationMoEngage, IntegrationCleverTap}

type relayEvent struct {
	Event string         `json:"event"`
	Data  map[string]any `json:"data"`
}

// Transform rewrites a Relay event payload for a vendor. An unknown type and a
// payload that is not a Relay event are both errors rather than a silent
// passthrough: sending the wrong shape to a customer's marketing platform
// creates a user in it.
func Transform(integration string, payload []byte, now time.Time) ([]byte, error) {
	var event relayEvent
	if err := json.Unmarshal(payload, &event); err != nil || event.Event == "" {
		return nil, fmt.Errorf("webhook: payload is not a Relay event")
	}
	name := eventName(event.Event)
	user := userID(event.Data)
	attributes := flatten("", event.Data)
	switch integration {
	case IntegrationWebEngage:
		// POST /v1/accounts/{licenseCode}/events
		return json.Marshal(map[string]any{
			"userId": user, "eventName": name, "eventData": attributes,
			"eventTime": now.UTC().Format("2006-01-02T15:04:05-0700"),
		})
	case IntegrationMoEngage:
		// POST /v1/event/{workspace_id}
		return json.Marshal(map[string]any{
			"type": "event", "customer_id": user,
			"actions": []map[string]any{{
				"action": name, "attributes": attributes, "platform": "web",
				"current_time": now.Unix(), "user_timezone_offset": 19800,
			}},
		})
	case IntegrationCleverTap:
		// POST /1/upload
		return json.Marshal(map[string]any{"d": []map[string]any{{
			"identity": user, "ts": now.Unix(), "type": "event",
			"evtName": name, "evtData": attributes,
		}}})
	}
	return nil, fmt.Errorf("webhook: unknown integration %q", integration)
}

// eventName reads "message.delivered" as "Relay Message Delivered": a name a
// marketer recognises in their own tool, and one that cannot collide with their
// events of the same word.
func eventName(event string) string {
	words := strings.FieldsFunc(event, func(r rune) bool { return r == '.' || r == '_' })
	for i, word := range words {
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return "Relay " + strings.Join(words, " ")
}

// userID is who the event is about: the phone number when the event has one.
func userID(data map[string]any) string {
	for _, key := range []string{"msisdn", "recipient", "to"} {
		if value, ok := data[key].(string); ok && value != "" {
			return value
		}
	}
	if id, ok := data["messageId"].(string); ok {
		return id
	}
	return "unknown"
}

// flatten turns nested data into one level of scalar properties, which is all
// the three vendors accept. Nulls are dropped and nested names are joined with
// an underscore, so {"a":{"b":1}} becomes {"a_b":1}.
func flatten(prefix string, data map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range data {
		name := key
		if prefix != "" {
			name = prefix + "_" + key
		}
		switch typed := value.(type) {
		case nil:
		case map[string]any:
			for k, v := range flatten(name, typed) {
				out[k] = v
			}
		case []any:
			joined, _ := json.Marshal(typed)
			out[name] = string(joined)
		default:
			out[name] = typed
		}
	}
	return out
}
