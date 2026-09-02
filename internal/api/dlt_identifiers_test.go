package api_test

import (
	"fmt"
	"net/http"
	"testing"
)

// A DLT identifier is issued by DLT, to the customer, on their own operator
// portal. Relay is the system of record for it and never its issuer.
//
// Before this, approval minted one: a database trigger wrote
// 'REG-' || upper(header) || '-0001' the moment a sender or registration
// reached `approved`, and the customer's screen showed an official-looking id
// that no operator had ever seen. That is worse than a blank field — it lets a
// tenant submit under a content-template id that does not exist on DLT, which
// is how a telemarketer registration gets pulled.
func TestASuppliedDltIdSurvivesApprovalByteForByte(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	operator := h.operatorToken()

	const supplied = "1234567890123456789"
	created := h.do(http.MethodPost, "/v1/sender-ids", acct.Token, map[string]any{
		"header": "ACMERT", "channel": "SMS", "country": "IN",
		"registrationId": supplied,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create sender = %d\n%s", created.Code, created.Body)
	}
	var sender struct {
		ID             string  `json:"id"`
		RegistrationID *string `json:"registrationId"`
	}
	created.decode(t, &sender)

	// It must round-trip before approval, or the rest of the test proves nothing.
	if sender.RegistrationID == nil || *sender.RegistrationID != supplied {
		t.Fatalf("registrationId on create = %v, want %q", sender.RegistrationID, supplied)
	}

	approve := h.do(http.MethodPost,
		fmt.Sprintf("/v1/operator/senders/%s/approve", sender.ID), operator, nil)
	if approve.Code != http.StatusOK {
		t.Fatalf("approve = %d\n%s", approve.Code, approve.Body)
	}

	after := h.do(http.MethodGet, "/v1/sender-ids", acct.Token, nil)
	var list []struct {
		ID             string  `json:"id"`
		Status         string  `json:"status"`
		RegistrationID *string `json:"registrationId"`
	}
	after.decode(t, &list)
	for _, row := range list {
		if row.ID != sender.ID {
			continue
		}
		if row.Status != "approved" {
			t.Fatalf("status = %q, want approved", row.Status)
		}
		if row.RegistrationID == nil {
			t.Fatal("registrationId was cleared by approval")
		}
		if *row.RegistrationID != supplied {
			t.Fatalf("registrationId after approval = %q, want %q — approval rewrote "+
				"a customer's DLT id", *row.RegistrationID, supplied)
		}
		return
	}
	t.Fatalf("sender %s vanished from the list", sender.ID)
}

// No id supplied means no id, forever — not "until approval mints one".
func TestApprovalNeverInventsADltId(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	operator := h.operatorToken()

	created := h.do(http.MethodPost, "/v1/sender-ids", acct.Token, map[string]any{
		"header": "NOIDXX", "channel": "SMS", "country": "IN",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create sender = %d\n%s", created.Code, created.Body)
	}
	var sender struct {
		ID string `json:"id"`
	}
	created.decode(t, &sender)

	if approve := h.do(http.MethodPost,
		fmt.Sprintf("/v1/operator/senders/%s/approve", sender.ID), operator, nil); approve.Code != http.StatusOK {
		t.Fatalf("approve = %d\n%s", approve.Code, approve.Body)
	}

	after := h.do(http.MethodGet, "/v1/sender-ids", acct.Token, nil)
	var list []struct {
		ID             string  `json:"id"`
		RegistrationID *string `json:"registrationId"`
	}
	after.decode(t, &list)
	for _, row := range list {
		if row.ID != sender.ID {
			continue
		}
		if row.RegistrationID != nil && *row.RegistrationID != "" {
			t.Fatalf("approval invented registrationId %q — Relay has no authority "+
				"to issue a DLT id", *row.RegistrationID)
		}
		return
	}
	t.Fatalf("sender %s vanished from the list", sender.ID)
}

// The id the customer types lives in the typed column, and only there.
//
// The regime owns the shape of the fields bag, so that is where the id is
// entered. Leaving a copy behind would give one value two homes that a later
// edit could pull apart, and every reader downstream uses the column.
func TestRegistrationLiftsTheDltIdOutOfTheFieldsBag(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	const supplied = "9876543210987654321"
	created := h.do(http.MethodPost, "/v1/registrations", acct.Token, map[string]any{
		"country": "IN", "objectKey": "pe_rtm_entity",
		"fields": map[string]any{
			"registrationId": supplied,
			"legalName":      "Acme Retail Pvt Ltd",
			"pan":            "ABCDE1234F",
			"entityType":     "private_ltd",
			"contactEmail":   "compliance@acme.test",
		},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create registration = %d\n%s", created.Code, created.Body)
	}
	var registration struct {
		RegistrationID *string        `json:"registrationId"`
		Fields         map[string]any `json:"fields"`
	}
	created.decode(t, &registration)

	if registration.RegistrationID == nil || *registration.RegistrationID != supplied {
		t.Fatalf("typed registrationId = %v, want %q", registration.RegistrationID, supplied)
	}
	if _, duplicated := registration.Fields["registrationId"]; duplicated {
		t.Errorf("registrationId is still in the fields bag as well as the column — "+
			"two homes for one value: %v", registration.Fields["registrationId"])
	}
	// The rest of the bag must survive the lift.
	if registration.Fields["pan"] != "ABCDE1234F" {
		t.Errorf("lifting the id disturbed the rest of the bag: %v", registration.Fields)
	}
}

// The console sends registrationId at the top level of the body, not inside
// the fields bag. Before the contract carried it there, encoding/json dropped
// it without a word: the customer typed their DLT id, the form said saved, and
// the column stayed null.
func TestRegistrationAcceptsATopLevelDltId(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	const supplied = "1102345678901234567"
	created := h.do(http.MethodPost, "/v1/registrations", acct.Token, map[string]any{
		"country": "IN", "objectKey": "pe_rtm_entity",
		"registrationId": supplied,
		"fields": map[string]any{
			"legalName":    "Acme Retail Pvt Ltd",
			"pan":          "ABCDE1234F",
			"entityType":   "private_ltd",
			"contactEmail": "compliance@acme.test",
		},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create registration = %d\n%s", created.Code, created.Body)
	}
	var registration struct {
		RegistrationID *string `json:"registrationId"`
	}
	created.decode(t, &registration)

	if registration.RegistrationID == nil || *registration.RegistrationID != supplied {
		t.Fatalf("typed registrationId = %v, want %q", registration.RegistrationID, supplied)
	}
}

// DLT's taxonomy is not Meta's, and a value outside it is refused rather than
// stored. A mis-filed template is not rejected by us — it is scrubbed by the
// carrier, after the customer believes they are live.
func TestAnInvalidDltCategoryIsRefused(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	sender := h.do(http.MethodPost, "/v1/sender-ids", acct.Token, map[string]any{
		"header": "CATTST", "channel": "SMS", "country": "IN",
	})
	if sender.Code != http.StatusCreated {
		t.Fatalf("create sender = %d\n%s", sender.Code, sender.Body)
	}
	var created struct {
		ID string `json:"id"`
	}
	sender.decode(t, &created)

	bad := h.do(http.MethodPost, "/v1/templates", acct.Token, map[string]any{
		"name": "dlt-bad-category", "senderId": created.ID,
		"body": "Hi {{name}}", "dltCategory": "NOT_A_REAL_CATEGORY",
	})
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid dltCategory = %d, want 422\n%s", bad.Code, bad.Body)
	}

	// UTILITY is valid in Meta's taxonomy and absent from DLT's. Accepting it
	// here is the exact confusion the two enums exist to prevent.
	utility := h.do(http.MethodPost, "/v1/templates", acct.Token, map[string]any{
		"name": "dlt-utility", "senderId": created.ID,
		"body": "Hi {{name}}", "dltCategory": "UTILITY",
	})
	if utility.Code != http.StatusUnprocessableEntity {
		t.Fatalf("dltCategory=UTILITY = %d, want 422 — Meta's taxonomy leaked into "+
			"DLT's\n%s", utility.Code, utility.Body)
	}

	good := h.do(http.MethodPost, "/v1/templates", acct.Token, map[string]any{
		"name": "dlt-good", "senderId": created.ID,
		"body": "Hi {{name}}", "dltCategory": "SERVICE_IMPLICIT",
		"registrationId": "1107161234567890123",
	})
	if good.Code != http.StatusCreated {
		t.Fatalf("valid dltCategory = %d, want 201\n%s", good.Code, good.Body)
	}
	var template struct {
		DltCategory    *string `json:"dltCategory"`
		RegistrationID *string `json:"registrationId"`
	}
	good.decode(t, &template)
	if template.DltCategory == nil || *template.DltCategory != "SERVICE_IMPLICIT" {
		t.Errorf("dltCategory round-trip = %v, want SERVICE_IMPLICIT", template.DltCategory)
	}
	if template.RegistrationID == nil || *template.RegistrationID != "1107161234567890123" {
		t.Errorf("template registrationId round-trip = %v", template.RegistrationID)
	}
}
