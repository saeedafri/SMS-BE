package api_test

import (
	"net/http"
	"testing"
)

// Archived was a terminal status: archive was reachable from everywhere and
// nothing came back. A customer who archived a journey by mistake had no route
// to recovery in the product or in the API.
//
// Unarchiving is two transitions, not one. A journey that never ran returns to
// draft; one that had already been activated returns to PAUSED, and never to
// active — coming back active would resume sending to a live list on a single
// click, with no confirmation step.

// newJourney needs a real list: a list_entry trigger is validated against the
// tenant's own contact lists, so an invented id is a 422 rather than a journey.
func (h *harness) newJourney(token, name string) string {
	h.t.Helper()
	created := h.do(http.MethodPost, "/v1/contact-lists", token, map[string]any{"name": name + " list"})
	if created.Code != http.StatusCreated && created.Code != http.StatusOK {
		h.t.Fatalf("create list = %d\n%s", created.Code, created.Body)
	}
	var list struct {
		ID string `json:"id"`
	}
	created.decode(h.t, &list)

	res := h.do(http.MethodPost, "/v1/automation/journeys", token, map[string]any{
		"name":    name,
		"trigger": map[string]any{"type": "list_entry", "listId": list.ID},
		"steps": []any{
			map[string]any{"type": "wait", "id": "step-1", "durationMinutes": 60},
		},
	})
	if res.Code != http.StatusCreated && res.Code != http.StatusOK {
		h.t.Fatalf("create journey = %d\n%s", res.Code, res.Body)
	}
	var out struct {
		ID string `json:"id"`
	}
	res.decode(h.t, &out)
	return out.ID
}

// journeyState reads the two fields every case below turns on.
func (h *harness) journeyState(token, id string) (status string, activatedAt *string) {
	h.t.Helper()
	res := h.do(http.MethodGet, "/v1/automation/journeys/"+id, token, nil)
	if res.Code != http.StatusOK {
		h.t.Fatalf("get journey = %d\n%s", res.Code, res.Body)
	}
	var out struct {
		Status      string  `json:"status"`
		ActivatedAt *string `json:"activatedAt"`
	}
	res.decode(h.t, &out)
	return out.Status, out.ActivatedAt
}

func (h *harness) post(token, path string) int {
	h.t.Helper()
	return h.do(http.MethodPost, path, token, nil).Code
}

// Case 1: never activated -> draft, activatedAt still null.
func TestUnarchivingAJourneyThatNeverRanReturnsItToDraft(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	id := h.newJourney(tenant.Token, "Never ran")

	if code := h.post(tenant.Token, "/v1/automation/journeys/"+id+"/archive"); code != http.StatusOK {
		t.Fatalf("archive = %d", code)
	}
	res := h.do(http.MethodPost, "/v1/automation/journeys/"+id+"/unarchive", tenant.Token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("unarchive = %d\n%s", res.Code, res.Body)
	}
	var body struct {
		Status      string  `json:"status"`
		ActivatedAt *string `json:"activatedAt"`
	}
	res.decode(t, &body)
	if body.Status != "draft" {
		t.Errorf("status = %q, want draft", body.Status)
	}
	if body.ActivatedAt != nil {
		t.Errorf("activatedAt = %v, want null", *body.ActivatedAt)
	}
	// The response must reflect PERSISTED state: the frontend re-reads
	// immediately, and a status the database did not take shows up as the badge
	// flipping back on refresh.
	if status, _ := h.journeyState(tenant.Token, id); status != "draft" {
		t.Errorf("re-read status = %q, want draft", status)
	}
}

// Case 2: previously activated -> paused, activatedAt byte-identical.
func TestUnarchivingAJourneyThatRanReturnsItToPausedAndKeepsActivatedAt(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	id := h.newJourney(tenant.Token, "Already ran")

	if code := h.post(tenant.Token, "/v1/automation/journeys/"+id+"/activate"); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}
	_, activatedBefore := h.journeyState(tenant.Token, id)
	if activatedBefore == nil {
		t.Fatal("activate did not stamp activatedAt")
	}
	if code := h.post(tenant.Token, "/v1/automation/journeys/"+id+"/archive"); code != http.StatusOK {
		t.Fatalf("archive = %d", code)
	}

	res := h.do(http.MethodPost, "/v1/automation/journeys/"+id+"/unarchive", tenant.Token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("unarchive = %d\n%s", res.Code, res.Body)
	}
	var body struct {
		Status      string  `json:"status"`
		ActivatedAt *string `json:"activatedAt"`
	}
	res.decode(t, &body)
	if body.Status != "paused" {
		t.Fatalf("status = %q, want paused — coming back active would resume "+
			"sending to a live list on one click", body.Status)
	}
	if body.ActivatedAt == nil || *body.ActivatedAt != *activatedBefore {
		t.Errorf("activatedAt = %v, want it unchanged at %v", body.ActivatedAt, *activatedBefore)
	}
}

// Cases 3, 4, 5: every non-archived status is refused, and says which rule.
func TestOnlyAnArchivedJourneyCanBeUnarchived(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")

	for _, c := range []struct {
		state string
		setup func(id string)
	}{
		{"draft", func(string) {}},
		{"active", func(id string) { h.post(tenant.Token, "/v1/automation/journeys/"+id+"/activate") }},
		{"paused", func(id string) {
			h.post(tenant.Token, "/v1/automation/journeys/"+id+"/activate")
			h.post(tenant.Token, "/v1/automation/journeys/"+id+"/pause")
		}},
	} {
		id := h.newJourney(tenant.Token, "In "+c.state)
		c.setup(id)
		res := h.do(http.MethodPost, "/v1/automation/journeys/"+id+"/unarchive", tenant.Token, nil)
		if res.Code != http.StatusUnprocessableEntity {
			t.Errorf("unarchive a %s journey = %d, want 422\n%s", c.state, res.Code, res.Body)
			continue
		}
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		res.decode(t, &body)
		if body.Error.Code != "invalid_status" {
			t.Errorf("%s: code = %q, want invalid_status", c.state, body.Error.Code)
		}
		if body.Error.Message == "" {
			t.Errorf("%s: no message — this text is shown to the user verbatim", c.state)
		}
	}
}

// Case 6: unknown id.
func TestUnarchivingAnUnknownJourneyIsNotFound(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	res := h.do(http.MethodPost,
		"/v1/automation/journeys/00000000-0000-0000-0000-000000000000/unarchive",
		tenant.Token, nil)
	if res.Code != http.StatusNotFound {
		t.Fatalf("unknown id = %d, want 404\n%s", res.Code, res.Body)
	}
}

// Case 7: another tenant's journey is NOT FOUND, never forbidden — a 403 would
// confirm the journey exists.
func TestAnotherTenantsArchivedJourneyIsNotFoundNotForbidden(t *testing.T) {
	h := newHarness(t)
	owner := h.newAccount("owner")
	stranger := h.newAccount("owner")

	id := h.newJourney(owner.Token, "Theirs")
	if code := h.post(owner.Token, "/v1/automation/journeys/"+id+"/archive"); code != http.StatusOK {
		t.Fatalf("archive = %d", code)
	}

	res := h.do(http.MethodPost, "/v1/automation/journeys/"+id+"/unarchive", stranger.Token, nil)
	if res.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant unarchive = %d, want 404\n%s", res.Code, res.Body)
	}
	// And it did not move the other tenant's journey.
	if status, _ := h.journeyState(owner.Token, id); status != "archived" {
		t.Fatalf("the owner's journey is now %q — a stranger moved it", status)
	}
}

// Case 8: no bearer token.
func TestUnarchivingWithoutATokenIsUnauthenticated(t *testing.T) {
	h := newHarness(t)
	res := h.do(http.MethodPost,
		"/v1/automation/journeys/00000000-0000-0000-0000-000000000000/unarchive", "", nil)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401\n%s", res.Code, res.Body)
	}
}

// Case 9 and 10: not idempotent, and the round trip is clean.
func TestUnarchiveIsNotIdempotentAndRoundTripsCleanly(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	id := h.newJourney(tenant.Token, "Round trip")

	if code := h.post(tenant.Token, "/v1/automation/journeys/"+id+"/archive"); code != http.StatusOK {
		t.Fatalf("archive = %d", code)
	}
	if code := h.post(tenant.Token, "/v1/automation/journeys/"+id+"/unarchive"); code != http.StatusOK {
		t.Fatalf("first unarchive = %d", code)
	}
	// A second one matches no row: it is a state transition, not a desired-state
	// write, and saying so is what lets a double-click be reported.
	if code := h.post(tenant.Token, "/v1/automation/journeys/"+id+"/unarchive"); code != http.StatusUnprocessableEntity {
		t.Fatalf("second unarchive = %d, want 422", code)
	}
	// archive -> unarchive -> archive -> unarchive, all four succeed.
	for i, step := range []string{"archive", "unarchive", "archive", "unarchive"} {
		if code := h.post(tenant.Token, "/v1/automation/journeys/"+id+"/"+step); code != http.StatusOK {
			t.Fatalf("round trip step %d (%s) = %d", i+1, step, code)
		}
	}
}

// Case 11: a restored journey is genuinely usable again, not just relabelled.
func TestAJourneyRestoredToPausedCanBeResumed(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	id := h.newJourney(tenant.Token, "Resumable")

	h.post(tenant.Token, "/v1/automation/journeys/"+id+"/activate")
	h.post(tenant.Token, "/v1/automation/journeys/"+id+"/archive")
	if code := h.post(tenant.Token, "/v1/automation/journeys/"+id+"/unarchive"); code != http.StatusOK {
		t.Fatalf("unarchive = %d", code)
	}
	if code := h.post(tenant.Token, "/v1/automation/journeys/"+id+"/resume"); code != http.StatusOK {
		t.Fatalf("resume after unarchive = %d, want 200", code)
	}
	if status, _ := h.journeyState(tenant.Token, id); status != "active" {
		t.Fatalf("status = %q after resume, want active", status)
	}
}

// Case 12: a journey restored to draft is editable again.
func TestAJourneyRestoredToDraftCanBeEditedAgain(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	id := h.newJourney(tenant.Token, "Editable")

	h.post(tenant.Token, "/v1/automation/journeys/"+id+"/archive")
	if code := h.post(tenant.Token, "/v1/automation/journeys/"+id+"/unarchive"); code != http.StatusOK {
		t.Fatalf("unarchive = %d", code)
	}
	edited := h.do(http.MethodPatch, "/v1/automation/journeys/"+id, tenant.Token, map[string]any{
		"steps": []any{
			map[string]any{"type": "wait", "id": "step-1", "durationMinutes": 120},
		},
	})
	if edited.Code != http.StatusOK {
		t.Fatalf("editing steps on a restored draft = %d, want 200\n%s", edited.Code, edited.Body)
	}
}
