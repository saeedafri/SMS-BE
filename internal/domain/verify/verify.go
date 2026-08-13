// Package verify owns OTP code generation and checking.
package verify

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"math/big"
	"strings"
)

var (
	// ErrExpired means the code was right but too late. Kept distinct from
	// ErrIncorrect so the UI can say "that code has expired, request another"
	// rather than making someone re-type a code that was never going to work.
	ErrExpired = errors.New("verify: code expired")
	// ErrIncorrect is a wrong guess with attempts still remaining.
	ErrIncorrect = errors.New("verify: code incorrect")
	// ErrLocked means the attempt budget is spent. The verification is dead
	// even if the next guess would have been right — that is the entire point
	// of an attempt limit.
	ErrLocked = errors.New("verify: too many attempts")
	// ErrRateLimited means this number has requested too many codes.
	ErrRateLimited = errors.New("verify: too many requests for this number")
)

// GenerateCode produces a numeric OTP of the requested length.
//
// crypto/rand, not math/rand: a predictable OTP is not an OTP. Each digit is
// drawn independently so every code of the given length is equally likely,
// including ones with leading zeros — trimming those would shrink the keyspace
// and bias the result.
func GenerateCode(length int) (string, error) {
	if length != 4 && length != 6 && length != 8 {
		length = 6
	}
	var builder strings.Builder
	for i := 0; i < length; i++ {
		digit, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		builder.WriteByte(byte('0' + digit.Int64()))
	}
	return builder.String(), nil
}

// HashCode is what gets stored. The code itself is never persisted: a database
// leak must not hand an attacker live codes for every pending login.
func HashCode(code string) []byte {
	sum := sha256.Sum256([]byte(code))
	return sum[:]
}

// CodeMatches compares in constant time.
//
// A byte-by-byte comparison that returns early leaks how many leading digits
// were right through timing, which turns a 6-digit code into six 1-digit
// guesses. That is a real attack on a short numeric secret, not a theoretical
// one, which is why this is not just bytes.Equal.
func CodeMatches(stored []byte, candidate string) bool {
	return subtle.ConstantTimeCompare(stored, HashCode(candidate)) == 1
}

// BodyHasCodeVariable reports whether OTP copy contains exactly one {{code}}.
// Zero means the recipient gets a message with no code in it; more than one
// means the code appears twice, which carriers reject as a template mismatch.
func BodyHasCodeVariable(body string) bool {
	return strings.Count(body, "{{code}}") == 1
}
