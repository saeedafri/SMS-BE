package messaging

import "testing"

// Ask 35 §2.9. An SMPP receipt's error is "stat:err". A handset that was off is
// unreachable, not a refusal.
func TestAReceiptStatDecidesTheErrorClass(t *testing.T) {
	cases := map[string]ErrorClass{
		"EXPIRD:027":  ErrorExpired,
		"UNDELIV:001": ErrorUnreachable,
		"REJECTD:000": ErrorRejected,
		"DELETED:000": ErrorRejected,
		"UNKNOWN:000": ErrorRejected,
	}
	for code, want := range cases {
		class, message := ClassifyCarrierError(code)
		if class != want {
			t.Errorf("%s classified %q, want %q", code, class, want)
		}
		if want == ErrorRejected && !containsCode(message, code) {
			t.Errorf("%s: message %q does not keep the raw code", code, message)
		}
	}
}

func containsCode(message, code string) bool {
	for i := 0; i+len(code) <= len(message); i++ {
		if message[i:i+len(code)] == code {
			return true
		}
	}
	return false
}
