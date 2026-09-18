package api_test

import (
	"net/http"
	"testing"
	"time"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// Ask 33. A template the carrier rejected can be fixed and submitted again, and
// the new submission replaces the rejected registration. While a registration
// is pending it cannot be submitted a second time.
func TestATemplateTheCarrierRejectedCanBeSubmittedAgain(t *testing.T) {
	t.Parallel()
	carrier := &stubRegistrar{vendor: "airtel", issued: "01kresubmitfirst000000000a"}
	h := newCarrierHarness(t, carrier)
	tenant := h.newAccount("owner")
	templateID := h.rcsTemplate(tenant, "Resubmit after rejection", []string{"first_name"}, "UTILITY")
	path := "/v1/templates/" + templateID.String() + "/carrier-registration"
	submit := func() response {
		return h.do(http.MethodPost, path, tenant.Token, map[string]any{"vendor": carrier.vendor})
	}

	first := submit()
	if first.Code != http.StatusOK {
		t.Fatalf("first submit = %d\n%s", first.Code, first.Body)
	}
	var firstRegistration gen.CarrierTemplateRegistration
	first.decode(t, &firstRegistration)

	if res := submit(); res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("submit while pending = %d, want 422\n%s", res.Code, res.Body)
	}

	if res := h.postWebhook("airtel", webhookToken, map[string]any{
		"messageId": carrier.issued, "templateId": carrier.issued,
		"eventType": "TEMPLATE_REJECTED",
		"messageContent": map[string]any{"templateStatus": "REJECTED",
			"templateId": carrier.issued, "rejectionReason": "Variable misuse."},
	}); res.Code != http.StatusOK {
		t.Fatalf("rejection webhook = %d\n%s", res.Code, res.Body)
	}

	time.Sleep(10 * time.Millisecond)
	carrier.issued = "01kresubmitsecond00000000b"
	second := submit()
	if second.Code != http.StatusOK {
		t.Fatalf("submit after rejection = %d, want 200\n%s", second.Code, second.Body)
	}
	var registration gen.CarrierTemplateRegistration
	second.decode(t, &registration)
	if registration.Status != "pending" {
		t.Errorf("status = %q, want pending", registration.Status)
	}
	if registration.CarrierTemplateId == nil || *registration.CarrierTemplateId != carrier.issued {
		t.Errorf("carrierTemplateId = %v, want the new id %s", registration.CarrierTemplateId, carrier.issued)
	}
	if registration.RejectionReason != nil {
		t.Errorf("rejectionReason = %q, want cleared", *registration.RejectionReason)
	}
	if registration.SubmittedAt == nil || firstRegistration.SubmittedAt == nil ||
		!registration.SubmittedAt.After(*firstRegistration.SubmittedAt) {
		t.Errorf("submittedAt = %v after first %v, want the resubmission's time",
			registration.SubmittedAt, firstRegistration.SubmittedAt)
	}
	if carrier.calls != 2 {
		t.Errorf("carrier saw %d submissions, want 2", carrier.calls)
	}
}
