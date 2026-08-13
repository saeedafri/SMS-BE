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

// ErrConflict is returned when a uniqueness rule would be violated.
var ErrConflict = errors.New("store: conflict")

// Credentials is what login needs to verify a password and then scope itself.
type Credentials struct {
	UserID       uuid.UUID
	PasswordHash string
	MFAEnabled   bool
	MFASecret    string
}

// FindCredentialsByEmail looks a user up for login. `users` carries no RLS —
// it is global, not tenant-owned — so this is a plain query.
func FindCredentialsByEmail(ctx context.Context, pool *pgxpool.Pool, email string) (Credentials, error) {
	var c Credentials
	var secret *string
	err := pool.QueryRow(ctx,
		`SELECT id, password_hash, mfa_enabled, mfa_secret FROM users WHERE email = $1`, email,
	).Scan(&c.UserID, &c.PasswordHash, &c.MFAEnabled, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credentials{}, ErrNotFound
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("store: find credentials: %w", err)
	}
	if secret != nil {
		c.MFASecret = *secret
	}
	return c, nil
}

// Membership is a user's tenant and role, resolved after their password checks
// out. Uses a SECURITY DEFINER function because there is no tenant scope yet.
type Membership struct {
	TenantID uuid.UUID
	Role     string
	Status   string
}

func FindMembership(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) (Membership, error) {
	var m Membership
	err := pool.QueryRow(ctx,
		`SELECT tenant_id, role, status FROM resolve_user_tenant($1)`, userID,
	).Scan(&m.TenantID, &m.Role, &m.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Membership{}, ErrNotFound
	}
	if err != nil {
		return Membership{}, fmt.Errorf("store: find membership: %w", err)
	}
	return m, nil
}

// SessionRequest carries the client details GET /v1/sessions displays.
type SessionRequest struct {
	TenantID  uuid.UUID
	UserID    uuid.UUID
	TokenHash []byte
	Device    string
	Browser   string
	IP        string
	ExpiresAt time.Time
}

func CreateSession(ctx context.Context, pool *pgxpool.Pool, req SessionRequest) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx,
		`SELECT create_session($1, $2, $3, $4, $5, $6, $7)`,
		req.TenantID, req.UserID, req.TokenHash,
		req.Device, req.Browser, req.IP, req.ExpiresAt,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("store: create session: %w", err)
	}
	return id, nil
}

// SignupRequest is everything needed to create a tenant and its first owner.
type SignupRequest struct {
	FullName     string
	Email        string
	PasswordHash string
	OrgName      string
	Country      string
}

// CreateTenantWithOwner creates the tenant, the user, and the owner membership
// atomically, through a SECURITY DEFINER function.
//
// It has to be a function rather than a transaction of plain statements: a
// policy's USING clause governs SELECT/UPDATE/DELETE, not INSERT, so under
// FORCE ROW LEVEL SECURITY with no WITH CHECK the application role cannot
// insert a tenant at all. See migration 00006.
func CreateTenantWithOwner(ctx context.Context, pool *pgxpool.Pool, req SignupRequest) (tenantID, userID uuid.UUID, err error) {
	err = pool.QueryRow(ctx,
		`SELECT tenant_id, user_id FROM signup_tenant_owner($1, $2, $3, $4, $5)`,
		req.OrgName, req.Country, req.Email, req.FullName, req.PasswordHash,
	).Scan(&tenantID, &userID)
	if isUniqueViolation(err) {
		return uuid.Nil, uuid.Nil, ErrConflict
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("store: signup: %w", err)
	}
	return tenantID, userID, nil
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}

// SessionSummary is one row of GET /v1/sessions.
type SessionSummary struct {
	ID           uuid.UUID
	Device       string
	Browser      string
	Location     string
	IPAddress    string
	LastActiveAt time.Time
	Current      bool
}

func ListSessions(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]SessionSummary, error) {
	var out []SessionSummary
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, device, browser, location, ip_address, last_active_at
			FROM sessions
			WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
			ORDER BY last_active_at DESC`, id.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s SessionSummary
			if err := rows.Scan(&s.ID, &s.Device, &s.Browser, &s.Location,
				&s.IPAddress, &s.LastActiveAt); err != nil {
				return err
			}
			s.Current = s.ID == id.SessionID
			out = append(out, s)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	return out, nil
}

// RevokeSession kills one session. It runs scoped, so an id belonging to
// another tenant simply matches nothing and surfaces as ErrNotFound — a
// cross-tenant probe learns only that the id does not exist for them.
func RevokeSession(ctx context.Context, pool *pgxpool.Pool, id Identity, sessionID uuid.UUID) error {
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE sessions SET revoked_at = now()
			 WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`,
			sessionID, id.UserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: revoke session: %w", err)
	}
	return nil
}

// RevokeAllUserSessions is used after a password reset or when a member is
// removed from a tenant — anything that should log the person out everywhere.
func RevokeAllUserSessions(ctx context.Context, pool *pgxpool.Pool, tenantID, userID uuid.UUID) error {
	err := WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE sessions SET revoked_at = now()
			 WHERE user_id = $1 AND revoked_at IS NULL`, userID)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: revoke all sessions: %w", err)
	}
	return nil
}

// CreateMfaChallenge records a pending second-factor step. mfa_challenges is
// not tenant-scoped: at this point in login the tenant is not yet established,
// and possession of the challenge token is the only thing it grants.
func CreateMfaChallenge(ctx context.Context, pool *pgxpool.Pool,
	tokenHash []byte, userID uuid.UUID, expiresAt time.Time) error {

	_, err := pool.Exec(ctx,
		`INSERT INTO mfa_challenges (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		tokenHash, userID, expiresAt)
	if err != nil {
		return fmt.Errorf("store: create mfa challenge: %w", err)
	}
	return nil
}

// ConsumeMfaChallenge marks a challenge used and returns its user. It is
// single-use by construction: the UPDATE only matches a row that is unused and
// unexpired, so a replayed token matches nothing.
func ConsumeMfaChallenge(ctx context.Context, pool *pgxpool.Pool, tokenHash []byte) (uuid.UUID, error) {
	var userID uuid.UUID
	err := pool.QueryRow(ctx,
		`UPDATE mfa_challenges SET used_at = now()
		 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		 RETURNING user_id`, tokenHash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("store: consume mfa challenge: %w", err)
	}
	return userID, nil
}
