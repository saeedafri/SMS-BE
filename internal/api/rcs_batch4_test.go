package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

func rcsCard(title string, suggestions ...map[string]any) map[string]any {
	card := map[string]any{"mediaUrl": "https://cdn.example/a.jpg", "title": title, "description": "D"}
	if len(suggestions) > 0 {
		card["suggestions"] = suggestions
	}
	return card
}

func (h *harness) rcsContentTemplate(tenant account, sender string, content map[string]any) response {
	h.t.Helper()
	return h.do(http.MethodPost, "/v1/templates", tenant.Token, map[string]any{
		"name": "RCS " + uuid.NewString()[:8], "senderId": sender, "category": "UTILITY",
		"rcsContent": content})
}

// Batch 4 rows R1 to R3 and R8: a carousel is accepted with its limits, and a
// variable that lives only in a card button's link is a variable of the
// template.
func TestACarouselTemplateIsAcceptedWithinItsLimits(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tenant := h.newAccount("owner")
	sender := h.approvedSenderOn(tenant, "RCS")

	two := map[string]any{"kind": "carousel", "cardWidth": "MEDIUM", "cards": []any{
		rcsCard("One", map[string]any{"type": "open_url", "text": "Track",
			"url": "{{track_url}}", "postbackData": "{{not_a_variable}}"}),
		rcsCard("Two")}}
	res := h.rcsContentTemplate(tenant, sender, two)
	var created struct {
		RcsContent struct {
			Kind string `json:"kind"`
		} `json:"rcsContent"`
		Variables []string `json:"variables"`
	}
	res.decode(t, &created)
	if res.Code != http.StatusCreated || created.RcsContent.Kind != "carousel" {
		t.Fatalf("R1 two cards = %d %s", res.Code, res.Body)
	}
	if strings.Join(created.Variables, ",") != "track_url" {
		t.Errorf("R8 variables = %v, want only track_url (postbackData is not walked)", created.Variables)
	}

	one := map[string]any{"kind": "carousel", "cardWidth": "MEDIUM", "cards": []any{rcsCard("One")}}
	if res := h.rcsContentTemplate(tenant, sender, one); res.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(string(res.Body), "2 to 10 cards") {
		t.Errorf("R2 one card = %d %s", res.Code, res.Body)
	}
	long := map[string]any{"kind": "carousel", "cardWidth": "MEDIUM", "cards": []any{
		rcsCard("One"), rcsCard(strings.Repeat("x", 201))}}
	if res := h.rcsContentTemplate(tenant, sender, long); res.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(string(res.Body), "Card 2 title") {
		t.Errorf("R3 long title = %d %s", res.Code, res.Body)
	}
	calendar := map[string]any{"kind": "text", "text": "Hi", "suggestions": []any{
		map[string]any{"type": "create_calendar_event", "text": "Add", "title": "T",
			"startTime": "2026-12-01T10:00:00Z", "endTime": "2026-12-01T10:00:00Z"}}}
	if res := h.rcsContentTemplate(tenant, sender, calendar); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("R7 end equals start = %d, want 422", res.Code)
	}
}

// Batch 4 row R13: a message that failed because the handset could not render
// it explains itself as the handset's, uncharged, with the step named.
func TestAnUnrenderableMessageIsTheHandsetsFailure(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	conn, err := h.server.ClickHouse.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	id, now, code := uuid.New(), time.Now().UTC(), "rich_content_unsupported"
	if err := store.InsertMessages(context.Background(), conn, []store.MessageRecord{{
		TenantID: tenant.TenantID, ID: id, Channel: "RCS", Country: "IN", SenderHeader: "RICH",
		Msisdn: "+919876543210", Status: "undelivered", ErrorCode: &code, FraudFlag: "none",
		Currency: "INR", CreatedAt: now, UpdatedAt: now, Version: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	got := h.logged(tenant, id)
	steps := []string{}
	for _, step := range got.Timeline {
		steps = append(steps, step.Step)
	}
	if got.Failure.Layer == nil || *got.Failure.Layer != "handset" || got.Failure.Retryable ||
		got.Failure.Charged || !strings.Contains(strings.Join(steps, ","), "rich_content_unsupported") {
		t.Errorf("R13 failure %+v timeline %v", got.Failure, steps)
	}
}

// Batch 4 rows R15 and R16: launch states per carrier, for operators only.
func TestOperatorsSeeLaunchStatesPerCarrier(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tenant := h.newAccount("owner")
	for _, token := range []string{tenant.Token, ""} {
		if res := h.do(http.MethodGet, "/v1/operator/rcs-launches", token, nil); res.Code != http.StatusUnauthorized {
			t.Errorf("R15 non-operator = %d, want 401", res.Code)
		}
	}
	res := h.do(http.MethodGet, "/v1/operator/rcs-launches", h.operatorToken(), nil)
	var body struct {
		Carriers []struct {
			Carrier                                   string
			NotSubmitted, Pending, Approved, Rejected int
		} `json:"carriers"`
	}
	res.decode(t, &body)
	if res.Code != http.StatusOK {
		t.Fatalf("R16 = %d %s", res.Code, res.Body)
	}
	var launches []struct {
		Carrier string
		Count   int
	}
	rows, err := h.admin.Query(context.Background(),
		`SELECT carrier, count(*)::int FROM rcs_agent_carrier_launches GROUP BY carrier`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var row struct {
			Carrier string
			Count   int
		}
		_ = rows.Scan(&row.Carrier, &row.Count)
		launches = append(launches, row)
	}
	rows.Close()
	sums := map[string]int{}
	for _, c := range body.Carriers {
		sums[c.Carrier] = c.NotSubmitted + c.Pending + c.Approved + c.Rejected
	}
	for _, launch := range launches {
		// Other tests write launches concurrently; a carrier's sum may only
		// have grown since it was read, never be smaller.
		if sums[launch.Carrier] > launch.Count {
			t.Errorf("R16 %s sums to %d but %d launch rows exist", launch.Carrier, sums[launch.Carrier], launch.Count)
		}
	}
}
