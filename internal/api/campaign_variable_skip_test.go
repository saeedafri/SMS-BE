package api_test

import (
	"fmt"
	"math/rand"
	"net/http"
	"testing"
)

// A campaign skips the contacts it cannot personalise, and says how many.
//
// Measured on the live API on 18 September 2026: of 1,000 contacts, exactly ONE
// had a first name. So a template saying "Dear {{firstName}}," would have put
// that literal text on 999 handsets in 1,000 — and nothing in the product, at
// any step, would have said a word. Part 2 asks 5 and 6.

type estimateBody struct {
	Recipients            int            `json:"recipients"`
	CostMinorMin          int            `json:"costMinorMin"`
	CostMinorMax          int            `json:"costMinorMax"`
	VariableSkipped       *int           `json:"variableSkipped"`
	FallbackForced        *int           `json:"fallbackForced"`
	FallbackEligible      int            `json:"fallbackEligible"`
	VariableSkippedByName map[string]int `json:"variableSkippedByName"`
}

func estimateFor(t *testing.T, h *harness, token string, body map[string]any) estimateBody {
	t.Helper()
	res := h.do(http.MethodPost, "/v1/campaigns/estimate", token, body)
	if res.Code != http.StatusOK {
		t.Fatalf("estimate = %d %s, want 200", res.Code, res.Body)
	}
	var out estimateBody
	res.decode(t, &out)
	return out
}

// seedSlotTemplate makes an approved SMS template whose body uses one slot.
func seedSlotTemplate(t *testing.T, h *harness, acct account, body string) string {
	t.Helper()
	ctx := t.Context()
	var senderID, templateID string
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO sender_ids (tenant_id, header, channel, country, status)
		VALUES ($1, $2, 'SMS', 'IN', 'approved') RETURNING id`,
		acct.TenantID, fmt.Sprintf("SKP%03d", h.nextSenderSeq())).Scan(&senderID); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO templates (tenant_id, sender_id, name, channel, country, body, status)
		VALUES ($1, $2, $3, 'SMS', 'IN', $4, 'approved') RETURNING id`,
		acct.TenantID, senderID, fmt.Sprintf("skip %d", rand.Int()), body).Scan(&templateID); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	return templateID
}

func TestAnEstimateExcludesTheContactsItCannotPersonaliseAndNamesTheSlot(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	list := createList(t, h, acct.Token, "Skip "+fmt.Sprint(rand.Int()))

	// Three contacts: one with the column, two without — the shape of the real
	// audience, where the exception is the one that CAN be personalised.
	importRows(t, h, acct.Token, list.Id.String(), []map[string]any{
		{"msisdn": number(), "fields": map[string]string{"First Name": "Rahul"}},
		{"msisdn": number(), "fields": map[string]string{"City": "Pune"}},
		{"msisdn": number(), "fields": map[string]string{"First Name": "   "}},
	}, "skip-"+fmt.Sprint(rand.Int()))

	listID := list.Id.String()
	// The estimate reads the body from the TEMPLATE, which is what a campaign
	// is actually sent with.
	slotted := seedSlotTemplate(t, h, acct, "Dear {{firstName}}, your registration is done.")
	estimate := map[string]any{
		"listId": listID, "country": "IN", "channel": "SMS", "templateId": slotted,
	}

	// With no mapping, the slot looks for a column spelled exactly "firstName"
	// and nobody has one. Every contact is skipped, and the cost is zero:
	// quoting for somebody who is never sent to is the same defect as sending
	// them a hole in a sentence, seen from the billing side.
	none := estimateFor(t, h, acct.Token, estimate)
	if none.Recipients != 0 || none.CostMinorMin != 0 || none.CostMinorMax != 0 {
		t.Errorf("unmapped estimate = %+v, want nobody and no cost", none)
	}
	if none.VariableSkipped == nil || *none.VariableSkipped != 3 {
		t.Errorf("variableSkipped = %v, want 3", none.VariableSkipped)
	}
	// "12 skipped" is a fact; "12 have no first name" is an instruction.
	if none.VariableSkippedByName["firstName"] != 3 {
		t.Errorf("byName = %+v, want firstName: 3", none.VariableSkippedByName)
	}

	// Now say which column feeds the slot.
	if res := h.do(http.MethodPatch, "/v1/contact-lists/"+listID, acct.Token,
		map[string]any{"variableMapping": map[string]string{"firstName": "First Name"}}); res.Code != http.StatusOK {
		t.Fatalf("set mapping = %d %s", res.Code, res.Body)
	}

	mapped := estimateFor(t, h, acct.Token, estimate)
	// One resolves. The whitespace-only one does NOT: a blank is not a value,
	// and substituting it delivers "Dear ," — a hole in the sentence that the
	// operator matches against the registered template none the wiser.
	if mapped.Recipients != 1 {
		t.Errorf("recipients = %d, want the 1 contact with a real name", mapped.Recipients)
	}
	if mapped.VariableSkipped == nil || *mapped.VariableSkipped != 2 {
		t.Errorf("variableSkipped = %v, want 2", mapped.VariableSkipped)
	}
	if mapped.CostMinorMin == 0 {
		t.Error("cost is zero for one real recipient")
	}

	// A body with no slots skips nobody and must not cost a walk of the list.
	plain := estimateFor(t, h, acct.Token, map[string]any{
		"listId": listID, "country": "IN", "channel": "SMS",
		"templateId": seedSlotTemplate(t, h, acct, "Your registration is done."),
	})
	if plain.Recipients != 3 {
		t.Errorf("a body with no slots reached %d, want all 3", plain.Recipients)
	}
	if plain.VariableSkipped == nil || *plain.VariableSkipped != 0 {
		t.Errorf("variableSkipped = %v, want a measured 0", plain.VariableSkipped)
	}
	if plain.VariableSkippedByName != nil {
		t.Errorf("byName = %+v, want it omitted when nothing was skipped", plain.VariableSkippedByName)
	}
}

// The mapping belongs to the list, and PATCH takes a name, a mapping, or both.
func TestAListCarriesWhichColumnFeedsWhichSlot(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	list := createList(t, h, acct.Token, "Mapping "+fmt.Sprint(rand.Int()))
	path := "/v1/contact-lists/" + list.Id.String()

	res := h.do(http.MethodPatch, path, acct.Token, map[string]any{
		"variableMapping": map[string]string{"firstName": "First Name", "orderId": "Order ID"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("patch mapping = %d %s", res.Code, res.Body)
	}
	var got struct {
		Name            string            `json:"name"`
		VariableMapping map[string]string `json:"variableMapping"`
	}
	res.decode(t, &got)
	if got.VariableMapping["firstName"] != "First Name" ||
		got.VariableMapping["orderId"] != "Order ID" {
		t.Errorf("mapping = %+v, want both slots", got.VariableMapping)
	}

	// A rename still works, and leaves the mapping alone.
	renamed := h.do(http.MethodPatch, path, acct.Token, map[string]any{"name": "Renamed"})
	renamed.decode(t, &got)
	if got.Name != "Renamed" || got.VariableMapping["firstName"] != "First Name" {
		t.Errorf("after rename = %+v, want the mapping kept", got)
	}

	for name, body := range map[string]any{
		"empty body":    map[string]any{},
		"blank name":    map[string]any{"name": "   "},
		"unknown field": map[string]any{"slots": map[string]string{}},
	} {
		if res := h.do(http.MethodPatch, path, acct.Token, body); res.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d %s, want 422", name, res.Code, res.Body)
		}
	}
	if res := h.do(http.MethodPatch, "/v1/contact-lists/6f1d1f6a-0000-4000-8000-000000000000",
		acct.Token, map[string]any{"name": "x"}); res.Code != http.StatusNotFound {
		t.Errorf("unknown list = %d, want 404", res.Code)
	}
}

// T13 and T16. The campaign estimate READS the fallback the wizard sends — it
// used to parse it and drop it — and fallbackForced is always on the response,
// as a measured zero when nothing falls back. Absent tells the screen this
// server does not apply the per-leg rule, which would now be false.
func TestTheCampaignEstimateReadsTheFallbackAndAlwaysStatesTheSplit(t *testing.T) {
	t.Parallel()
	h := newSendHarness(t)
	acct := h.newAccount("owner")
	list := createList(t, h, acct.Token, "Fallback "+fmt.Sprint(rand.Int()))
	importRows(t, h, acct.Token, list.Id.String(), []map[string]any{
		{"msisdn": number(), "fields": map[string]string{"firstName": "Rahul"}},
		{"msisdn": number(), "fields": map[string]string{"City": "Pune"}},
	}, "fallback-"+fmt.Sprint(rand.Int()))

	named := seedSlotTemplate(t, h, acct, "Dear {{firstName}}, your order shipped.")
	plain := seedSlotTemplate(t, h, acct, "Your order shipped.")

	alone := estimateFor(t, h, acct.Token, map[string]any{
		"listId": list.Id.String(), "country": "IN", "channel": "SMS", "templateId": named,
	})
	if alone.FallbackForced == nil || *alone.FallbackForced != 0 {
		t.Fatalf("fallbackForced = %v with no fallback, want a present 0", alone.FallbackForced)
	}
	if alone.Recipients != 1 {
		t.Fatalf("recipients = %d, want 1: the contact with no first name is skipped", alone.Recipients)
	}

	withFallback := estimateFor(t, h, acct.Token, map[string]any{
		"listId": list.Id.String(), "country": "IN", "channel": "SMS", "templateId": named,
		"fallback": map[string]any{"channel": "SMS", "senderId": "00000000-0000-0000-0000-000000000000", "templateId": plain},
	})
	if withFallback.FallbackForced == nil || *withFallback.FallbackForced != 1 {
		t.Fatalf("fallbackForced = %v, want 1: the fallback needs no name", withFallback.FallbackForced)
	}
	if withFallback.Recipients != 2 || withFallback.FallbackEligible != 2 {
		t.Fatalf("recipients %d eligible %d, want 2 and 2: the fallback carries who the primary cannot",
			withFallback.Recipients, withFallback.FallbackEligible)
	}
}
