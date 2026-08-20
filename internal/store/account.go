package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CreateEmailVerification records a single-use verification token.
func CreateEmailVerification(ctx context.Context, pool *pgxpool.Pool,
	tokenHash []byte, userID uuid.UUID, expiresAt time.Time) error {

	if _, err := pool.Exec(ctx,
		// Same reason as CreatePasswordReset: the dev build issues a fixed token.
		`INSERT INTO email_verifications (token_hash, user_id, expires_at) VALUES ($1, $2, $3)
		 ON CONFLICT (token_hash) DO UPDATE
		   SET user_id = EXCLUDED.user_id, expires_at = EXCLUDED.expires_at, used_at = NULL`,
		tokenHash, userID, expiresAt); err != nil {
		return fmt.Errorf("store: create email verification: %w", err)
	}
	return nil
}

// ConsumeEmailVerification marks the token used and flips the user's verified
// flag. Single-use is enforced by the UPDATE's own predicate rather than a
// read-then-write, so two concurrent submissions cannot both succeed.
func ConsumeEmailVerification(ctx context.Context, pool *pgxpool.Pool, tokenHash []byte) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID uuid.UUID
	err = tx.QueryRow(ctx,
		`UPDATE email_verifications SET used_at = now()
		 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		 RETURNING user_id`, tokenHash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: consume email verification: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE users SET email_verified = true, updated_at = now() WHERE id = $1`,
		userID); err != nil {
		return fmt.Errorf("store: mark verified: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// FindUserIDByEmail is used by password/forgot, which must behave identically
// whether or not the address exists.
func FindUserIDByEmail(ctx context.Context, pool *pgxpool.Pool, email string) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("store: find user by email: %w", err)
	}
	return id, nil
}

func CreatePasswordReset(ctx context.Context, pool *pgxpool.Pool,
	tokenHash []byte, userID uuid.UUID, expiresAt time.Time) error {

	// Upsert, because token_hash is the primary key and the dev build issues a
	// FIXED token — so the second reset request ever made collided and the
	// endpoint answered 500 instead of 204. A random token never conflicts, so
	// this changes nothing for real traffic.
	//
	// used_at is cleared as well: re-requesting a reset must re-arm the link,
	// or a fixture account that consumed one could never request another.
	if _, err := pool.Exec(ctx,
		`INSERT INTO password_resets (token_hash, user_id, expires_at) VALUES ($1, $2, $3)
		 ON CONFLICT (token_hash) DO UPDATE
		   SET user_id = EXCLUDED.user_id, expires_at = EXCLUDED.expires_at, used_at = NULL`,
		tokenHash, userID, expiresAt); err != nil {
		return fmt.Errorf("store: create password reset: %w", err)
	}
	return nil
}

// ConsumePasswordReset sets the new password and revokes every session that
// user holds. Anyone who reset a password because they suspected compromise
// expects the attacker to be logged out — leaving sessions alive would defeat
// the point of the reset.
func ConsumePasswordReset(ctx context.Context, pool *pgxpool.Pool,
	tokenHash []byte, newPasswordHash string) error {

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID uuid.UUID
	err = tx.QueryRow(ctx,
		`UPDATE password_resets SET used_at = now()
		 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		 RETURNING user_id`, tokenHash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: consume password reset: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE users SET password_hash = $1, updated_at = now() WHERE id = $2`,
		newPasswordHash, userID); err != nil {
		return fmt.Errorf("store: set password: %w", err)
	}
	// Through the SECURITY DEFINER function, not a plain UPDATE: this
	// transaction has no tenant scope, so RLS would silently filter the write
	// to zero rows and leave every stolen session alive. See migration 00007.
	if _, err := tx.Exec(ctx, `SELECT revoke_user_sessions($1)`, userID); err != nil {
		return fmt.Errorf("store: revoke sessions after reset: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// SetPassword changes a password for an already-authenticated user.
func SetPassword(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, hash string) error {
	if _, err := pool.Exec(ctx,
		`UPDATE users SET password_hash = $1, updated_at = now() WHERE id = $2`,
		hash, userID); err != nil {
		return fmt.Errorf("store: set password: %w", err)
	}
	return nil
}

// StageMfaSecret stores the secret without enabling MFA. Enrolment is not
// complete until a code proves the user actually scanned it — enabling on
// enroll would lock out anyone who abandoned the flow halfway.
func StageMfaSecret(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, secret string) error {
	if _, err := pool.Exec(ctx,
		`UPDATE users SET mfa_secret = $1, mfa_enabled = false, updated_at = now()
		 WHERE id = $2`, secret, userID); err != nil {
		return fmt.Errorf("store: stage mfa secret: %w", err)
	}
	return nil
}

// EnableMfa flips the flag and replaces any previous recovery codes.
func EnableMfa(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, codeHashes [][]byte) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE users SET mfa_enabled = true, updated_at = now() WHERE id = $1`,
		userID); err != nil {
		return fmt.Errorf("store: enable mfa: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM mfa_recovery_codes WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("store: clear recovery codes: %w", err)
	}
	for _, hash := range codeHashes {
		if _, err := tx.Exec(ctx,
			`INSERT INTO mfa_recovery_codes (code_hash, user_id) VALUES ($1, $2)`,
			hash, userID); err != nil {
			return fmt.Errorf("store: insert recovery code: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// DisableMfa clears the flag, the secret and every unused recovery code, so
// re-enrolling later starts from a clean slate rather than an old secret.
func DisableMfa(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE users SET mfa_enabled = false, mfa_secret = NULL, updated_at = now()
		 WHERE id = $1`, userID); err != nil {
		return fmt.Errorf("store: disable mfa: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM mfa_recovery_codes WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("store: clear recovery codes: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// ConsumeRecoveryCode burns one code. Single-use is enforced by the UPDATE
// predicate, so the same code cannot be spent twice concurrently.
func ConsumeRecoveryCode(ctx context.Context, pool *pgxpool.Pool,
	userID uuid.UUID, codeHash []byte) error {

	tag, err := pool.Exec(ctx,
		`UPDATE mfa_recovery_codes SET used_at = now()
		 WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`, userID, codeHash)
	if err != nil {
		return fmt.Errorf("store: consume recovery code: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// LoadUserForMfa fetches what challenge verification needs.
func LoadUserForMfa(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) (Credentials, error) {
	var c Credentials
	var secret *string
	err := pool.QueryRow(ctx,
		`SELECT id, password_hash, mfa_enabled, mfa_secret FROM users WHERE id = $1`, userID,
	).Scan(&c.UserID, &c.PasswordHash, &c.MFAEnabled, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credentials{}, ErrNotFound
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("store: load user for mfa: %w", err)
	}
	if secret != nil {
		c.MFASecret = *secret
	}
	return c, nil
}
