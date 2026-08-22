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

// Second factor for platform staff.
//
// A deliberate mirror of the tenant functions in account.go and auth.go rather
// than a shared implementation. The two identity systems share no users table,
// no sessions table and no token namespace, and the moment one challenge row
// could belong to either kind of account, a single missing branch turns a
// customer's second factor into an operator session — the most expensive bug
// this codebase could have.

// OperatorMfaState is what challenge verification needs.
type OperatorMfaState struct {
	OperatorID uuid.UUID
	Enabled    bool
	Secret     string
}

func LoadOperatorForMfa(ctx context.Context, pool *pgxpool.Pool, operatorID uuid.UUID) (
	OperatorMfaState, error) {

	var state OperatorMfaState
	var secret *string
	err := pool.QueryRow(ctx,
		`SELECT id, mfa_enabled, mfa_secret FROM operator_users WHERE id = $1`, operatorID,
	).Scan(&state.OperatorID, &state.Enabled, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperatorMfaState{}, ErrNotFound
	}
	if err != nil {
		return OperatorMfaState{}, fmt.Errorf("store: load operator for mfa: %w", err)
	}
	if secret != nil {
		state.Secret = *secret
	}
	return state, nil
}

// StageOperatorMfaSecret stores the secret without enabling MFA. Enrolment is
// not complete until a code proves the operator actually scanned it — enabling
// here would lock out anyone who opened the screen and walked away, and an
// operator locked out of the console is an incident nobody can fix from the
// console.
func StageOperatorMfaSecret(ctx context.Context, pool *pgxpool.Pool,
	operatorID uuid.UUID, secret string) error {

	if _, err := pool.Exec(ctx,
		`UPDATE operator_users SET mfa_secret = $1, mfa_enabled = false WHERE id = $2`,
		secret, operatorID); err != nil {
		return fmt.Errorf("store: stage operator mfa secret: %w", err)
	}
	return nil
}

// EnableOperatorMfa flips the flag and replaces any previous recovery codes.
func EnableOperatorMfa(ctx context.Context, pool *pgxpool.Pool,
	operatorID uuid.UUID, codeHashes [][]byte) error {

	return inTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE operator_users SET mfa_enabled = true WHERE id = $1`, operatorID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM operator_recovery_codes WHERE operator_id = $1`, operatorID); err != nil {
			return err
		}
		for _, hash := range codeHashes {
			if _, err := tx.Exec(ctx,
				`INSERT INTO operator_recovery_codes (code_hash, operator_id) VALUES ($1, $2)`,
				hash, operatorID); err != nil {
				return err
			}
		}
		return nil
	}, "enable operator mfa")
}

// DisableOperatorMfa clears the flag, the secret and every unused recovery
// code, so re-enrolling later starts from a clean slate rather than an old
// secret somebody else may still hold.
func DisableOperatorMfa(ctx context.Context, pool *pgxpool.Pool, operatorID uuid.UUID) error {
	return inTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE operator_users SET mfa_enabled = false, mfa_secret = NULL WHERE id = $1`,
			operatorID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`DELETE FROM operator_recovery_codes WHERE operator_id = $1`, operatorID)
		return err
	}, "disable operator mfa")
}

// ConsumeOperatorRecoveryCode burns one code. Single-use is enforced by the
// UPDATE predicate, so the same code cannot be spent twice concurrently.
func ConsumeOperatorRecoveryCode(ctx context.Context, pool *pgxpool.Pool,
	operatorID uuid.UUID, codeHash []byte) error {

	tag, err := pool.Exec(ctx,
		`UPDATE operator_recovery_codes SET used_at = now()
		 WHERE operator_id = $1 AND code_hash = $2 AND used_at IS NULL`, operatorID, codeHash)
	if err != nil {
		return fmt.Errorf("store: consume operator recovery code: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateOperatorMfaChallenge records a pending second-factor step. No session
// exists until the code is verified; possession of this token grants nothing
// except the right to present one.
func CreateOperatorMfaChallenge(ctx context.Context, pool *pgxpool.Pool,
	tokenHash []byte, operatorID uuid.UUID, expiresAt time.Time) error {

	_, err := pool.Exec(ctx,
		`INSERT INTO operator_mfa_challenges (token_hash, operator_id, expires_at)
		 VALUES ($1, $2, $3)`, tokenHash, operatorID, expiresAt)
	if err != nil {
		return fmt.Errorf("store: create operator mfa challenge: %w", err)
	}
	return nil
}

// ConsumeOperatorMfaChallenge marks a challenge used and returns its operator.
// Single-use by construction: the UPDATE only matches a row that is unused and
// unexpired, so a replayed token matches nothing.
func ConsumeOperatorMfaChallenge(ctx context.Context, pool *pgxpool.Pool, tokenHash []byte) (
	uuid.UUID, error) {

	var operatorID uuid.UUID
	err := pool.QueryRow(ctx,
		`UPDATE operator_mfa_challenges SET used_at = now()
		 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		 RETURNING operator_id`, tokenHash).Scan(&operatorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("store: consume operator mfa challenge: %w", err)
	}
	return operatorID, nil
}

// inTx runs fn in a transaction and names the operation in any error. The
// tenant equivalents each hand-roll this; there are four of them here and the
// rollback is the part that is easy to forget.
func inTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error, what string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	return nil
}
