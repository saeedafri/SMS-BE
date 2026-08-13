package verify_test

import (
	"strings"
	"testing"

	"github.com/saeedafri/sms-be/internal/domain/verify"
)

func TestGeneratedCodesHaveTheRequestedLengthAndAreNumeric(t *testing.T) {
	for _, length := range []int{4, 6, 8} {
		code, err := verify.GenerateCode(length)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if len(code) != length {
			t.Fatalf("code %q has length %d, want %d", code, len(code), length)
		}
		if strings.Trim(code, "0123456789") != "" {
			t.Fatalf("code %q contains a non-digit", code)
		}
	}
}

// Leading zeros must survive. Trimming them — or generating a number and
// formatting it — shrinks the keyspace and biases the result, which is a real
// weakening of a short secret rather than a cosmetic detail.
func TestCodesKeepLeadingZeros(t *testing.T) {
	sawLeadingZero := false
	for i := 0; i < 2000 && !sawLeadingZero; i++ {
		code, err := verify.GenerateCode(6)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if len(code) != 6 {
			t.Fatalf("code %q lost digits", code)
		}
		if code[0] == '0' {
			sawLeadingZero = true
		}
	}
	if !sawLeadingZero {
		t.Fatal("no code with a leading zero in 2000 draws — the keyspace is being trimmed")
	}
}

func TestCodesAreNotRepeated(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 500; i++ {
		code, err := verify.GenerateCode(8)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if seen[code] {
			t.Fatalf("code %q repeated within 500 draws — the source is not random enough", code)
		}
		seen[code] = true
	}
}

func TestOnlyTheExactCodeMatches(t *testing.T) {
	stored := verify.HashCode("123456")

	if !verify.CodeMatches(stored, "123456") {
		t.Fatal("the correct code was rejected")
	}
	// A prefix must not pass. If it did, an attacker could find the code one
	// digit at a time.
	for _, wrong := range []string{"12345", "1234567", "123457", "", "023456"} {
		if verify.CodeMatches(stored, wrong) {
			t.Fatalf("code %q was accepted against 123456", wrong)
		}
	}
}

func TestTheCodeIsNotRecoverableFromWhatWeStore(t *testing.T) {
	code := "482915"
	stored := verify.HashCode(code)
	if strings.Contains(string(stored), code) {
		t.Fatal("the stored value contains the code in the clear")
	}
	if len(stored) != 32 {
		t.Fatalf("stored value is %d bytes, want a 32-byte sha256 digest", len(stored))
	}
}

func TestOtpCopyMustCarryExactlyOneCodeVariable(t *testing.T) {
	cases := map[string]bool{
		"Your code is {{code}}":            true,
		"{{code}} is your code":            true,
		"Your code is here":                false, // no code reaches the recipient
		"{{code}} again {{code}}":          false, // carriers reject this as a template mismatch
		"Your code is {{ code }}":          false,
		"Your code is {{code}} - {{name}}": true,
	}
	for body, want := range cases {
		if got := verify.BodyHasCodeVariable(body); got != want {
			t.Errorf("BodyHasCodeVariable(%q) = %v, want %v", body, got, want)
		}
	}
}
