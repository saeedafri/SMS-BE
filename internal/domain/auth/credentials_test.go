package auth_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/saeedafri/sms-be/internal/domain/auth"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := auth.HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !auth.VerifyPassword(hash, "correct-horse-battery-staple") {
		t.Fatal("VerifyPassword rejected the correct password")
	}
	if auth.VerifyPassword(hash, "wrong-password") {
		t.Fatal("VerifyPassword accepted the wrong password")
	}
}

// Two hashes of the same password must differ, so an attacker holding the
// database cannot tell which accounts share a password.
func TestPasswordHashesAreSalted(t *testing.T) {
	first, err := auth.HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := auth.HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if first == second {
		t.Fatal("two hashes of the same password are identical; the salt is not random")
	}
}

func TestPasswordHashDoesNotContainThePassword(t *testing.T) {
	const password = "plaintext-should-never-appear"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if strings.Contains(hash, password) {
		t.Fatal("the stored hash contains the plaintext password")
	}
}

// A malformed or truncated hash column must never authenticate anyone.
func TestVerifyRejectsMalformedHashes(t *testing.T) {
	for _, hash := range []string{
		"",
		"not-a-real-hash",
		"$argon2id$v=19$m=65536,t=3,p=4$onlyonepart",
		"$bcrypt$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=notanumber,t=3,p=4$c2FsdA$aGFzaA",
	} {
		if auth.VerifyPassword(hash, "anything") {
			t.Errorf("VerifyPassword accepted the malformed hash %q", hash)
		}
	}
}

func TestHashPasswordRejectsEmptyPassword(t *testing.T) {
	if _, err := auth.HashPassword(""); err == nil {
		t.Fatal("expected an error for an empty password, got nil")
	}
}

func TestNewTokenIsUnique(t *testing.T) {
	seen := make(map[string]bool, 100)
	for range 100 {
		raw, _, err := auth.NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		if seen[raw] {
			t.Fatal("NewToken returned a duplicate token")
		}
		seen[raw] = true
	}
}

func TestHashTokenReproducesTheStoredHash(t *testing.T) {
	raw, hash, err := auth.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if !bytes.Equal(hash, auth.HashToken(raw)) {
		t.Fatal("HashToken does not reproduce the hash NewToken returned")
	}
	if bytes.Contains(hash, []byte(raw)) {
		t.Fatal("the stored hash contains the raw token")
	}
	if len(hash) != 32 {
		t.Fatalf("hash length = %d, want 32 (SHA-256)", len(hash))
	}
}

// Tokens go into an Authorization header and a cookie, so they must survive
// both without escaping.
func TestNewTokenIsURLSafe(t *testing.T) {
	raw, _, err := auth.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if strings.ContainsAny(raw, "+/= ") {
		t.Fatalf("token %q contains characters that need escaping", raw)
	}
	if len(raw) < 40 {
		t.Fatalf("token length = %d, want at least 40 characters for 32 bytes of entropy", len(raw))
	}
}
