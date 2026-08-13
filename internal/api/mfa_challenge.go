package api

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/auth"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// mfaChallengeLifetime is short on purpose: it exists only to bridge the gap
// between entering a password and entering a code.
const mfaChallengeLifetime = 5 * time.Minute

// issueMfaChallenge records a pending second-factor step and returns the
// contract's MfaChallenge. No session exists until the code is verified.
func (s *Server) issueMfaChallenge(ctx context.Context, userID uuid.UUID) (gen.MfaChallenge, error) {
	raw, hash, err := auth.NewToken()
	if err != nil {
		return gen.MfaChallenge{}, err
	}
	expiresAt := time.Now().Add(mfaChallengeLifetime)
	if err := store.CreateMfaChallenge(ctx, s.DB, hash, userID, expiresAt); err != nil {
		return gen.MfaChallenge{}, err
	}
	return gen.MfaChallenge{
		ChallengeToken: raw,
		Methods:        []gen.MfaChallengeMethods{"totp", "recovery_code"},
		ExpiresAt:      expiresAt,
	}, nil
}
