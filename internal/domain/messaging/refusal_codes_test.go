package messaging

import (
	"testing"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// Every refusal the gate can return must be a code the contract declares, or a
// customer sees "Refused" with no reason. Neither compiler can catch this: a Go
// named string accepts any string. So the test walks every refusal error and
// asks the generated enum.
func TestEveryGateRefusalIsADeclaredCode(t *testing.T) {
	for _, err := range refusals {
		code := GateFailureCode(err)
		if !gen.MessageRefusalCode(code).Valid() {
			t.Errorf("%v -> %q is not a declared MessageRefusalCode", err, code)
		}
		if code == "rejected" {
			t.Errorf("%v falls to the catch-all: give it a named code", err)
		}
	}
	// The codes the send path sets directly, outside the gate.
	for _, code := range []string{"sender_not_found", "template_not_found", "no_rate",
		"no_operator_bind", "dlt_ids_missing"} {
		if !gen.MessageRefusalCode(code).Valid() {
			t.Errorf("the send path sets %q, which is not a declared MessageRefusalCode", code)
		}
	}
}
