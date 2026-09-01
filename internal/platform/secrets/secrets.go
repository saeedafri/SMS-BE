// Package secrets encrypts values that must be recoverable, not merely verified.
//
// A password we check is hashed. An SMPP bind password is different: we have to
// present the plaintext to the operator's gateway on every bind, so a hash would
// make the field useless. That leaves encryption, and encryption means a key
// that lives outside the database — otherwise the ciphertext and the means to
// read it sit in the same dump.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// ErrNoKey is returned when no encryption key is configured.
//
// Callers must treat this as a refusal to store, never as permission to store
// plaintext. A deployment without a key can define a connection and cannot give
// it a password, which is exactly the state `enable` already refuses.
var ErrNoKey = errors.New("secrets: no encryption key configured")

// ErrCiphertext is returned when stored data cannot be decrypted — a truncated
// value, or a key that has been rotated without re-encrypting.
var ErrCiphertext = errors.New("secrets: value cannot be decrypted with this key")

// Box encrypts and decrypts with AES-256-GCM.
//
// GCM rather than CBC: it authenticates as well as encrypts, so a ciphertext
// edited in the database fails to open instead of decrypting to something
// attacker-chosen. The nonce is random per value and stored in front of the
// ciphertext, which is why encrypting the same password twice gives two
// different rows — that is the point, not a bug.
type Box struct {
	aead cipher.AEAD
}

// NewBox builds a Box from a base64-encoded 32-byte key.
//
// An empty key is not an error here: it produces a nil Box, and every method on
// a nil Box refuses with ErrNoKey. That keeps "this deployment has no key" a
// first-class state rather than something each caller nil-checks differently.
func NewBox(base64Key string) (*Box, error) {
	if base64Key == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return nil, fmt.Errorf("secrets: key is not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("secrets: key must decode to 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Encrypt returns base64(nonce || ciphertext).
func (b *Box) Encrypt(plaintext string) (string, error) {
	if b == nil {
		return "", ErrNoKey
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("secrets: nonce: %w", err)
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt.
func (b *Box) Decrypt(encoded string) (string, error) {
	if b == nil {
		return "", ErrNoKey
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", ErrCiphertext
	}
	if len(raw) < b.aead.NonceSize() {
		return "", ErrCiphertext
	}
	nonce, ciphertext := raw[:b.aead.NonceSize()], raw[b.aead.NonceSize():]
	opened, err := b.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", ErrCiphertext
	}
	return string(opened), nil
}
