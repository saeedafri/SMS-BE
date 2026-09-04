package messaging_test

import (
	"testing"

	"github.com/saeedafri/sms-be/internal/domain/messaging"
)

// The rule an Indian operator applies: the fixed text around the variables is
// what DLT approved, and it is what gets matched. Everything here is a case
// that either arrives or is dropped on a real route.
func TestMatchesTemplate(t *testing.T) {
	cases := []struct {
		name     string
		template string
		body     string
		want     bool
	}{
		{"exact, no variables",
			"Your order has shipped.", "Your order has shipped.", true},
		{"no variables, different text",
			"Your order has shipped.", "Your order is late.", false},
		{"variable filled",
			"Hi {{name}}, your order {{id}} has shipped.",
			"Hi Priya, your order 4821 has shipped.", true},
		{"variable left unfilled is still the template's own text",
			"Hi {{name}}, your order {{id}} has shipped.",
			"Hi {{name}}, your order {{id}} has shipped.", true},
		{"fixed text altered",
			"Hi {{name}}, your order {{id}} has shipped.",
			"Hi Priya, your order 4821 has been cancelled.", false},

		// The case the document calls out by name: a substring check would pass
		// this, and the operator would drop it.
		{"opens with the template and then says something else",
			"Hi {{name}}, your order has shipped.",
			"Hi Priya, your order has shipped. Also WIN FREE CASH NOW", false},
		{"prefixed with something else",
			"Your OTP is {{code}}.",
			"URGENT! Your OTP is 123456.", false},

		{"variable at the start",
			"{{code}} is your verification code.",
			"558213 is your verification code.", true},
		{"variable at the start, wrong tail",
			"{{code}} is your verification code.",
			"558213 is your password.", false},
		{"variable at the end",
			"Your code is {{code}}",
			"Your code is 558213", true},
		{"empty variable value",
			"Hi {{name}}, welcome.", "Hi , welcome.", true},

		// A repeated fixed segment must not let the scan match the same one
		// twice and skip content in between.
		{"repeated fixed segment",
			"A {{x}} A {{y}} A", "A 1 A 2 A", true},
		{"repeated fixed segment, one missing",
			"A {{x}} A {{y}} A", "A 1 A", false},

		{"unrelated body entirely",
			"Hi {{name}}, your order has shipped.",
			"Totally unrelated text that matches no template.", false},
		{"empty body against a template",
			"Hi {{name}}.", "", false},
		{"unclosed brace is literal text",
			"Hi {{name, welcome.", "Hi {{name, welcome.", true},
		{"unclosed brace does not become a wildcard",
			"Hi {{name, welcome.", "Hi anyone, welcome.", false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := messaging.MatchesTemplate(testCase.template, testCase.body); got != testCase.want {
				t.Fatalf("MatchesTemplate(%q, %q) = %v, want %v",
					testCase.template, testCase.body, got, testCase.want)
			}
		})
	}
}

// The gate, as the submit path sees it.
func TestTheGateRefusesAnIndiaSendWithNoRegisteredTemplate(t *testing.T) {
	err := messaging.Check(messaging.GateInput{
		TenantStatus: "active", SenderStatus: "approved", SenderID: "s1",
		RecipientValid: true, BalanceMinor: 10_000, CostMinor: 12,
		RegisteredTemplateRequired: true,
		Body:                       "Your Acme order 4821 has shipped.",
	})
	if err == nil {
		t.Fatal("a send with no template was accepted where the regime requires one — " +
			"an Indian operator drops 100% of that traffic after we have charged for it")
	}
	if code := messaging.GateFailureCode(err); code != "registered_template_required" {
		t.Fatalf("failure code = %q, want registered_template_required", code)
	}
	if !messaging.IsRefusal(err) {
		t.Fatal("the refusal is not classified as one, so it would be reported as a 500")
	}
}

func TestTheGateRefusesABodyThatIsNotTheTemplate(t *testing.T) {
	err := messaging.Check(messaging.GateInput{
		TenantStatus: "active", SenderStatus: "approved", SenderID: "s1",
		TemplateStatus: "approved", TemplateSender: "s1",
		RecipientValid: true, BalanceMinor: 10_000, CostMinor: 12,
		RegisteredTemplateRequired: true,
		TemplateBody:               "Hi {{name}}, your order {{id}} has shipped.",
		Body:                       "Totally unrelated text that matches no template.",
	})
	if code := messaging.GateFailureCode(err); code != "template_body_mismatch" {
		t.Fatalf("failure code = %q, want template_body_mismatch", code)
	}
}

func TestTheGateAcceptsALegalInstantiation(t *testing.T) {
	err := messaging.Check(messaging.GateInput{
		TenantStatus: "active", SenderStatus: "approved", SenderID: "s1",
		TemplateStatus: "approved", TemplateSender: "s1",
		RecipientValid: true, BalanceMinor: 10_000, CostMinor: 12,
		RegisteredTemplateRequired: true,
		TemplateBody:               "Hi {{name}}, your order {{id}} has shipped.",
		Body:                       "Hi Priya, your order 4821 has shipped.",
	})
	if err != nil {
		t.Fatalf("a legal instantiation was refused: %v", err)
	}
}

// A country whose regime does not demand it must be unaffected — the rule is a
// property of the regime, not a new global requirement.
func TestACountryWithoutTheRuleStillSendsWithoutATemplate(t *testing.T) {
	err := messaging.Check(messaging.GateInput{
		TenantStatus: "active", SenderStatus: "approved", SenderID: "s1",
		RecipientValid: true, BalanceMinor: 10_000, CostMinor: 12,
		RegisteredTemplateRequired: false,
		Body:                       "Anything at all.",
	})
	if err != nil {
		t.Fatalf("a send outside the rule was refused: %v", err)
	}
}

// Refusal must cost nothing: the binding check has to sit before the balance
// check, or a refused send still reports insufficient_balance and the customer
// fixes the wrong thing.
func TestTemplateBindingIsRefusedBeforeAnythingAboutMoney(t *testing.T) {
	err := messaging.Check(messaging.GateInput{
		TenantStatus: "active", SenderStatus: "approved", SenderID: "s1",
		RecipientValid: true, BalanceMinor: 0, CostMinor: 12,
		RegisteredTemplateRequired: true,
		Body:                       "No template here.",
	})
	if code := messaging.GateFailureCode(err); code != "registered_template_required" {
		t.Fatalf("failure code = %q — an empty wallet reported before the real "+
			"reason sends the customer to top up a wallet that was not the problem", code)
	}
}

// An RCS campaign sends no body: the carrier holds the approved template and
// renders it from the variables we pass, so what reaches the handset IS the
// registered template. Comparing an empty body against the template text
// refused every RCS campaign the first time this gate was written.
func TestAnEmptyBodyIsNotComparedAgainstTheTemplate(t *testing.T) {
	err := messaging.Check(messaging.GateInput{
		TenantStatus: "active", SenderStatus: "approved", SenderID: "s1",
		TemplateStatus: "approved", TemplateSender: "s1",
		RecipientValid: true, BalanceMinor: 10_000, CostMinor: 12,
		RegisteredTemplateRequired: true,
		TemplateBody:               "Hi {{first_name}}, your order {{order_id}} shipped.",
		Body:                       "",
	})
	if err != nil {
		t.Fatalf("an RCS-style send with no body of its own was refused: %v", err)
	}
}

// But the template is still REQUIRED. An empty body does not become a way past
// the rule entirely.
func TestAnEmptyBodyStillNeedsATemplateWhereTheRegimeDemandsOne(t *testing.T) {
	err := messaging.Check(messaging.GateInput{
		TenantStatus: "active", SenderStatus: "approved", SenderID: "s1",
		RecipientValid: true, BalanceMinor: 10_000, CostMinor: 12,
		RegisteredTemplateRequired: true,
		Body:                       "",
	})
	if code := messaging.GateFailureCode(err); code != "registered_template_required" {
		t.Fatalf("failure code = %q, want registered_template_required", code)
	}
}
