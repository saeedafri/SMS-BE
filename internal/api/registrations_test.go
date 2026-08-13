package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func indiaEntityFields() map[string]any {
	return map[string]any{
		"legalName":    "Acme Pvt Ltd",
		"pan":          "ABCDE1234F",
		"entityType":   "private_ltd",
		"contactEmail": "compliance@acme.example",
	}
}

func TestCreateRegistrationStartsPendingReviewAndEchoesFields(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPost, "/v1/registrations", acct.Token, map[string]any{
		"country": "IN", "objectKey": "pe_rtm_entity", "fields": indiaEntityFields(),
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", res.Code, res.Body)
	}
	var registration gen.Registration
	res.decode(t, &registration)

	if registration.Status != gen.ApprovalStatus("pending_review") {
		t.Errorf("status = %q, want pending_review", registration.Status)
	}
	if registration.ObjectKey != "pe_rtm_entity" {
		t.Errorf("objectKey = %q, want pe_rtm_entity", registration.ObjectKey)
	}
	if registration.Fields["pan"] != "ABCDE1234F" {
		t.Errorf("fields were not echoed back: %v", registration.Fields)
	}
	if registration.CreatedAt.IsZero() || registration.UpdatedAt.IsZero() {
		t.Error("createdAt/updatedAt are zero")
	}
}

func TestCreateRegistrationRejectsUnknownObjectKey(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPost, "/v1/registrations", acct.Token, map[string]any{
		"country": "IN", "objectKey": "not_a_real_object", "fields": map[string]any{},
	})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", res.Code, res.Body)
	}
}

// The UI needs to know which input to highlight, so every missing required
// field is named.
func TestCreateRegistrationNamesMissingRequiredFields(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPost, "/v1/registrations", acct.Token, map[string]any{
		"country": "IN", "objectKey": "pe_rtm_entity",
		"fields": map[string]any{"legalName": "Acme Pvt Ltd", "pan": "   "},
	})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", res.Code, res.Body)
	}
	body := string(res.Body)
	for _, key := range []string{"pan", "entityType", "contactEmail"} {
		if !contains(body, key) {
			t.Errorf("message does not name the missing field %q: %s", key, body)
		}
	}
}

func TestCreateRegistrationRejectsDuplicates(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	body := map[string]any{
		"country": "IN", "objectKey": "pe_rtm_entity", "fields": indiaEntityFields(),
	}

	if first := h.do(http.MethodPost, "/v1/registrations", acct.Token, body); first.Code != http.StatusCreated {
		t.Fatalf("first: status = %d", first.Code)
	}
	second := h.do(http.MethodPost, "/v1/registrations", acct.Token, body)
	if second.Code != http.StatusConflict {
		t.Fatalf("duplicate: status = %d, want 409; body = %s", second.Code, second.Body)
	}
}

// A stub regime exists but registers nothing. That is a different answer from
// "we do not operate there", and the user can act on the difference.
func TestStubRegimeRejectsRegistrationsDistinctly(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPost, "/v1/registrations", acct.Token, map[string]any{
		"country": "GB", "objectKey": "anything", "fields": map[string]any{},
	})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", res.Code, res.Body)
	}
	if !contains(string(res.Body), "United Kingdom") {
		t.Errorf("message does not name the regime: %s", res.Body)
	}
}

// The US campaign cannot be filed before its brand is approved. This ordering
// lives on the registration object, so the handler never mentions the US.
func TestUSCampaignRequiresAnApprovedBrand(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	campaign := map[string]any{
		"country": "US", "objectKey": "tcr_campaign",
		"fields": map[string]any{
			"useCase": "2fa", "description": "OTP delivery",
			"sampleMessage": "Your Acme code is 123456. Reply STOP to opt out.",
		},
	}

	t.Run("before the brand exists", func(t *testing.T) {
		res := h.do(http.MethodPost, "/v1/registrations", acct.Token, campaign)
		if res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422; body = %s", res.Code, res.Body)
		}
		if !contains(string(res.Body), "TCR brand") {
			t.Errorf("message does not name the dependency: %s", res.Body)
		}
	})

	brand := h.do(http.MethodPost, "/v1/registrations", acct.Token, map[string]any{
		"country": "US", "objectKey": "tcr_brand",
		"fields": map[string]any{
			"legalName": "Acme Inc", "website": "https://acme.example",
			"supportEmail": "support@acme.example",
		},
	})
	if brand.Code != http.StatusCreated {
		t.Fatalf("create brand: status = %d; body = %s", brand.Code, brand.Body)
	}

	t.Run("while the brand is still pending", func(t *testing.T) {
		res := h.do(http.MethodPost, "/v1/registrations", acct.Token, campaign)
		if res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422; body = %s", res.Code, res.Body)
		}
		if !contains(string(res.Body), "pending_review") {
			t.Errorf("message does not report the dependency's state: %s", res.Body)
		}
	})

	// Approval is the operator's job (Stage 9); until then, approve directly.
	var brandBody gen.Registration
	brand.decode(t, &brandBody)
	if _, err := h.admin.Exec(context.Background(),
		`UPDATE registrations SET status = 'approved' WHERE id = $1`, brandBody.Id); err != nil {
		t.Fatalf("approve brand: %v", err)
	}

	t.Run("once the brand is approved", func(t *testing.T) {
		res := h.do(http.MethodPost, "/v1/registrations", acct.Token, campaign)
		if res.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body = %s", res.Code, res.Body)
		}
	})
}

func TestRegistrationsRespectRoleAndTenant(t *testing.T) {
	h := newHarness(t)
	owner := h.newAccount("owner")
	member := h.newAccount("member")
	other := h.newAccount("owner")

	created := h.do(http.MethodPost, "/v1/registrations", owner.Token, map[string]any{
		"country": "IN", "objectKey": "pe_rtm_entity", "fields": indiaEntityFields(),
	})
	var registration gen.Registration
	created.decode(t, &registration)

	t.Run("member is forbidden", func(t *testing.T) {
		res := h.do(http.MethodPost, "/v1/registrations", member.Token, map[string]any{
			"country": "IN", "objectKey": "pe_rtm_entity", "fields": indiaEntityFields(),
		})
		if res.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body = %s", res.Code, res.Body)
		}
		if code := res.errorCode(t); code != "forbidden" {
			t.Fatalf("error.code = %q, want forbidden", code)
		}
	})

	t.Run("another tenant cannot read it", func(t *testing.T) {
		res := h.do(http.MethodGet, "/v1/registrations/"+registration.Id.String(),
			other.Token, nil)
		if res.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body = %s", res.Code, res.Body)
		}
	})

	t.Run("another tenant's list excludes it", func(t *testing.T) {
		res := h.do(http.MethodGet, "/v1/registrations", other.Token, nil)
		var registrations []gen.Registration
		res.decode(t, &registrations)
		for _, item := range registrations {
			if item.Id == registration.Id {
				t.Fatal("another tenant's registration appeared in the list")
			}
		}
	})
}

func TestGetRegistrationReturns404ForAnUnknownId(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodGet, "/v1/registrations/"+uuid.New().String(), acct.Token, nil)
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", res.Code, res.Body)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
