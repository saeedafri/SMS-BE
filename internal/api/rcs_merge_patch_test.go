package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// JSON Merge Patch on an agent: an omitted key leaves the stored value alone,
// an explicit null clears it.
//
// The two are the SAME nil pointer in the generated request struct, so nothing
// in the typed body can tell them apart. Without the middleware's record of
// which keys the request actually carried, "clear my agent's website" is
// indistinguishable from "change the phone number and leave everything else",
// and there would be no way at all to empty an optional field once set.
func TestClearingAnAgentFieldIsDistinctFromOmittingIt(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	h.approveRegistration(acct, "IN")
	agent := h.createAgent(acct, "Acme Orders")

	filled := h.patchAgent(acct, agent.Id, `{
		"website": "https://acme.test",
		"phoneNumber": "+919876500001",
		"description": "Order notifications"
	}`)
	if filled["website"] != "https://acme.test" {
		t.Fatalf("website did not save: %v", filled["website"])
	}

	// Omission. Two fields untouched by a patch that mentions neither.
	kept := h.patchAgent(acct, agent.Id, `{"description":"Order updates"}`)
	if kept["website"] != "https://acme.test" {
		t.Errorf("website = %v after a patch that never mentioned it — "+
			"an omitted key must leave the stored value alone", kept["website"])
	}
	if kept["phoneNumber"] != "+919876500001" {
		t.Errorf("phoneNumber = %v after a patch that never mentioned it", kept["phoneNumber"])
	}

	// Explicit null. The same nil pointer, the opposite instruction.
	cleared := h.patchAgent(acct, agent.Id, `{"website":null}`)
	if cleared["website"] != nil {
		t.Errorf("website = %v after an explicit null — it should be gone", cleared["website"])
	}
	if cleared["phoneNumber"] != "+919876500001" {
		t.Errorf("phoneNumber = %v — clearing one field cleared another",
			cleared["phoneNumber"])
	}
	if cleared["description"] != "Order updates" {
		t.Errorf("description = %v — clearing one field cleared another",
			cleared["description"])
	}
}

// The two fields a null must NOT clear. Neither is nullable in the table, and
// an agent with no display name is not an agent — so a null there is a caller
// mistake, and silently emptying the name the handset draws would be the worst
// possible reading of it.
func TestANullCannotEmptyAnAgentsNameOrUseCase(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	h.approveRegistration(acct, "IN")
	agent := h.createAgent(acct, "Acme Orders")

	after := h.patchAgent(acct, agent.Id, `{"displayName":null,"useCase":null}`)
	if after["displayName"] != "Acme Orders" {
		t.Errorf("displayName = %v, want it untouched", after["displayName"])
	}
	if after["useCase"] != "TRANSACTIONAL" {
		t.Errorf("useCase = %v, want it untouched", after["useCase"])
	}
}

// patchAgent sends a RAW body rather than a marshalled map, because the whole
// distinction under test is one a Go map cannot express: a key present with a
// nil value and a key absent both marshal to the same thing.
func (h *harness) patchAgent(acct account, agentID, body string) map[string]any {
	h.t.Helper()
	res := h.doRaw(http.MethodPatch, "/v1/rcs/agents/"+agentID, acct.Token,
		"application/json", []byte(body))
	if res.Code != http.StatusOK {
		h.t.Fatalf("patch agent = %d: %s", res.Code, res.Body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(res.Body, &decoded); err != nil {
		h.t.Fatalf("decode agent: %v", err)
	}
	return decoded
}
