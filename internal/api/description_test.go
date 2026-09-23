package api_test

import (
	"net/http"
	"testing"
	"time"
)

// Ask 9. A campaign's description is set at creation and only then; a
// journey's can be changed later. Empty and absent both mean "none".

func descriptionOf(t *testing.T, res response) *string {
	t.Helper()
	var body struct {
		Description *string `json:"description"`
	}
	res.decode(t, &body)
	return body.Description
}

func TestACampaignKeepsTheDescriptionItWasCreatedWith(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	sender, template := h.categorisedTemplate(tenant, "TRANSACTIONAL")
	later := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)

	for _, tc := range []struct {
		name  string
		given any
		want  *string
	}{
		{"given", "Diwali reminder for lapsed buyers", ptr("Diwali reminder for lapsed buyers")},
		{"empty", "", nil},
		{"absent", nil, nil},
	} {
		body := map[string]any{"name": "Described " + tc.name, "channel": "SMS", "country": "IN",
			"senderId": sender, "templateId": template, "scheduledAt": later}
		if tc.given != nil {
			body["description"] = tc.given
		}
		created := h.do(http.MethodPost, "/v1/campaigns", tenant.Token, body)
		if created.Code != http.StatusCreated {
			t.Fatalf("%s: create = %d\n%s", tc.name, created.Code, created.Body)
		}
		var campaign struct {
			ID string `json:"id"`
		}
		created.decode(t, &campaign)

		read := h.do(http.MethodGet, "/v1/campaigns/"+campaign.ID, tenant.Token, nil)
		if read.Code != http.StatusOK {
			t.Fatalf("%s: get = %d\n%s", tc.name, read.Code, read.Body)
		}
		if got := descriptionOf(t, read); !sameString(got, tc.want) {
			t.Errorf("%s: description = %v, want %v", tc.name, deref(got), deref(tc.want))
		}
	}
}

func TestAJourneyDescriptionCanBeSetChangedAndCleared(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tenant := h.newAccount("owner")
	id := h.newJourney(tenant.Token, "Described journey")
	path := "/v1/automation/journeys/" + id

	if got := descriptionOf(t, h.do(http.MethodGet, path, tenant.Token, nil)); got != nil {
		t.Fatalf("fresh journey description = %q, want null", *got)
	}

	steps := []struct {
		patch map[string]any
		want  *string
	}{
		{map[string]any{"description": "Welcome series"}, ptr("Welcome series")},
		// A rename that does not mention the description leaves it alone.
		{map[string]any{"name": "Renamed"}, ptr("Welcome series")},
		{map[string]any{"description": ""}, nil},
	}
	for i, step := range steps {
		res := h.do(http.MethodPatch, path, tenant.Token, step.patch)
		if res.Code != http.StatusOK {
			t.Fatalf("patch %d = %d\n%s", i, res.Code, res.Body)
		}
		got := descriptionOf(t, h.do(http.MethodGet, path, tenant.Token, nil))
		if !sameString(got, step.want) {
			t.Errorf("after patch %d: description = %v, want %v", i, deref(got), deref(step.want))
		}
	}
}

func sameString(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func deref(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
