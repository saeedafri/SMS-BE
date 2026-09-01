package secrets

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func testKey(t *testing.T) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
}

func TestARoundTripRecoversThePlaintext(t *testing.T) {
	box, err := NewBox(testKey(t))
	if err != nil {
		t.Fatalf("new box: %v", err)
	}
	const password = "s3cret-bind-password"
	sealed, err := box.Encrypt(password)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// The whole reason this is encryption and not hashing.
	opened, err := box.Decrypt(sealed)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if opened != password {
		t.Fatalf("round trip = %q, want %q", opened, password)
	}
	if strings.Contains(sealed, password) {
		t.Fatal("the plaintext is visible inside the ciphertext")
	}
}

// The same password encrypted twice must not produce the same row, or the
// database reveals which connections share a password.
func TestTheSameValueEncryptsDifferentlyEachTime(t *testing.T) {
	box, _ := NewBox(testKey(t))
	first, _ := box.Encrypt("same")
	second, _ := box.Encrypt("same")
	if first == second {
		t.Fatal("deterministic ciphertext — the nonce is not random")
	}
}

// GCM authenticates. A ciphertext edited in the database must fail to open
// rather than decrypt to something else.
func TestATamperedCiphertextIsRefused(t *testing.T) {
	box, _ := NewBox(testKey(t))
	sealed, _ := box.Encrypt("bind-password")
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw[len(raw)-1] ^= 0xff
	if _, err := box.Decrypt(base64.StdEncoding.EncodeToString(raw)); !errors.Is(err, ErrCiphertext) {
		t.Fatalf("tampered ciphertext opened, or wrong error: %v", err)
	}
}

// No key is a refusal to store, never permission to store plaintext.
func TestWithoutAKeyEncryptionRefuses(t *testing.T) {
	box, err := NewBox("")
	if err != nil {
		t.Fatalf("empty key should not be an error: %v", err)
	}
	if box != nil {
		t.Fatal("empty key should produce a nil box")
	}
	if _, err := box.Encrypt("anything"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("encrypt without a key = %v, want ErrNoKey", err)
	}
	if _, err := box.Decrypt("anything"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("decrypt without a key = %v, want ErrNoKey", err)
	}
}

func TestAKeyOfTheWrongSizeIsRejected(t *testing.T) {
	if _, err := NewBox(base64.StdEncoding.EncodeToString([]byte("too-short"))); err == nil {
		t.Fatal("a 9-byte key was accepted")
	}
	if _, err := NewBox("not base64 at all!!"); err == nil {
		t.Fatal("a non-base64 key was accepted")
	}
}
