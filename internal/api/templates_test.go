package api_test

import (
	"net/http"
	"reflect"
	"testing"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func TestCreateTemplateDerivesVariablesAndInheritsFromTheSender(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	sender := createSender(t, h, acct.Token, "TPLHDR", "SMS", "IN")

	res := h.do(http.MethodPost, "/v1/templates", acct.Token, map[string]any{
		"name":     "Order shipped",
		"senderId": sender.Id.String(),
		"body":     "Hi {{name}}, order {{order_id}} ships today. Thanks {{name}}!",
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", res.Code, res.Body)
	}
	var template gen.Template
	res.decode(t, &template)

	// Distinct, in first-seen order — the repeated {{name}} collapses.
	if want := []string{"name", "order_id"}; !reflect.DeepEqual(template.Variables, want) {
		t.Errorf("variables = %v, want %v", template.Variables, want)
	}
	// Channel and country come from the sender, never the request: a template
	// claiming a different country from its sender would be unenforceable.
	if template.Channel != gen.ChannelId("SMS") || template.Country != gen.CountryCode("IN") {
		t.Errorf("channel/country = %q/%q, want SMS/IN", template.Channel, template.Country)
	}
	if template.Status != gen.ApprovalStatus("pending_review") {
		t.Errorf("status = %q, want pending_review", template.Status)
	}
	if template.SenderId != sender.Id {
		t.Errorf("senderId = %s, want %s", template.SenderId, sender.Id)
	}
}

func TestCreateTemplateRejectsMalformedVariables(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	sender := createSender(t, h, acct.Token, "BADVAR", "SMS", "IN")

	for _, body := range []string{"Hi {{bad-name}}", "Hi {{unclosed", "Hi {{}}"} {
		res := h.do(http.MethodPost, "/v1/templates", acct.Token, map[string]any{
			"name": "T " + body, "senderId": sender.Id.String(), "body": body,
		})
		if res.Code != http.StatusUnprocessableEntity {
			t.Errorf("body %q: status = %d, want 422; body = %s", body, res.Code, res.Body)
		}
	}
}

// India has disallowed public URL shorteners under DLT since Oct 2024. This is
// the authoritative check — the frontend validating too is a convenience.
func TestIndiaTemplateRejectsShortenedCtaUrl(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	sender := createSender(t, h, acct.Token, "CTAHDR", "SMS", "IN")

	res := h.do(http.MethodPost, "/v1/templates", acct.Token, map[string]any{
		"name": "Shortened", "senderId": sender.Id.String(),
		"body": "Offer inside", "ctaUrl": "https://bit.ly/deal",
	})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", res.Code, res.Body)
	}

	full := h.do(http.MethodPost, "/v1/templates", acct.Token, map[string]any{
		"name": "Full URL", "senderId": sender.Id.String(),
		"body": "Offer inside", "ctaUrl": "https://acme.example/deal",
	})
	if full.Code != http.StatusCreated {
		t.Fatalf("full URL: status = %d, want 201; body = %s", full.Code, full.Body)
	}
}

// The same URL under a US sender must be accepted: 10DLC carries no shortener
// rule, and the regime — not the handler — decides.
func TestUSTemplateAllowsShortenedCtaUrl(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	sender := createSender(t, h, acct.Token, "USHDR", "SMS", "US")

	res := h.do(http.MethodPost, "/v1/templates", acct.Token, map[string]any{
		"name": "US shortened", "senderId": sender.Id.String(),
		"body": "Offer inside", "ctaUrl": "https://bit.ly/deal",
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", res.Code, res.Body)
	}
}

// Referencing another tenant's sender must not 404: a 404 would confirm the id
// exists somewhere. 422 says only "that sender does not exist" from the
// caller's point of view, which is true.
func TestTemplateCannotReferenceAnotherTenantsSender(t *testing.T) {
	h := newHarness(t)
	owner := h.newAccount("owner")
	other := h.newAccount("owner")
	sender := createSender(t, h, other.Token, "OTHERS", "SMS", "IN")

	res := h.do(http.MethodPost, "/v1/templates", owner.Token, map[string]any{
		"name": "Borrowed sender", "senderId": sender.Id.String(), "body": "Hello",
	})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", res.Code, res.Body)
	}
	if code := res.errorCode(t); code != "validation_failed" {
		t.Fatalf("error.code = %q, want validation_failed", code)
	}
}

func TestCreateTemplateRejectsDuplicateNames(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	sender := createSender(t, h, acct.Token, "DUPTPL", "SMS", "IN")

	body := map[string]any{"name": "Same name", "senderId": sender.Id.String(), "body": "Hi"}
	if first := h.do(http.MethodPost, "/v1/templates", acct.Token, body); first.Code != http.StatusCreated {
		t.Fatalf("first: status = %d", first.Code)
	}
	second := h.do(http.MethodPost, "/v1/templates", acct.Token, body)
	if second.Code != http.StatusConflict {
		t.Fatalf("duplicate: status = %d, want 409; body = %s", second.Code, second.Body)
	}
}

func TestTemplateListAndGetAreTenantScoped(t *testing.T) {
	h := newHarness(t)
	owner := h.newAccount("owner")
	other := h.newAccount("owner")
	sender := createSender(t, h, owner.Token, "SCOPED", "SMS", "IN")

	created := h.do(http.MethodPost, "/v1/templates", owner.Token, map[string]any{
		"name": "Scoped template", "senderId": sender.Id.String(), "body": "Hi {{name}}",
	})
	var template gen.Template
	created.decode(t, &template)

	if res := h.do(http.MethodGet, "/v1/templates/"+template.Id.String(),
		other.Token, nil); res.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant get: status = %d, want 404", res.Code)
	}

	res := h.do(http.MethodGet, "/v1/templates", other.Token, nil)
	var templates []gen.Template
	res.decode(t, &templates)
	for _, item := range templates {
		if item.Id == template.Id {
			t.Fatal("another tenant's template appeared in the list")
		}
	}
}

func TestTemplateEndpointsRespectRole(t *testing.T) {
	h := newHarness(t)
	owner := h.newAccount("owner")
	member := h.newAccount("member")
	sender := createSender(t, h, owner.Token, "ROLEHD", "SMS", "IN")

	res := h.do(http.MethodPost, "/v1/templates", member.Token, map[string]any{
		"name": "Member template", "senderId": sender.Id.String(), "body": "Hi",
	})
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", res.Code, res.Body)
	}
}

// A template with no body still needs `variables` present as an empty array —
// the contract makes it required, and the UI maps over it.
func TestTemplateWithoutABodyStillReportsEmptyVariables(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")
	sender := createSender(t, h, acct.Token, "NOBODY", "RCS", "IN")

	res := h.do(http.MethodPost, "/v1/templates", acct.Token, map[string]any{
		"name": "No body", "senderId": sender.Id.String(),
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", res.Code, res.Body)
	}
	var template gen.Template
	res.decode(t, &template)
	if template.Variables == nil {
		t.Fatal("variables is null, want an empty array")
	}
	if len(template.Variables) != 0 {
		t.Fatalf("variables = %v, want empty", template.Variables)
	}
}
