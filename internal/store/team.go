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

// ErrLastOwner is returned when an operation would leave a tenant with no
// owner. That state is unrecoverable through the API — nobody left could
// promote anyone — so it is refused rather than repaired later.
var ErrLastOwner = errors.New("store: tenant would be left without an owner")

// TeamMember is one row of GET /v1/team. Name and InvitedAt are pointers
// because the contract makes both nullable, with opposite meanings: an invited
// member has no name yet, and an active one has no invite timestamp.
type TeamMember struct {
	ID        uuid.UUID
	Name      *string
	Email     string
	Role      string
	Status    string
	InvitedAt *time.Time
}

func ListTeamMembers(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]TeamMember, error) {
	var out []TeamMember
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT u.id, u.name, u.email::text, tu.role, tu.status, tu.invited_at
			FROM tenant_users tu
			JOIN users u ON u.id = tu.user_id
			WHERE tu.tenant_id = $1
			ORDER BY tu.created_at`, id.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m TeamMember
			var name string
			if err := rows.Scan(&m.ID, &name, &m.Email, &m.Role, &m.Status, &m.InvitedAt); err != nil {
				return err
			}
			// An invited member has not chosen a name yet; the contract says
			// that is null rather than an empty string.
			if m.Status != "invited" {
				m.Name = &name
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: list team: %w", err)
	}
	return out, nil
}

// InviteTeamMember adds a pending member. It reuses an existing global user
// row when the address is already known to the platform, so one person can
// belong to several tenants.
func InviteTeamMember(ctx context.Context, pool *pgxpool.Pool, id Identity,
	email, role string) (TeamMember, error) {

	var member TeamMember
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var userID uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&userID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// An invited user has no password until they accept. A hash that
			// no input can produce is the safest placeholder: it cannot be
			// guessed, and login against it simply fails.
			if err := tx.QueryRow(ctx,
				`INSERT INTO users (email, name, password_hash) VALUES ($1, '', '!invited')
				 RETURNING id`, email).Scan(&userID); err != nil {
				return err
			}
		case err != nil:
			return err
		}

		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT exists(SELECT 1 FROM tenant_users WHERE tenant_id = $1 AND user_id = $2)`,
			id.TenantID, userID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrConflict
		}

		invitedAt := time.Now()
		if _, err := tx.Exec(ctx,
			`INSERT INTO tenant_users (tenant_id, user_id, role, status, invited_at)
			 VALUES ($1, $2, $3, 'invited', $4)`,
			id.TenantID, userID, role, invitedAt); err != nil {
			return err
		}
		member = TeamMember{
			ID: userID, Email: email, Role: role, Status: "invited", InvitedAt: &invitedAt,
		}
		return nil
	})
	if errors.Is(err, ErrConflict) {
		return TeamMember{}, ErrConflict
	}
	if err != nil {
		return TeamMember{}, fmt.Errorf("store: invite member: %w", err)
	}
	return member, nil
}

// UpdateTeamMemberRole changes a member's role, refusing to demote the last
// owner.
func UpdateTeamMemberRole(ctx context.Context, pool *pgxpool.Pool, id Identity,
	memberID uuid.UUID, role string) (TeamMember, error) {

	var member TeamMember
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := guardLastOwner(ctx, tx, id.TenantID, memberID, role); err != nil {
			return err
		}
		var name, status string
		var invitedAt *time.Time
		err := tx.QueryRow(ctx, `
			UPDATE tenant_users SET role = $1
			WHERE tenant_id = $2 AND user_id = $3
			RETURNING status, invited_at`, role, id.TenantID, memberID,
		).Scan(&status, &invitedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var email string
		if err := tx.QueryRow(ctx,
			`SELECT name, email::text FROM users WHERE id = $1`, memberID,
		).Scan(&name, &email); err != nil {
			return err
		}
		member = TeamMember{ID: memberID, Email: email, Role: role,
			Status: status, InvitedAt: invitedAt}
		if status != "invited" {
			member.Name = &name
		}
		return nil
	})
	switch {
	case errors.Is(err, ErrLastOwner):
		return TeamMember{}, ErrLastOwner
	case errors.Is(err, ErrNotFound):
		return TeamMember{}, ErrNotFound
	case err != nil:
		return TeamMember{}, fmt.Errorf("store: update member role: %w", err)
	}
	return member, nil
}

// RemoveTeamMember removes a member and revokes their sessions, so losing
// access takes effect immediately rather than whenever their token expires.
func RemoveTeamMember(ctx context.Context, pool *pgxpool.Pool, id Identity, memberID uuid.UUID) error {
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		// Removing a member is a demotion to "no role at all", so the same
		// last-owner guard applies.
		if err := guardLastOwner(ctx, tx, id.TenantID, memberID, "member"); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM tenant_users WHERE tenant_id = $1 AND user_id = $2`,
			id.TenantID, memberID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx,
			`UPDATE sessions SET revoked_at = now()
			 WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL`,
			id.TenantID, memberID)
		return err
	})
	switch {
	case errors.Is(err, ErrLastOwner):
		return ErrLastOwner
	case errors.Is(err, ErrNotFound):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("store: remove member: %w", err)
	}
	return nil
}

// guardLastOwner refuses a change that would remove the tenant's only owner.
func guardLastOwner(ctx context.Context, tx pgx.Tx, tenantID, memberID uuid.UUID, newRole string) error {
	if newRole == "owner" {
		return nil
	}
	var currentRole string
	err := tx.QueryRow(ctx,
		`SELECT role FROM tenant_users WHERE tenant_id = $1 AND user_id = $2`,
		tenantID, memberID).Scan(&currentRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if currentRole != "owner" {
		return nil
	}
	var owners int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM tenant_users WHERE tenant_id = $1 AND role = 'owner'`,
		tenantID).Scan(&owners); err != nil {
		return err
	}
	if owners <= 1 {
		return ErrLastOwner
	}
	return nil
}
