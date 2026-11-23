package rcs

import (
	"encoding/json"
	"strings"
	"testing"
)

func card(title string, suggestions ...map[string]any) map[string]any {
	c := map[string]any{"mediaUrl": "https://cdn.example/a.jpg", "title": title, "description": "D"}
	if len(suggestions) > 0 {
		c["suggestions"] = suggestions
	}
	return c
}

func carousel(cards ...map[string]any) map[string]any {
	return map[string]any{"kind": "carousel", "cardWidth": "MEDIUM", "cards": cards}
}

func reply(label string) map[string]any { return map[string]any{"type": "reply", "text": label} }

func check(t *testing.T, name string, content map[string]any, wantFragment string) {
	t.Helper()
	raw, _ := json.Marshal(content)
	got := Validate(raw, func(string) string { return "" })
	if wantFragment == "" && got != "" {
		t.Errorf("%s refused: %s", name, got)
	}
	if wantFragment != "" && !strings.Contains(got, wantFragment) {
		t.Errorf("%s = %q, want it to mention %q", name, got, wantFragment)
	}
}

// Batch 4 rows R1 to R7, as the rule itself.
func TestRCSContentLimits(t *testing.T) {
	check(t, "R1 two cards", carousel(card("A"), card("B")), "")
	check(t, "R2 one card", carousel(card("A")), "2 to 10 cards")
	eleven := []map[string]any{}
	for i := 0; i < 11; i++ {
		eleven = append(eleven, card("C"))
	}
	check(t, "R2 eleven cards", carousel(eleven...), "2 to 10 cards")
	check(t, "R3 long title", carousel(card("A"), card(strings.Repeat("x", 201))), "Card 2 title")

	twelve := carousel(card("A", reply("1"), reply("2"), reply("3"), reply("4")),
		card("B", reply("5"), reply("6"), reply("7"), reply("8")),
		card("C", reply("9"), reply("10"), reply("11")))
	twelve["suggestions"] = []map[string]any{reply("12")}
	check(t, "R4 twelve suggestions", twelve, "at most 11 suggestions in all")

	check(t, "R5 ftp", map[string]any{"kind": "text", "text": "Hi", "suggestions": []map[string]any{
		{"type": "open_url", "text": "Go", "url": "ftp://x.example"}}}, "https://")
	check(t, "R6 latitude 91", map[string]any{"kind": "text", "text": "Hi", "suggestions": []map[string]any{
		{"type": "view_location", "text": "Map", "latLong": map[string]any{"latitude": 91, "longitude": 0}}}},
		"Latitude")
	check(t, "R6 neither", map[string]any{"kind": "text", "text": "Hi", "suggestions": []map[string]any{
		{"type": "view_location", "text": "Map"}}}, "coordinates or a search")
	check(t, "R7 end equals start", map[string]any{"kind": "text", "text": "Hi", "suggestions": []map[string]any{
		{"type": "create_calendar_event", "text": "Add", "title": "T",
			"startTime": "2026-12-01T10:00:00Z", "endTime": "2026-12-01T10:00:00Z"}}}, "end after it starts")
	check(t, "undeclared field", map[string]any{"kind": "text", "text": "Hi", "colour": "red"}, "Unknown field")
	check(t, "share location", map[string]any{"kind": "text", "text": "Hi", "suggestions": []map[string]any{
		{"type": "share_location", "text": "Share", "postbackData": "loc"}}}, "")
}

func TestRichFeaturesNameWhatAHandsetMustRender(t *testing.T) {
	raw, _ := json.Marshal(carousel(card("A", map[string]any{"type": "share_location", "text": "S"}), card("B")))
	if got := strings.Join(RichFeatures(raw), ","); got != "ACTION_SHARE_LOCATION,RICHCARD_CAROUSEL" {
		t.Errorf("features = %s", got)
	}
	plain, _ := json.Marshal(map[string]any{"kind": "text", "text": "Hi"})
	if got := RichFeatures(plain); len(got) != 0 {
		t.Errorf("a plain text needs %v", got)
	}
}

func TestWithoutPostbackDataBlanksOnlyThatField(t *testing.T) {
	raw := []byte(`{"text":"{{a}}","suggestions":[{"postbackData":"{{b}} \"q\"","text":"x"}]}`)
	want := `{"text":"{{a}}","suggestions":[{"postbackData":"","text":"x"}]}`
	if got := string(WithoutPostbackData(raw)); got != want {
		t.Errorf("got %s", got)
	}
}

func TestWithoutPostbackDataToleratesSpacing(t *testing.T) {
	raw := []byte(`{"postbackData" : "{{b}}", "text": "{{a}}"}`)
	if got := string(WithoutPostbackData(raw)); got != `{"postbackData":"", "text": "{{a}}"}` {
		t.Errorf("got %s", got)
	}
}
