package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// User activity: the security-relevant things a person does to their own
// account, which the operator console reads when answering "who changed this,
// and when".
//
// These are recorded where they happen rather than derived afterwards. The
// previous implementation listed recent SESSIONS and labelled each one a login,
// which could not represent an API key being created or MFA being switched off,
// and lost a login entirely once its session was revoked.

// Event types, matching the contract's UserActivityEventType and the CHECK
// constraint on the table. Named constants because a typo in a string literal
// here would be stored happily by Postgres only if it happened to be one of the
// ten allowed values — and silently rejected as a constraint violation at
// runtime if not.
const (
	ActivityLogin           = "login"
	ActivityMFAEnroll       = "mfa.enroll"
	ActivityMFADisable      = "mfa.disable"
	ActivityAPIKeyCreate    = "api_key.create"
	ActivityAPIKeyRevoke    = "api_key.revoke"
	ActivityAPIKeyRotate    = "api_key.rotate"
	ActivitySessionRevoke   = "session.revoke"
	ActivityTeamInvite      = "team.invite"
	ActivityTeamRoleChange  = "team.role_change"
	ActivitySSOConfigChange = "sso.config_change"
	// The three halts. A campaign is a billable object and stopping one is the
	// action someone asks about afterwards.
	ActivityCampaignPause  = "campaign.pause"
	ActivityCampaignResume = "campaign.resume"
	ActivityCampaignCancel = "campaign.cancel"
)

// UserActivityEntry is one recorded event, already joined to the names the
// console displays.
type UserActivityEntry struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	TenantName string
	UserName   string
	UserEmail  string
	EventType  string
	Detail     string
	OccurredAt time.Time
}

// UserActivityFilter narrows the console's list. Every field is optional; the
// zero value asks for everything in the default window.
type UserActivityFilter struct {
	TenantID  *uuid.UUID
	EventType *string
	// Since bounds how far back to look. Zero means no lower bound.
	Since  time.Time
	Cursor string
	Limit  int
}

// RecordUserActivity appends one event.
//
// It takes the user's name and email rather than looking them up, because the
// row must stay readable after the user is removed from the tenant — at which
// point the join that would have supplied them returns nothing, and the entry
// explaining what that person did becomes anonymous.
//
// A failure here is returned but is never a reason to fail the action that
// caused it: callers log it and carry on. Refusing someone's login because an
// audit row could not be written would turn a bookkeeping problem into an
// outage, and the event has already happened either way.
func RecordUserActivity(ctx context.Context, pool *pgxpool.Pool, id Identity,
	userID uuid.UUID, userName, userEmail, eventType, detail string) error {

	// Through WithTenant, not a bare Exec. The table's RLS policy carries a
	// WITH CHECK clause, so an INSERT is refused outright unless
	// current_tenant_id() is set for the transaction — and a bare pool.Exec
	// never sets it.
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO user_activity (tenant_id, user_id, user_name, user_email,
			                           event_type, detail)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			id.TenantID, userID, userName, userEmail, eventType, detail)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: record user activity: %w", err)
	}
	return nil
}

// ListUserActivity reads the console's view across every tenant.
//
// It runs on the operator pool, which is not subject to the tenant RLS policy —
// working across all tenants is the entire point of the operator console. The
// tenant filter below is therefore a genuine filter, not a security boundary;
// the boundary is that only an authenticated operator reaches this code.
func ListUserActivity(ctx context.Context, pool *pgxpool.Pool,
	filter UserActivityFilter) ([]UserActivityEntry, int, string, error) {

	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cursorTime, cursorID, err := decodeLedgerCursor(filter.Cursor)
	if err != nil {
		return nil, 0, "", err
	}
	var tenantID *uuid.UUID
	if filter.TenantID != nil {
		tenantID = filter.TenantID
	}
	var since *time.Time
	if !filter.Since.IsZero() {
		since = &filter.Since
	}

	// Counted with the filters and without the cursor: it is the denominator of
	// the console's "Showing 1 to 20 of 54", so it must not move as the operator
	// pages and must track what they filtered to.
	var total int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM user_activity a
		WHERE ($1::uuid IS NULL OR a.tenant_id = $1)
		  AND ($2::text IS NULL OR a.event_type = $2)
		  AND ($3::timestamptz IS NULL OR a.occurred_at >= $3)`,
		tenantID, filter.EventType, since).Scan(&total); err != nil {
		return nil, 0, "", fmt.Errorf("store: count user activity: %w", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT a.id, a.tenant_id, t.name, a.user_name, a.user_email,
		       a.event_type, a.detail, a.occurred_at
		FROM user_activity a
		JOIN tenants t ON t.id = a.tenant_id
		WHERE ($1::uuid IS NULL OR a.tenant_id = $1)
		  AND ($2::text IS NULL OR a.event_type = $2)
		  AND ($3::timestamptz IS NULL OR a.occurred_at >= $3)
		  AND ($5::timestamptz IS NULL OR (a.occurred_at, a.id) < ($5, $6))
		ORDER BY a.occurred_at DESC, a.id DESC
		LIMIT $4`, tenantID, filter.EventType, since, limit+1, cursorTime, cursorID)
	if err != nil {
		return nil, 0, "", fmt.Errorf("store: list user activity: %w", err)
	}
	defer rows.Close()

	out := []UserActivityEntry{}
	for rows.Next() {
		var entry UserActivityEntry
		if err := rows.Scan(&entry.ID, &entry.TenantID, &entry.TenantName,
			&entry.UserName, &entry.UserEmail, &entry.EventType,
			&entry.Detail, &entry.OccurredAt); err != nil {
			return nil, 0, "", fmt.Errorf("store: scan user activity: %w", err)
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, "", err
	}

	next := ""
	if len(out) > limit {
		next = encodeLedgerCursor(out[limit-1].OccurredAt, out[limit-1].ID)
		out = out[:limit]
	}
	return out, total, next, nil
}

// RecordLogin appends a login event, resolving the user's name and email from
// their row rather than making every caller carry them.
//
// The sign-in paths do not have a resolved Identity yet — the session they are
// creating is what an Identity is built from — so this is the one recorder that
// takes ids and looks the rest up.
func RecordLogin(ctx context.Context, pool *pgxpool.Pool, tenantID, userID uuid.UUID) error {
	err := WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO user_activity (tenant_id, user_id, user_name, user_email,
			                           event_type, detail)
			SELECT $1, u.id, u.name, u.email::text, $3, 'Signed in'
			FROM users u WHERE u.id = $2`, tenantID, userID, ActivityLogin)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: record login: %w", err)
	}
	return nil
}
