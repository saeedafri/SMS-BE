package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/saeedafri/sms-be/internal/store"
)

// Ask 46: India's telemarketer mapping, filed and approved like any other
// registration, and sitting between the principal entity and the header.

func (h *harness) operatorApprove(operator, id string) {
	h.t.Helper()
	if res := h.do(http.MethodPost, "/v1/operator/registrations/"+id+"/approve", operator, nil); res.Code != http.StatusOK {
		h.t.Fatalf("approve %s = %d\n%s", id, res.Code, res.Body)
	}
}

func fileMapping(h *harness, token string) response {
	return h.do(http.MethodPost, "/v1/registrations", token, map[string]any{
		"country": "IN", "objectKey": "tm_mapping", "fields": map[string]any{},
	})
}

func TestTheMappingNeedsAnApprovedPrincipalEntity(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")

	if res := fileMapping(h, acct.Token); res.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(string(res.Body), "Register Principal entity (PE/RTM) first.") {
		t.Errorf("no PE = %d %s, want 422 naming the PE", res.Code, res.Body)
	}

	h.createRegistration(acct.Token) // pending_review
	if res := fileMapping(h, acct.Token); res.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(string(res.Body), "must be approved first — it is currently pending_review.") {
		t.Errorf("PE pending = %d %s, want 422 naming its status", res.Code, res.Body)
	}
}

func TestAMappingIsFiledWithNoFieldsApprovedWithNoIdAndOnlyOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	operator := h.operatorToken()
	h.operatorApprove(operator, h.createRegistration(acct.Token))

	res := fileMapping(h, acct.Token)
	if res.Code != http.StatusCreated {
		t.Fatalf("file mapping = %d %s, want 201", res.Code, res.Body)
	}
	var mapping struct {
		ID             string         `json:"id"`
		Status         string         `json:"status"`
		RegistrationID *string        `json:"registrationId"`
		Fields         map[string]any `json:"fields"`
	}
	res.decode(t, &mapping)
	if mapping.Status != "pending_review" || mapping.RegistrationID != nil || len(mapping.Fields) != 0 {
		t.Errorf("mapping = %+v, want pending_review, no registrationId, no fields", mapping)
	}

	if again := fileMapping(h, acct.Token); again.Code != http.StatusConflict {
		t.Errorf("second mapping = %d %s, want 409", again.Code, again.Body)
	}

	h.operatorApprove(operator, mapping.ID)
	var list []struct {
		ObjectKey      string  `json:"objectKey"`
		Status         string  `json:"status"`
		RegistrationID *string `json:"registrationId"`
	}
	h.do(http.MethodGet, "/v1/registrations?country=IN", acct.Token, nil).decode(t, &list)
	found := false
	for _, r := range list {
		if r.ObjectKey == "tm_mapping" {
			found = true
			if r.Status != "approved" || r.RegistrationID != nil {
				t.Errorf("listed mapping = %+v, want approved with no registrationId", r)
			}
		}
	}
	if !found {
		t.Errorf("the approved mapping is not listed: %+v", list)
	}
}

func TestAHeaderCannotBeFiledBeforeTheMapping(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	h.operatorApprove(h.operatorToken(), h.createRegistration(acct.Token))

	res := h.do(http.MethodPost, "/v1/registrations", acct.Token, map[string]any{
		"country": "IN", "objectKey": "dlt_header",
		"fields": map[string]any{"header": "TEXTFI", "headerType": "transactional"},
	})
	if res.Code != http.StatusUnprocessableEntity || !strings.Contains(string(res.Body), "Telemarketer mapping") {
		t.Errorf("header before mapping = %d %s, want 422 naming the Telemarketer mapping", res.Code, res.Body)
	}
}

// A second entity-tier record must not change which PE id an SMS carries.
func TestAnApprovedMappingLeavesThePrincipalEntityIdOnEverySMS(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	acct := h.newAccount("owner")
	operator := h.operatorToken()
	pe := h.do(http.MethodPost, "/v1/registrations", acct.Token, map[string]any{
		"country": "IN", "objectKey": "pe_rtm_entity", "fields": indiaEntityFields(),
		"registrationId": "1701160157450894629",
	})
	var created struct {
		ID string `json:"id"`
	}
	pe.decode(t, &created)
	h.operatorApprove(operator, created.ID)
	var mapping struct {
		ID string `json:"id"`
	}
	fileMapping(h, acct.Token).decode(t, &mapping)
	h.operatorApprove(operator, mapping.ID)

	entityID, err := store.DLTEntityID(context.Background(), h.pool,
		store.Identity{TenantID: acct.TenantID}, "IN")
	if err != nil || entityID != "1701160157450894629" {
		t.Errorf("DLTEntityID = %q, %v; want the PE's id", entityID, err)
	}
}
