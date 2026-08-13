// Package auth holds credential primitives: password hashing and the opaque
// session tokens the API issues. It deliberately knows nothing about HTTP or
// the database.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters. Memory is the expensive dimension for an attacker with
// GPUs, so it carries the weight: 64 MB per hash makes large-scale offline
// cracking costly while staying comfortable for a login request.
const (
	argonMemoryKiB  = 64 * 1024
	argonIterations = 3
	argonThreads    = 4
	argonKeyLength  = 32
	saltLength      = 16
	tokenBytes      = 32
)

var errMalformedHash = errors.New("auth: malformed password hash")

// HashPassword returns a PHC-format argon2id hash carrying its own parameters,
// so the cost can be raised later without invalidating existing hashes.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("auth: password must not be empty")
	}
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt,
		argonIterations, argonMemoryKiB, argonThreads, argonKeyLength)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonIterations, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether password matches encodedHash. Any malformed
// hash returns false rather than an error: a corrupt row must fail closed, and
// the caller has no safe way to act on the distinction anyway.
func VerifyPassword(encodedHash, password string) bool {
	salt, want, memory, iterations, threads, err := decodeHash(encodedHash)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt,
		iterations, memory, threads, uint32(len(want)))

	return subtle.ConstantTimeCompare(got, want) == 1
}

func decodeHash(encoded string) (salt, key []byte, memory, iterations uint32, threads uint8, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, errMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return nil, nil, 0, 0, 0, errMalformedHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return nil, nil, 0, 0, 0, errMalformedHash
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return nil, nil, 0, 0, 0, errMalformedHash
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return nil, nil, 0, 0, 0, errMalformedHash
	}
	if len(salt) == 0 || len(key) == 0 {
		return nil, nil, 0, 0, 0, errMalformedHash
	}
	return salt, key, memory, iterations, threads, nil
}

// NewToken mints an opaque session token, returning the value to hand the
// client and the SHA-256 to store. The raw token is never persisted, so a
// database dump yields nothing an attacker can present as a session.
//
// SHA-256 rather than argon2 is correct here: the token already carries 256
// bits of entropy from a CSPRNG, so there is no low-entropy secret to
// stretch — and this runs on every authenticated request.
func NewToken() (raw string, hash []byte, err error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("auth: read token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, HashToken(raw), nil
}

func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// DummyHash is a valid argon2id hash of a random value. Login verifies against
// it when the email is unknown, so an absent account costs the same work as a
// present one with a wrong password — without it, response timing alone tells
// an attacker which addresses are registered here.
//
// It is computed at startup rather than hard-coded because a hard-coded
// constant that failed to decode would make VerifyPassword return early
// without doing any argon2 work, silently removing the very timing defence
// this exists to provide.
var DummyHash = mustDummyHash()

func mustDummyHash() string {
	filler := make([]byte, 32)
	if _, err := rand.Read(filler); err != nil {
		panic("auth: cannot generate dummy hash: " + err.Error())
	}
	hash, err := HashPassword(base64.RawURLEncoding.EncodeToString(filler))
	if err != nil {
		panic("auth: cannot generate dummy hash: " + err.Error())
	}
	return hash
}
