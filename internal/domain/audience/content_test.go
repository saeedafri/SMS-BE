package audience

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The card is the shape RCS exists for, and it is the shape a field-by-field
// parser misses. This document carries a slot in every place one can hide: the
// text, the card's title, description and media url, and — the one the
// frontend's own parser missed on their side — a suggestion's link.
const rcsCard = `{
  "text": "Hi {{firstName}}",
  "card": {
    "title": "{{offer}} just for you",
    "description": "Ends {{expiryDate}}",
    "mediaUrl": "https://cdn.example.com/{{bannerId}}.png"
  },
  "suggestions": [
    {"text": "Track {{orderId}}", "url": "https://example.com/t/{{orderId}}"},
    {"text": "Call us", "phoneNumber": "+91{{supportLine}}"}
  ]
}`

func TestEverySlotInTheMessageIsFound(t *testing.T) {
	found := SlotsInDocument([]byte(rcsCard))
	want := []string{"bannerId", "expiryDate", "firstName", "offer", "orderId", "supportLine"}
	if !reflect.DeepEqual(found, want) {
		t.Fatalf("slots in document = %v, want %v", found, want)
	}
}

func TestAVariableInAButtonLinkIsNotMissed(t *testing.T) {
	// The narrow case on its own, because it is the one that survived a parser
	// that read three named fields: no text, no card, a slot only in a link.
	document := []byte(`{"suggestions":[{"text":"Track it","url":"https://x.test/{{orderId}}"}]}`)
	if found := SlotsInDocument(document); !reflect.DeepEqual(found, []string{"orderId"}) {
		t.Fatalf("slots in a button link = %v, want [orderId]", found)
	}
}

func TestFillingWalksTheWholeDocument(t *testing.T) {
	fields := map[string]string{
		"First Name": "Priya", "offer": "20% off", "expiryDate": "31 Mar",
		"bannerId": "spring", "orderId": "A-91", "supportLine": "8001234567",
	}
	mapping := map[string]string{"firstName": "First Name"}

	filled, missing := FillDocument([]byte(rcsCard), fields, mapping)
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none", missing)
	}
	if HasSlot(string(filled)) {
		t.Fatalf("a slot survived the fill: %s", filled)
	}

	var decoded struct {
		Text string `json:"text"`
		Card struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			MediaURL    string `json:"mediaUrl"`
		} `json:"card"`
		Suggestions []struct {
			Text        string `json:"text"`
			URL         string `json:"url"`
			PhoneNumber string `json:"phoneNumber"`
		} `json:"suggestions"`
	}
	if err := json.Unmarshal(filled, &decoded); err != nil {
		t.Fatalf("filled document does not decode: %v", err)
	}
	if decoded.Text != "Hi Priya" {
		t.Errorf("text = %q", decoded.Text)
	}
	if decoded.Card.Title != "20% off just for you" {
		t.Errorf("card title = %q", decoded.Card.Title)
	}
	if decoded.Card.MediaURL != "https://cdn.example.com/spring.png" {
		t.Errorf("media url = %q", decoded.Card.MediaURL)
	}
	if decoded.Suggestions[0].URL != "https://example.com/t/A-91" {
		t.Errorf("suggestion url = %q", decoded.Suggestions[0].URL)
	}
	if decoded.Suggestions[1].PhoneNumber != "+918001234567" {
		t.Errorf("suggestion phone = %q", decoded.Suggestions[1].PhoneNumber)
	}
}

func TestASlotWithNoValueIsReportedAndLeftRaw(t *testing.T) {
	// Left raw rather than blanked, so the token is still visible to the
	// invariant that reads the rendered strings — blanking it would hide the
	// hole from the one check that exists to find it.
	document := []byte(`{"card":{"title":"Hi {{firstName}}","description":"Order {{orderId}}"}}`)
	filled, missing := FillDocument(document, map[string]string{"orderId": "A-91"}, nil)
	if !reflect.DeepEqual(missing, []string{"firstName"}) {
		t.Fatalf("missing = %v, want [firstName]", missing)
	}
	if !HasSlot(string(filled)) {
		t.Fatalf("an unfilled slot should survive visibly: %s", filled)
	}
}

func TestFieldNamesAreNotTreatedAsContent(t *testing.T) {
	// Keys come from the contract, not from anything a customer typed.
	// Rewriting one would rename the field and change the message's shape.
	document := []byte(`{"card":{"title":"Hello"}}`)
	filled, _ := FillDocument(document, map[string]string{"card": "x"}, nil)
	if !hasKey(t, filled, "card") {
		t.Fatalf("the card key did not survive the walk: %s", filled)
	}
}

func hasKey(t *testing.T, document []byte, key string) bool {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(document, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_, present := decoded[key]
	return present
}

func TestADocumentThatIsNotJSONNamesNothing(t *testing.T) {
	if found := SlotsInDocument([]byte("not json {{firstName}}")); found != nil {
		t.Fatalf("slots = %v, want none", found)
	}
	filled, missing := FillDocument([]byte("not json"), nil, nil)
	if string(filled) != "not json" || missing != nil {
		t.Fatalf("filled = %q, missing = %v; the bytes should come back untouched", filled, missing)
	}
}
