package compliance_test

import (
	"reflect"
	"testing"

	"github.com/saeedafri/sms-be/internal/domain/compliance"
)

// These cases mirror ../SMS-UI/src/lib/templates/variables.ts. The UI shows a
// live preview parsed from the same string, so any divergence means the user
// is previewing something different from what we store.
func TestParseVariables(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"no tokens", "Your order has shipped.", []string{}},
		{"single", "Hi {{name}}", []string{"name"}},
		{"several in order", "Hi {{name}}, order {{order_id}} ships {{date}}",
			[]string{"name", "order_id", "date"}},
		{"repeats collapse, first-seen order wins",
			"{{b}} then {{a}} then {{b}} again", []string{"b", "a"}},
		{"inner whitespace is trimmed", "Hi {{  name  }}", []string{"name"}},
		{"digits and underscores are allowed", "{{first_name_2}}", []string{"first_name_2"}},
		{"empty body", "", []string{}},
		{"malformed tokens contribute nothing", "{{bad-name}} {{}}", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := compliance.ParseVariables(tc.body)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseVariables(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// Never nil: the column is NOT NULL and the contract's `variables` is a
// required array, so a nil slice would serialise as null and break the client.
func TestParseVariablesNeverReturnsNil(t *testing.T) {
	if got := compliance.ParseVariables(""); got == nil {
		t.Fatal("ParseVariables returned nil, want an empty slice")
	}
}

func TestValidateBody(t *testing.T) {
	valid := []string{
		"",
		"No variables here.",
		"Hi {{name}}",
		"{{a}}{{b}}",
		"Braces { alone } are fine",
	}
	for _, body := range valid {
		if result := compliance.ValidateBody(body); !result.OK {
			t.Errorf("ValidateBody(%q) rejected: %s", body, result.Reason)
		}
	}

	malformed := []string{
		"{{unclosed",
		"unopened}}",
		"{{bad-name}}",
		"{{}}",
		"{{ }}",
		"{{two words}}",
		"{{a}} and {{b",
	}
	for _, body := range malformed {
		result := compliance.ValidateBody(body)
		if result.OK {
			t.Errorf("ValidateBody(%q) accepted, want rejected", body)
		} else if result.Reason == "" {
			t.Errorf("ValidateBody(%q) rejected with no reason", body)
		}
	}
}
