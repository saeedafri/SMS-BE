package api_test

import (
	"net/http"
	"testing"
)

// A submitted compliance registration must be reachable by an operator.
//
// It was not. GetApprovalQueue merged senders and templates and nothing else, and
// no endpoint anywhere could change a registration's status. So a customer who
// submitted a DLT or EIN registration saw "in review" forever: no screen listed
// it, no operator could act on it, and because compliance approval is what
// unblocks sending in that country, that tenant could never send a message.
//
// The reason 263 browser tests stayed green through all of it is worth writing
// down: the suite advances registrations with the /v1/dev/advance-registration
// hook, which stood in for an operator workflow that had never been built. A
// test hook that substitutes for a missing feature makes the gap invisible
// exactly where you would look for it.
func TestApprovalQueueIncludesRegistrations(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()

	created := h.do(http.MethodPost, "/v1/registrations", tenant.Token, map[string]any{
		"country": "IN", "objectKey": "pe_rtm_entity", "fields": indiaEntityFields(),
	})
	if created.Code != http.StatusCreated && created.Code != http.StatusOK {
		t.Fatalf("create registration = %d\n%s", created.Code, created.Body)
	}
	var registration struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	created.decode(t, &registration)

	queue := h.do(http.MethodGet, "/v1/operator/approvals", operator, nil)
	if queue.Code != http.StatusOK {
		t.Fatalf("approval queue = %d\n%s", queue.Code, queue.Body)
	}
	var page struct {
		Items []struct {
			ID         string         `json:"id"`
			ItemType   string         `json:"itemType"`
			TenantName string         `json:"tenantName"`
			ObjectKey  string         `json:"objectKey"`
			Fields     map[string]any `json:"fields"`
		} `json:"items"`
	}
	queue.decode(t, &page)

	for _, item := range page.Items {
		if item.ID != registration.ID {
			continue
		}
		if item.ItemType != "registration" {
			t.Errorf("itemType = %q, want registration", item.ItemType)
		}
		if item.TenantName == "" {
			t.Error("tenantName empty — the operator cannot tell whose submission this is")
		}
		// The submitted values are the point of the review: an operator judging
		// a DLT entity needs the PAN and entity name in front of them, not just
		// the fact that something was submitted.
		if len(item.Fields) == 0 {
			t.Error("fields empty — there is nothing to review")
		}
		return
	}
	t.Fatalf("registration %s is not in the operator approval queue; the customer "+
		"sees 'in review' and nobody can act on it", registration.ID)
}

func TestOperatorCanApproveARegistrationAndTheTenantSeesIt(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()
	id := h.createRegistration(tenant.Token)

	approved := h.do(http.MethodPost, "/v1/operator/registrations/"+id+"/approve", operator, nil)
	if approved.Code != http.StatusOK {
		t.Fatalf("approve = %d\n%s", approved.Code, approved.Body)
	}

	// The half that matters: approval exists to unblock the tenant, so the
	// tenant's own view has to change, not just the operator's.
	var seen struct {
		Status string `json:"status"`
	}
	h.do(http.MethodGet, "/v1/registrations/"+id, tenant.Token, nil).decode(t, &seen)
	if seen.Status != "approved" {
		t.Fatalf("the tenant still sees %q after the operator approved it", seen.Status)
	}
}

func TestRejectingARegistrationNeedsAReasonTheCustomerCanActOn(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	operator := h.operatorToken()
	id := h.createRegistration(tenant.Token)

	// A rejection the customer cannot act on is worse than none: they resubmit
	// the same thing and wait again.
	blank := h.do(http.MethodPost, "/v1/operator/registrations/"+id+"/reject", operator,
		map[string]any{"reason": ""})
	if blank.Code != http.StatusUnprocessableEntity {
		t.Fatalf("reject with no reason = %d, want 422\n%s", blank.Code, blank.Body)
	}

	rejected := h.do(http.MethodPost, "/v1/operator/registrations/"+id+"/reject", operator,
		map[string]any{"reason": "PAN does not match the registered entity name."})
	if rejected.Code != http.StatusOK {
		t.Fatalf("reject = %d\n%s", rejected.Code, rejected.Body)
	}

	var seen struct {
		Status          string  `json:"status"`
		RejectionReason *string `json:"rejectionReason"`
	}
	h.do(http.MethodGet, "/v1/registrations/"+id, tenant.Token, nil).decode(t, &seen)
	if seen.Status != "rejected" {
		t.Fatalf("tenant sees %q, want rejected", seen.Status)
	}
	if seen.RejectionReason == nil || *seen.RejectionReason == "" {
		t.Fatal("the tenant is told no, and not why")
	}
}

// A tenant must not be able to approve their own compliance submission.
func TestARegistrationDecisionIsOperatorOnly(t *testing.T) {
	h := newHarness(t)
	tenant := h.newAccount("owner")
	id := h.createRegistration(tenant.Token)

	res := h.do(http.MethodPost, "/v1/operator/registrations/"+id+"/approve", tenant.Token, nil)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("tenant approving their own registration = %d, want 401\n%s",
			res.Code, res.Body)
	}
}
