package rcs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Google's RBM limits, read 23 October 2026. A carrier stricter than these is
// a reason to tighten them (decision D-F); never to loosen.
const (
	textBodyMax            = 3072
	cardTitleMax           = 200
	cardDescriptionMax     = 2000
	suggestionTextMax      = 25
	postbackDataMax        = 2048
	urlMax                 = 2048
	suggestionsPerMessage  = 11
	suggestionsPerCard     = 4
	carouselCardsMin       = 2
	carouselCardsMax       = 10
	calendarTitleMax       = 100
	calendarDescriptionMax = 500
	locationLabelMax       = 100
	locationQueryMax       = 200
)

type object = map[string]json.RawMessage

var contentKeys = map[string][]string{
	"text":     {"kind", "text", "suggestions"},
	"card":     {"kind", "card", "suggestions", "orientation", "thumbnailAlignment"},
	"carousel": {"kind", "cardWidth", "cards", "suggestions"},
}

var cardKeys = []string{"mediaUrl", "title", "description", "mediaHeight", "suggestions"}

var suggestionKeys = map[string][]string{
	"reply":                 {"type", "text", "postbackData"},
	"open_url":              {"type", "text", "url", "postbackData", "application", "webviewViewMode"},
	"dial":                  {"type", "text", "phoneNumber", "postbackData"},
	"view_location":         {"type", "text", "latLong", "label", "query", "postbackData"},
	"create_calendar_event": {"type", "text", "startTime", "endTime", "title", "description", "postbackData"},
	"share_location":        {"type", "text", "postbackData"},
}

// Validate judges RCS content in the contract's order: an undeclared field,
// the structure, the limits, then the destination's URL rule. It returns the
// sentence naming what is wrong, or "" when the content is acceptable. Nothing
// is ever truncated to fit.
func Validate(raw []byte, urlRule func(string) string) string {
	var content object
	if json.Unmarshal(raw, &content) != nil {
		return "Choose a message type."
	}
	kind := text(content["kind"])
	if _, known := contentKeys[kind]; !known {
		return "Choose a message type."
	}
	if stray := undeclared(content, kind); stray != "" {
		return stray
	}

	messageLevel := list(content["suggestions"])
	max := suggestionsPerMessage
	if kind == "card" {
		max = suggestionsPerCard
	}
	if len(messageLevel) > max {
		return fmt.Sprintf("A %s message allows at most %d suggestions.", kind, max)
	}
	type button struct {
		value object
		where string
	}
	var buttons []button
	for _, s := range messageLevel {
		buttons = append(buttons, button{asObject(s), ""})
	}

	switch kind {
	case "text":
		body := text(content["text"])
		if strings.TrimSpace(body) == "" {
			return "Message text is required."
		}
		if len([]rune(body)) > textBodyMax {
			return fmt.Sprintf("Message text is at most %d characters.", textBodyMax)
		}
	case "card":
		card := asObject(content["card"])
		if problem := validateCard(card, "The card"); problem != "" {
			return problem
		}
		orientation, hasOrientation := optionalText(content, "orientation")
		if hasOrientation && orientation != "VERTICAL" && orientation != "HORIZONTAL" {
			return "A card is VERTICAL or HORIZONTAL."
		}
		if alignment, set := optionalText(content, "thumbnailAlignment"); set {
			if alignment != "LEFT" && alignment != "RIGHT" {
				return "The thumbnail sits LEFT or RIGHT."
			}
			if orientation != "HORIZONTAL" {
				return "A thumbnail side only applies to a HORIZONTAL card."
			}
		}
		for _, s := range list(card["suggestions"]) {
			buttons = append(buttons, button{asObject(s), "card "})
		}
	case "carousel":
		width := text(content["cardWidth"])
		if width != "SMALL" && width != "MEDIUM" {
			return "A carousel card width is SMALL or MEDIUM."
		}
		cards := list(content["cards"])
		if len(cards) < carouselCardsMin || len(cards) > carouselCardsMax {
			return fmt.Sprintf("A carousel has %d to %d cards.", carouselCardsMin, carouselCardsMax)
		}
		for i, raw := range cards {
			card := asObject(raw)
			if problem := validateCard(card, fmt.Sprintf("Card %d", i+1)); problem != "" {
				return problem
			}
			for _, s := range list(card["suggestions"]) {
				buttons = append(buttons, button{asObject(s), fmt.Sprintf("card %d ", i+1)})
			}
		}
	}

	if len(buttons) > suggestionsPerMessage {
		return fmt.Sprintf("A message allows at most %d suggestions in all, cards included.",
			suggestionsPerMessage)
	}
	for _, b := range buttons {
		if problem := validateSuggestion(b.value, b.where, urlRule); problem != "" {
			return problem
		}
	}
	return ""
}

func validateCard(card object, who string) string {
	media := text(card["mediaUrl"])
	if !strings.HasPrefix(strings.TrimSpace(media), "https://") {
		return who + " needs an https media URL."
	}
	if len(media) > urlMax {
		return fmt.Sprintf("%s media URL is at most %d characters.", who, urlMax)
	}
	title := text(card["title"])
	if strings.TrimSpace(title) == "" {
		return who + " needs a title."
	}
	if len([]rune(title)) > cardTitleMax {
		return fmt.Sprintf("%s title is at most %d characters.", who, cardTitleMax)
	}
	description := text(card["description"])
	if strings.TrimSpace(description) == "" {
		return who + " needs a description."
	}
	if len([]rune(description)) > cardDescriptionMax {
		return fmt.Sprintf("%s description is at most %d characters.", who, cardDescriptionMax)
	}
	if height, set := optionalText(card, "mediaHeight"); set &&
		height != "SHORT" && height != "MEDIUM" && height != "TALL" {
		return who + " media height must be SHORT, MEDIUM or TALL."
	}
	if len(list(card["suggestions"])) > suggestionsPerCard {
		return fmt.Sprintf("%s allows at most %d suggestions.", who, suggestionsPerCard)
	}
	return ""
}

func validateSuggestion(s object, where string, urlRule func(string) string) string {
	name := where + "suggestion"
	label := text(s["text"])
	if strings.TrimSpace(label) == "" {
		return "Each " + name + " needs a label."
	}
	if len([]rune(label)) > suggestionTextMax {
		return fmt.Sprintf("A %s label is at most %d characters.", name, suggestionTextMax)
	}
	if len(text(s["postbackData"])) > postbackDataMax {
		return fmt.Sprintf("A %s carries at most %d characters of postback data.", name, postbackDataMax)
	}
	switch text(s["type"]) {
	case "open_url":
		url := strings.TrimSpace(text(s["url"]))
		if url == "" {
			return "A URL suggestion needs a link."
		}
		if len(url) > urlMax {
			return fmt.Sprintf("A link is at most %d characters.", urlMax)
		}
		lower := strings.ToLower(url)
		if !strings.HasPrefix(url, "{{") && !strings.HasPrefix(lower, "https://") &&
			!strings.HasPrefix(lower, "http://") {
			return "A link must start with https:// or http://."
		}
		// A link that is one variable is judged once it is filled, not here.
		if !strings.HasPrefix(url, "{{") {
			if problem := urlRule(url); problem != "" {
				return problem
			}
		}
		application, hasApplication := optionalText(s, "application")
		if hasApplication && application != "BROWSER" && application != "WEBVIEW" {
			return "A link opens in the BROWSER or a WEBVIEW."
		}
		if mode, set := optionalText(s, "webviewViewMode"); set {
			if mode != "FULL" && mode != "HALF" && mode != "TALL" {
				return "A webview opens FULL, HALF or TALL."
			}
			if application != "WEBVIEW" {
				return "A webview size only applies to a link that opens in a WEBVIEW."
			}
		}
	case "dial":
		if strings.TrimSpace(text(s["phoneNumber"])) == "" {
			return "A dial suggestion needs a phone number."
		}
	case "view_location":
		var point *struct {
			Latitude  *float64 `json:"latitude"`
			Longitude *float64 `json:"longitude"`
		}
		_ = json.Unmarshal(s["latLong"], &point)
		hasQuery := strings.TrimSpace(text(s["query"])) != ""
		if point == nil && !hasQuery {
			return "A view-location suggestion needs coordinates or a search."
		}
		if point != nil {
			if point.Latitude == nil || *point.Latitude < -90 || *point.Latitude > 90 {
				return "Latitude must be between -90 and 90."
			}
			if point.Longitude == nil || *point.Longitude < -180 || *point.Longitude > 180 {
				return "Longitude must be between -180 and 180."
			}
		}
		if len([]rune(text(s["label"]))) > locationLabelMax {
			return fmt.Sprintf("A pin label is at most %d characters.", locationLabelMax)
		}
		if len([]rune(text(s["query"]))) > locationQueryMax {
			return fmt.Sprintf("A location search is at most %d characters.", locationQueryMax)
		}
	case "create_calendar_event":
		title := text(s["title"])
		if strings.TrimSpace(title) == "" {
			return "A calendar event needs a title."
		}
		if len([]rune(title)) > calendarTitleMax {
			return fmt.Sprintf("An event title is at most %d characters.", calendarTitleMax)
		}
		if len([]rune(text(s["description"]))) > calendarDescriptionMax {
			return fmt.Sprintf("An event description is at most %d characters.", calendarDescriptionMax)
		}
		start, startErr := time.Parse(time.RFC3339, text(s["startTime"]))
		end, endErr := time.Parse(time.RFC3339, text(s["endTime"]))
		if startErr != nil || endErr != nil {
			return "A calendar event needs a start and an end time."
		}
		if !end.After(start) {
			return "An event must end after it starts."
		}
	}
	return ""
}

func undeclared(content object, kind string) string {
	found := func(where string, value object, declared []string) string {
		var extra []string
		for key := range value {
			known := false
			for _, d := range declared {
				known = known || d == key
			}
			if !known {
				extra = append(extra, key)
			}
		}
		if len(extra) == 0 {
			return ""
		}
		sort.Strings(extra)
		return fmt.Sprintf("Unknown field(s) in %s: %s.", where, strings.Join(extra, ", "))
	}
	suggestions := func(raw json.RawMessage, where string) string {
		for i, item := range list(raw) {
			s := asObject(item)
			if keys, known := suggestionKeys[text(s["type"])]; known {
				if hit := found(fmt.Sprintf("%s suggestion %d", where, i+1), s, keys); hit != "" {
					return hit
				}
			}
		}
		return ""
	}
	card := func(raw json.RawMessage, where string) string {
		value := asObject(raw)
		if hit := found(where, value, cardKeys); hit != "" {
			return hit
		}
		return suggestions(value["suggestions"], where)
	}
	if hit := found("the message", content, contentKeys[kind]); hit != "" {
		return hit
	}
	if hit := suggestions(content["suggestions"], "the message"); hit != "" {
		return hit
	}
	switch kind {
	case "card":
		return card(content["card"], "the card")
	case "carousel":
		for i, raw := range list(content["cards"]) {
			if hit := card(raw, fmt.Sprintf("card %d", i+1)); hit != "" {
				return hit
			}
		}
	}
	return ""
}

// RichFeatures are the handset features a message needs beyond a plain card:
// what a phone's capability answer must list for it to render.
func RichFeatures(raw []byte) []string {
	var content object
	if json.Unmarshal(raw, &content) != nil {
		return nil
	}
	needs := map[string]bool{}
	if text(content["kind"]) == "carousel" {
		needs["RICHCARD_CAROUSEL"] = true
	}
	actions := map[string]string{"view_location": "ACTION_VIEW_LOCATION",
		"create_calendar_event": "ACTION_CREATE_CALENDAR_EVENT",
		"share_location":        "ACTION_SHARE_LOCATION"}
	var visit func(json.RawMessage)
	visit = func(raw json.RawMessage) {
		for _, item := range list(raw) {
			if feature, rich := actions[text(asObject(item)["type"])]; rich {
				needs[feature] = true
			}
		}
	}
	visit(content["suggestions"])
	visit(asObject(content["card"])["suggestions"])
	for _, card := range list(content["cards"]) {
		visit(asObject(card)["suggestions"])
	}
	out := make([]string, 0, len(needs))
	for feature := range needs {
		out = append(out, feature)
	}
	sort.Strings(out)
	return out
}

func text(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func optionalText(value object, key string) (string, bool) {
	raw, present := value[key]
	if !present || strings.TrimSpace(string(raw)) == "null" {
		return "", false
	}
	return text(raw), true
}

func list(raw json.RawMessage) []json.RawMessage {
	var items []json.RawMessage
	_ = json.Unmarshal(raw, &items)
	return items
}

func asObject(raw json.RawMessage) object {
	var value object
	_ = json.Unmarshal(raw, &value)
	return value
}

var postbackValue = regexp.MustCompile(`"postbackData"\s*:\s*"(?:[^"\\]|\\.)*"`)

// WithoutPostbackData blanks every postbackData value in JSON, keeping
// everything else byte for byte. postbackData is opaque text handed back on a
// tap: a {{token}} in it is not a variable of the message.
func WithoutPostbackData(raw []byte) []byte {
	return postbackValue.ReplaceAll(raw, []byte(`"postbackData":""`))
}
