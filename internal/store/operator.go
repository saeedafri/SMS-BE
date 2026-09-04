package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OperatorIdentity is a signed-in member of platform staff.
//
// It is deliberately NOT a store.Identity. A tenant identity carries a
// TenantID that scopes every query it touches; an operator has no tenant and
// works across all of them. Sharing one type would mean every handler had to
// remember which kind it held, and the first place someone forgot would be a
// cross-tenant leak.
type OperatorIdentity struct {
	OperatorID uuid.UUID
	SessionID  uuid.UUID
	Name       string
	Email      string
	Role       string
}

// CreateOperatorSession issues a session for an operator whose password has
// already been verified.
func CreateOperatorSession(ctx context.Context, pool *pgxpool.Pool, operatorID uuid.UUID,
	tokenHash []byte, ttl time.Duration) (time.Time, error) {

	expiresAt := time.Now().UTC().Add(ttl)
	_, err := pool.Exec(ctx, `
		INSERT INTO operator_sessions (operator_id, token_hash, expires_at)
		VALUES ($1, $2, $3)`, operatorID, tokenHash, expiresAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: create operator session: %w", err)
	}
	return expiresAt, nil
}

// FindOperatorByEmail returns the operator and their stored password hash.
func FindOperatorByEmail(ctx context.Context, pool *pgxpool.Pool, email string) (
	OperatorIdentity, string, error) {

	var identity OperatorIdentity
	var hash string
	err := pool.QueryRow(ctx, `
		SELECT id, name, email, role, password_hash FROM operator_users WHERE email = $1`,
		email).Scan(&identity.OperatorID, &identity.Name, &identity.Email,
		&identity.Role, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperatorIdentity{}, "", ErrNotFound
	}
	if err != nil {
		return OperatorIdentity{}, "", fmt.Errorf("store: find operator: %w", err)
	}
	return identity, hash, nil
}

// ResolveOperatorSession turns a presented token into the operator it belongs
// to. An expired session resolves to nothing rather than to an error the caller
// might treat as "database trouble" and retry.
func ResolveOperatorSession(ctx context.Context, pool *pgxpool.Pool, tokenHash []byte) (
	OperatorIdentity, error) {

	var identity OperatorIdentity
	err := pool.QueryRow(ctx, `
		SELECT s.id, o.id, o.name, o.email, o.role
		FROM operator_sessions s JOIN operator_users o ON o.id = s.operator_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, tokenHash,
	).Scan(&identity.SessionID, &identity.OperatorID, &identity.Name,
		&identity.Email, &identity.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperatorIdentity{}, ErrNotFound
	}
	if err != nil {
		return OperatorIdentity{}, fmt.Errorf("store: resolve operator session: %w", err)
	}
	return identity, nil
}

// RecordOperatorAction appends to the audit log.
//
// The tenant NAME is stored alongside the id because this record outlives the
// tenant: "who suspended Acme Retail" must still read as a sentence after the
// tenant row is gone.
func RecordOperatorAction(ctx context.Context, pool *pgxpool.Pool, actor, action string,
	tenantID *uuid.UUID, tenantName, targetLabel, detail string) error {

	_, err := pool.Exec(ctx, `
		INSERT INTO operator_audit_log (actor, action, tenant_id, tenant_name,
		                                target_label, detail)
		VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),NULLIF($6,''))`,
		actor, action, tenantID, tenantName, targetLabel, detail)
	if err != nil {
		return fmt.Errorf("store: record operator action: %w", err)
	}
	return nil
}

// AuditEntry is one line of the operator audit log.
type AuditEntry struct {
	ID          uuid.UUID
	OccurredAt  time.Time
	Actor       string
	Action      string
	TenantID    *uuid.UUID
	TenantName  *string
	TargetLabel *string
	Detail      *string
}

// ListAuditLog returns operator actions, most recent first, narrowed by tenant
// and action when given.
//
// Filtering matters here more than on most lists: the audit log is what someone
// reads during an incident to answer "who changed this tenant, and when". A
// tenant filter that silently returned every tenant's actions would have them
// reading the wrong history at the worst possible moment.
// AuditLogFilter narrows the audit log and positions one page within it.
type AuditLogFilter struct {
	TenantID *uuid.UUID
	Action   *string
	Since    time.Time
	Cursor   string
	Limit    int
}

// ListAuditLog returns one page of the audit log and the number of rows the
// filter matches.
//
// Total is counted with the same WHERE clause but without the cursor, because
// it is the denominator of "Showing 1 to 20 of 54" — a total that ignored the
// filters would read as the filter being broken, and one that included the
// cursor would shrink as the operator paged.
func ListAuditLog(ctx context.Context, pool *pgxpool.Pool, filter AuditLogFilter) (
	[]AuditEntry, int, string, error) {

	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cursorTime, cursorID, err := decodeLedgerCursor(filter.Cursor)
	if err != nil {
		return nil, 0, "", err
	}

	var total int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM operator_audit_log
		WHERE ($1::uuid IS NULL OR tenant_id = $1)
		  AND ($2::text IS NULL OR action    = $2)
		  AND occurred_at >= $3`,
		filter.TenantID, filter.Action, filter.Since).Scan(&total); err != nil {
		return nil, 0, "", fmt.Errorf("store: count audit log: %w", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT id, occurred_at, actor, action, tenant_id, tenant_name, target_label, detail
		FROM operator_audit_log
		WHERE ($2::uuid IS NULL OR tenant_id = $2)
		  AND ($3::text IS NULL OR action    = $3)
		  AND occurred_at >= $4
		  AND ($5::timestamptz IS NULL OR (occurred_at, id) < ($5, $6))
		ORDER BY occurred_at DESC, id DESC LIMIT $1`,
		limit+1, filter.TenantID, filter.Action, filter.Since, cursorTime, cursorID)
	if err != nil {
		return nil, 0, "", fmt.Errorf("store: list audit log: %w", err)
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var entry AuditEntry
		if err := rows.Scan(&entry.ID, &entry.OccurredAt, &entry.Actor, &entry.Action,
			&entry.TenantID, &entry.TenantName, &entry.TargetLabel, &entry.Detail); err != nil {
			return nil, 0, "", fmt.Errorf("store: scan audit entry: %w", err)
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

// OperatorTenant is a tenant as platform staff see it: identity, standing, and
// enough usage to judge whether it is worth attention.
type OperatorTenant struct {
	ID          uuid.UUID
	Name        string
	Country     string
	Status      string
	CreatedAt   time.Time
	FlaggedAt   *time.Time
	FlagReason  *string
	ThrottledAt *time.Time
	// ThrottledRatePerSecond is non-null exactly when ThrottledAt is.
	ThrottledRatePerSecond *int
	MessagesSent30d        int
	LastActivityAt         *time.Time
}

// ListTenants returns the tenants the operator console lists, narrowed by
// status and country when either is given.
//
// Both filters are applied in SQL rather than in Go. On today's eight-row
// fixture that makes no measurable difference; on a real platform the whole
// point of filtering to "suspended in India" is to avoid reading every tenant
// on the system into memory first.
//
// A nil pointer means "no filter" and matches everything — which is what an
// absent query parameter should do. The empty string is deliberately NOT
// treated as absent: `?status=` is a client asking for tenants whose status is
// empty, and quietly turning that into "all tenants" is how a filtered screen
// ends up showing rows it said it had excluded.
func ListTenants(ctx context.Context, pool *pgxpool.Pool, status, country *string,
	cursor string, limit int) ([]OperatorTenant, int, string, error) {

	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cursorTime, cursorID, err := decodeLedgerCursor(cursor)
	if err != nil {
		return nil, 0, "", err
	}

	// The status filter matches the STANDING the console displays, not the raw
	// status column — and those are not the same thing.
	//
	// Throttling a tenant sets throttled_at and leaves status as 'active',
	// because throttled is a restriction on an active account rather than a
	// separate lifecycle state. The API already collapses the two columns into
	// one standing for display (see tenantStanding). Filtering on the raw column
	// meant ?status=throttled matched nothing at all: the console showed a
	// tenant as throttled and then could not find it under that filter.
	//
	// The CASE below is the same precedence tenantStanding uses — suspended
	// outranks throttled, because a suspended tenant is not sending at all and
	// reporting "throttled" would understate what was done to them.
	var total int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM tenants
		WHERE ($1::text IS NULL OR CASE
		           WHEN status = 'suspended'      THEN 'suspended'
		           WHEN throttled_at IS NOT NULL  THEN 'throttled'
		           ELSE status
		       END = $1)
		  AND ($2::text IS NULL OR country = $2)`, status, country).Scan(&total); err != nil {
		return nil, 0, "", fmt.Errorf("store: count tenants: %w", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT id, name, country, status, created_at, flagged_at, flag_reason, throttled_at,
		       throttled_rate_per_second
		FROM tenants
		WHERE ($1::text IS NULL OR CASE
		           WHEN status = 'suspended'      THEN 'suspended'
		           WHEN throttled_at IS NOT NULL  THEN 'throttled'
		           ELSE status
		       END = $1)
		  AND ($2::text IS NULL OR country = $2)
		  AND ($3::timestamptz IS NULL OR (created_at, id) < ($3, $4))
		ORDER BY created_at DESC, id DESC
		LIMIT $5`, status, country, cursorTime, cursorID, limit+1)
	if err != nil {
		return nil, 0, "", fmt.Errorf("store: list tenants: %w", err)
	}
	defer rows.Close()
	out := []OperatorTenant{}
	for rows.Next() {
		var tenant OperatorTenant
		if err := rows.Scan(&tenant.ID, &tenant.Name, &tenant.Country, &tenant.Status,
			&tenant.CreatedAt, &tenant.FlaggedAt, &tenant.FlagReason,
			&tenant.ThrottledAt, &tenant.ThrottledRatePerSecond); err != nil {
			return nil, 0, "", fmt.Errorf("store: scan tenant: %w", err)
		}
		out = append(out, tenant)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, "", err
	}

	next := ""
	if len(out) > limit {
		next = encodeLedgerCursor(out[limit-1].CreatedAt, out[limit-1].ID)
		out = out[:limit]
	}
	return out, total, next, nil
}

// AllTenantIDs returns every tenant id, unpaged.
//
// Separate from ListTenants deliberately. That one pages for the console, and a
// background sweep that borrowed it would stop at whatever page size the
// console happened to want — silently reconciling the first 100 tenants and no
// others, with nothing in the logs to say so.
func AllTenantIDs(ctx context.Context, pool *pgxpool.Pool) ([]uuid.UUID, error) {
	rows, err := pool.Query(ctx, `SELECT id FROM tenants ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("store: all tenant ids: %w", err)
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scan tenant id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func GetTenant(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (OperatorTenant, error) {
	var tenant OperatorTenant
	err := pool.QueryRow(ctx, `
		SELECT id, name, country, status, created_at, flagged_at, flag_reason, throttled_at,
		       throttled_rate_per_second
		FROM tenants WHERE id = $1`, id).Scan(&tenant.ID, &tenant.Name, &tenant.Country,
		&tenant.Status, &tenant.CreatedAt, &tenant.FlaggedAt, &tenant.FlagReason,
		&tenant.ThrottledAt, &tenant.ThrottledRatePerSecond)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperatorTenant{}, ErrNotFound
	}
	if err != nil {
		return OperatorTenant{}, fmt.Errorf("store: get tenant: %w", err)
	}
	return tenant, nil
}

// SetTenantStatus suspends or reinstates a tenant.
func SetTenantStatus(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, status string) error {
	tag, err := pool.Exec(ctx, `UPDATE tenants SET status = $2 WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("store: set tenant status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTenantThrottled applies or lifts a send-rate ceiling.
//
// The rate and the throttled state move together, always. A transition out that
// cleared throttled_at and left the rate behind would leave the console
// reporting a live ceiling on a tenant that no longer has one — so both columns
// are written by the same statement, and a CHECK constraint refuses the pair if
// they ever disagree.
func SetTenantThrottled(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID,
	ratePerSecond *int) error {

	var tag pgconn.CommandTag
	var err error
	if ratePerSecond != nil {
		tag, err = pool.Exec(ctx, `UPDATE tenants
			SET throttled_at = now(), throttled_rate_per_second = $2
			WHERE id = $1`, id, *ratePerSecond)
	} else {
		tag, err = pool.Exec(ctx, `UPDATE tenants
			SET throttled_at = NULL, throttled_rate_per_second = NULL
			WHERE id = $1`, id)
	}
	if err != nil {
		return fmt.Errorf("store: throttle tenant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTenantFlag raises or clears an abuse flag.
func SetTenantFlag(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, reason string) error {
	var tag pgconn.CommandTag
	var err error
	if reason == "" {
		tag, err = pool.Exec(ctx,
			`UPDATE tenants SET flagged_at = NULL, flag_reason = NULL WHERE id = $1`, id)
	} else {
		tag, err = pool.Exec(ctx,
			`UPDATE tenants SET flagged_at = now(), flag_reason = $2 WHERE id = $1`, id, reason)
	}
	if err != nil {
		return fmt.Errorf("store: flag tenant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Route is one carrier path for a country and channel.
type Route struct {
	ID                  uuid.UUID
	Country             string
	Channel             string
	Carrier             string
	Label               string
	Priority            int
	ComplianceStanding  string
	CostPerSegmentMinor int64
	Currency            string
	Status              string
	// ConnectionID is the SMPP bind carrying this corridor. Null means the
	// corridor is defined but not yet wired, so it is skipped during failover.
	ConnectionID *uuid.UUID
}

// ListRoutes returns carrier routes, narrowed by country and channel when
// given. Ordered by priority within each country and channel, because priority
// is the order traffic actually attempts them — the list IS the routing table,
// not a report about it.
func ListRoutes(ctx context.Context, pool *pgxpool.Pool, country, channel *string) ([]Route, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, country, channel, carrier, label, priority, compliance_standing, connection_id,
		       cost_per_segment_minor, currency, status
		FROM routes
		WHERE ($1::text IS NULL OR country = $1)
		  AND ($2::text IS NULL OR channel = $2)
		ORDER BY country, channel, priority`, country, channel)
	if err != nil {
		return nil, fmt.Errorf("store: list routes: %w", err)
	}
	defer rows.Close()
	out := []Route{}
	for rows.Next() {
		var route Route
		if err := rows.Scan(&route.ID, &route.Country, &route.Channel, &route.Carrier,
			&route.Label, &route.Priority, &route.ComplianceStanding, &route.ConnectionID,
			&route.CostPerSegmentMinor, &route.Currency, &route.Status); err != nil {
			return nil, fmt.Errorf("store: scan route: %w", err)
		}
		out = append(out, route)
	}
	return out, rows.Err()
}

func SetRouteStatus(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, status string) error {
	tag, err := pool.Exec(ctx, `UPDATE routes SET status = $2 WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("store: set route status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveRoute swaps a route with its neighbour in the same country x channel
// corridor, across carriers.
//
// Carrier used to be part of the grouping, because priority then ranked the ways
// of reaching ONE network — "Jio Direct" ahead of "Jio via Aggregator A" — and
// said nothing about where Airtel sat. With four operator binds and
// priority-with-failover that is exactly the ordering the product now needs: a
// corridor is one ladder, and priority is what expresses "try Airtel, then Jio,
// then Vi, then BSNL". Carrier demotes to an ordinary attribute; two routes to
// the same carrier simply sit adjacent in the one ladder.
//
// The swap runs in one transaction with the unique constraint deferred by
// parking one row at a sentinel priority: without that, setting A to B's
// priority collides before B has moved out of the way.
func MoveRoute(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, up bool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: move route: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := func() error {
		var country, channel string
		var priority int
		if err := tx.QueryRow(ctx,
			`SELECT country, channel, priority FROM routes WHERE id = $1`, id,
		).Scan(&country, &channel, &priority); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}

		order, comparison := "DESC", "<"
		if !up {
			order, comparison = "ASC", ">"
		}
		var neighbourID uuid.UUID
		var neighbourPriority int
		err := tx.QueryRow(ctx, fmt.Sprintf(`
			SELECT id, priority FROM routes
			WHERE country = $1 AND channel = $2 AND priority %s $3
			ORDER BY priority %s LIMIT 1`, comparison, order),
			country, channel, priority).Scan(&neighbourID, &neighbourPriority)
		if errors.Is(err, pgx.ErrNoRows) {
			// Already at the end. Not an error: the console shows the arrow
			// regardless, and refusing would make a harmless click fail.
			return nil
		}
		if err != nil {
			return err
		}

		const parked = -1
		if _, err := tx.Exec(ctx, `UPDATE routes SET priority = $2 WHERE id = $1`,
			id, parked); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE routes SET priority = $2 WHERE id = $1`,
			neighbourID, priority); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE routes SET priority = $2 WHERE id = $1`,
			id, neighbourPriority)
		return err
	}(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CreateRoute adds a route at the end of its carrier's order in a corridor.
//
// Always last and always disabled. Priority ranks the whole corridor now, not
// one carrier within it, so a new path is appended to the end of the ladder and
// is tried after everything already proven. A route that went live the moment it
// was typed would put real traffic on an untested connection before anybody
// looked at it.
func CreateRoute(ctx context.Context, pool *pgxpool.Pool, route Route) (Route, error) {
	var created Route
	err := pool.QueryRow(ctx, `
		INSERT INTO routes (country, channel, carrier, label, priority,
		                    compliance_standing, cost_per_segment_minor, currency, status,
		                    connection_id)
		SELECT $1, $2, $3, $4,
		       COALESCE(MAX(priority), 0) + 1,
		       $5, $6, $7, 'disabled', $8
		  FROM routes
		 WHERE country = $1 AND channel = $2
		RETURNING id, country, channel, carrier, label, priority,
		          compliance_standing, connection_id, cost_per_segment_minor, currency, status`,
		route.Country, route.Channel, route.Carrier, route.Label,
		route.ComplianceStanding, route.CostPerSegmentMinor, route.Currency,
		route.ConnectionID,
	).Scan(&created.ID, &created.Country, &created.Channel, &created.Carrier,
		&created.Label, &created.Priority, &created.ComplianceStanding, &created.ConnectionID,
		&created.CostPerSegmentMinor, &created.Currency, &created.Status)
	if err != nil {
		return Route{}, fmt.Errorf("store: create route: %w", err)
	}
	return created, nil
}

// DeleteRoute removes a route and closes the gap it leaves.
//
// The priorities in a carrier's group are 1..n with no holes — the console
// renders them as an order, and a jump from 1 to 3 reads as a route somebody
// cannot see. Deleting the middle of three without this left exactly that.
//
// Refuses an active route. Removing the path traffic is currently taking is not
// a tidy-up, and the operator should have to disable it first and watch what
// happens before the row disappears.
func DeleteRoute(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: delete route: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var country, channel, carrier, status string
	var priority int
	if err := tx.QueryRow(ctx,
		`SELECT country, channel, carrier, priority, status FROM routes WHERE id = $1`, id,
	).Scan(&country, &channel, &carrier, &priority, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("store: delete route: %w", err)
	}
	if status == "active" {
		return ErrConflict
	}
	if _, err := tx.Exec(ctx, `DELETE FROM routes WHERE id = $1`, id); err != nil {
		return fmt.Errorf("store: delete route: %w", err)
	}
	// Everything below the hole moves up one, via the negative range.
	//
	// The direct `priority = priority - 1` reads correctly and is not safe: the
	// unique constraint is checked per row as the statement runs, and Postgres
	// updates rows in physical order, so moving 3 to 2 before 2 has moved to 1
	// raises a duplicate key on an update that ends up perfectly valid. Negating
	// first is collision-free because negation is one-to-one, and no route ever
	// holds a negative priority outside a transaction like this one.
	if _, err := tx.Exec(ctx, `
		UPDATE routes SET priority = -priority
		 WHERE country = $1 AND channel = $2 AND priority > $3`,
		country, channel, priority); err != nil {
		return fmt.Errorf("store: close route priority gap: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE routes SET priority = (-priority) - 1
		 WHERE country = $1 AND channel = $2 AND priority < 0`,
		country, channel); err != nil {
		return fmt.Errorf("store: close route priority gap: %w", err)
	}
	return tx.Commit(ctx)
}

// RateOverride is what one tenant pays instead of the default rate.
type RateOverride struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	TenantName      string
	Country         string
	Channel         string
	Category        *string
	PerSegmentMinor int64
	Currency        string
	UpdatedAt       time.Time
}

func ListRateOverrides(ctx context.Context, pool *pgxpool.Pool) ([]RateOverride, error) {
	rows, err := pool.Query(ctx, `
		SELECT o.id, o.tenant_id, t.name, o.country, o.channel, o.category,
		       o.per_segment_minor, o.currency, o.updated_at
		FROM rate_overrides o JOIN tenants t ON t.id = o.tenant_id
		ORDER BY t.name, o.country, o.channel`)
	if err != nil {
		return nil, fmt.Errorf("store: list rate overrides: %w", err)
	}
	defer rows.Close()
	out := []RateOverride{}
	for rows.Next() {
		var override RateOverride
		if err := rows.Scan(&override.ID, &override.TenantID, &override.TenantName,
			&override.Country, &override.Channel, &override.Category,
			&override.PerSegmentMinor, &override.Currency, &override.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan rate override: %w", err)
		}
		out = append(out, override)
	}
	return out, rows.Err()
}

func CreateRateOverride(ctx context.Context, pool *pgxpool.Pool, override RateOverride) (
	RateOverride, error) {

	err := pool.QueryRow(ctx, `
		INSERT INTO rate_overrides (tenant_id, country, channel, category,
		                            per_segment_minor, currency)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (tenant_id, country, channel, category)
		DO UPDATE SET per_segment_minor = EXCLUDED.per_segment_minor,
		              currency = EXCLUDED.currency, updated_at = now()
		RETURNING id, updated_at`,
		override.TenantID, override.Country, override.Channel, override.Category,
		override.PerSegmentMinor, override.Currency,
	).Scan(&override.ID, &override.UpdatedAt)
	if err != nil {
		return RateOverride{}, fmt.Errorf("store: create rate override: %w", err)
	}
	return override, nil
}

func UpdateRateOverride(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID,
	perSegmentMinor int64) error {

	tag, err := pool.Exec(ctx, `
		UPDATE rate_overrides SET per_segment_minor = $2, updated_at = now()
		WHERE id = $1`, id, perSegmentMinor)
	if err != nil {
		return fmt.Errorf("store: update rate override: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func DeleteRateOverride(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) error {
	tag, err := pool.Exec(ctx, `DELETE FROM rate_overrides WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: delete rate override: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func GetRoute(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (Route, error) {
	var route Route
	err := pool.QueryRow(ctx, `
		SELECT id, country, channel, carrier, label, priority, compliance_standing,
		       cost_per_segment_minor, currency, status
		FROM routes WHERE id = $1`, id).Scan(&route.ID, &route.Country, &route.Channel,
		&route.Carrier, &route.Label, &route.Priority, &route.ComplianceStanding,
		&route.CostPerSegmentMinor, &route.Currency, &route.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Route{}, ErrNotFound
	}
	if err != nil {
		return Route{}, fmt.Errorf("store: get route: %w", err)
	}
	return route, nil
}

// UpsertPricingRate sets the default price for a corridor. The currency is not
// a parameter: it is a property of the country, and letting a caller change it
// per edit would leave two rows for one corridor priced in different money.
// UpsertPricingRate writes a default rate and reports what it replaced.
//
// The previous value is returned because the audit line needs it: "changed the
// AE VOICE rate from 42 to 45" is actionable in an incident review and "the AE
// VOICE rate was changed" is not. Read here rather than in the handler so the
// two cannot drift, and so a caller cannot forget.
func UpsertPricingRate(ctx context.Context, pool *pgxpool.Pool, country, channel,
	category string, perSegmentMinor int64) (PricingRate, *int64, error) {

	var previous *int64
	var existing int64
	switch err := pool.QueryRow(ctx, `
		SELECT per_segment_minor FROM pricing_rates
		WHERE country = $1 AND channel = $2 AND category = $3`,
		country, channel, category).Scan(&existing); {
	case err == nil:
		previous = &existing
	case errors.Is(err, pgx.ErrNoRows):
		// No default yet: this is the first rate for the corridor.
	default:
		return PricingRate{}, nil, fmt.Errorf("store: read pricing rate: %w", err)
	}

	var rate PricingRate
	err := pool.QueryRow(ctx, `
		INSERT INTO pricing_rates (country, channel, category, per_segment_minor, currency)
		VALUES ($1, $2, $3, $4,
		        COALESCE((SELECT currency FROM pricing_rates
		                  WHERE country = $1 LIMIT 1), 'INR'))
		ON CONFLICT (country, channel, category)
		DO UPDATE SET per_segment_minor = EXCLUDED.per_segment_minor
		RETURNING country, channel, category, per_segment_minor, currency`,
		country, channel, category, perSegmentMinor,
	).Scan(&rate.Country, &rate.Channel, &rate.Category, &rate.PerSegmentMinor, &rate.Currency)
	if err != nil {
		return PricingRate{}, nil, fmt.Errorf("store: upsert pricing rate: %w", err)
	}
	return rate, previous, nil
}

// PendingSender is a sender awaiting an operator decision, with the tenant it
// belongs to. Cross-tenant by design: the approval queue's whole job is to show
// work from every customer in one list.
type PendingSender struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	TenantName string
	Header     string
	Channel    string
	Country    string
	Status     string
	CreatedAt  time.Time

	// Why it was refused, and the registry id it was given if it was approved.
	// Both are declared on the contract's queue item and neither was ever
	// filled, so the review dialog showed a rejected submission with no reason
	// on it — the one thing the operator needs in order to explain the decision
	// to the customer.
	RejectionReason *string
	RegistrationID  *string

	// The channel-specific proof-of-ownership fields the review dialog shows.
	//
	// An operator approving a sender is being asked "does this customer really
	// control this identity", and the answer lives in different columns per
	// channel: a caller-ID number for Voice, a domain and its DNS records for
	// Email, a Business display name for WhatsApp. All four are declared on the
	// contract's queue item and none was ever populated, so the dialog asked
	// for a decision while showing nothing to decide on.
	CallerIDNumber *string
	VoiceVerified  bool
	EmailDomain    *string
	FromAddress    *string
	DisplayName    *string
	DNSRecords     []SenderDNSRecord
}

// ListPendingSenders returns senders awaiting a decision, or — when a status is
// given — senders in that state.
//
// The default is pending_review because that is what a queue is for: the things
// still needing someone. But the screen offers a status filter, and the query
// used to hardcode pending_review, so choosing "rejected" narrowed a
// pending-only list to nothing. The filter looked broken because it was: no
// value other than the default could ever return a row.
func ListPendingSenders(ctx context.Context, pool *pgxpool.Pool, status *string) ([]PendingSender, error) {
	rows, err := pool.Query(ctx, `
		SELECT s.id, s.tenant_id, t.name, s.header, s.channel, s.country, s.status,
		       s.created_at, s.rejection_reason, s.external_id,
		       s.caller_id_number, s.voice_verified, s.email_domain,
		       s.from_address, s.display_name
		FROM sender_ids s JOIN tenants t ON t.id = s.tenant_id
		WHERE s.status = COALESCE($1, 'pending_review')
		ORDER BY s.created_at`, status)
	if err != nil {
		return nil, fmt.Errorf("store: list pending senders: %w", err)
	}
	defer rows.Close()
	out := []PendingSender{}
	for rows.Next() {
		var sender PendingSender
		if err := rows.Scan(&sender.ID, &sender.TenantID, &sender.TenantName, &sender.Header,
			&sender.Channel, &sender.Country, &sender.Status, &sender.CreatedAt,
			&sender.RejectionReason, &sender.RegistrationID,
			&sender.CallerIDNumber, &sender.VoiceVerified, &sender.EmailDomain,
			&sender.FromAddress, &sender.DisplayName); err != nil {
			return nil, fmt.Errorf("store: scan pending sender: %w", err)
		}
		out = append(out, sender)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Email senders carry their SPF/DKIM/DMARC rows. Approving an email sender
	// whose domain is not authenticated lets a tenant send as a domain they do
	// not control, so the per-record status is the single most important thing
	// on the dialog — and it was not being sent at all.
	emailIDs := make([]uuid.UUID, 0, len(out))
	for _, sender := range out {
		if sender.Channel == "EMAIL" {
			emailIDs = append(emailIDs, sender.ID)
		}
	}
	if len(emailIDs) > 0 {
		dnsRows, err := pool.Query(ctx, `
			SELECT sender_id, record_type, host, value, status
			FROM sender_dns_records WHERE sender_id = ANY($1)
			ORDER BY sender_id, record_type`, emailIDs)
		if err != nil {
			return nil, fmt.Errorf("store: list pending sender dns: %w", err)
		}
		defer dnsRows.Close()
		bySender := map[uuid.UUID][]SenderDNSRecord{}
		for dnsRows.Next() {
			var senderID uuid.UUID
			var record SenderDNSRecord
			if err := dnsRows.Scan(&senderID, &record.Type, &record.Host,
				&record.Value, &record.Status); err != nil {
				return nil, fmt.Errorf("store: scan pending sender dns: %w", err)
			}
			bySender[senderID] = append(bySender[senderID], record)
		}
		if err := dnsRows.Err(); err != nil {
			return nil, err
		}
		for i := range out {
			out[i].DNSRecords = bySender[out[i].ID]
		}
	}
	return out, nil
}

// PendingTemplate mirrors PendingSender for templates.
type PendingTemplate struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	TenantName string
	Name       string
	Channel    string
	Country    string
	Body       *string
	Category   *string
	Status     string
	CreatedAt  time.Time

	RejectionReason *string
}

// ListPendingTemplates mirrors ListPendingSenders: pending by default, any
// requested state when the screen's status filter names one.
func ListPendingTemplates(ctx context.Context, pool *pgxpool.Pool, status *string) ([]PendingTemplate, error) {
	rows, err := pool.Query(ctx, `
		SELECT p.id, p.tenant_id, t.name, p.name, p.channel, p.country, p.body,
		       p.category, p.status, p.created_at, p.rejection_reason
		FROM templates p JOIN tenants t ON t.id = p.tenant_id
		WHERE p.status = COALESCE($1, 'pending_review')
		ORDER BY p.created_at`, status)
	if err != nil {
		return nil, fmt.Errorf("store: list pending templates: %w", err)
	}
	defer rows.Close()
	out := []PendingTemplate{}
	for rows.Next() {
		var template PendingTemplate
		if err := rows.Scan(&template.ID, &template.TenantID, &template.TenantName,
			&template.Name, &template.Channel, &template.Country, &template.Body,
			&template.Category, &template.Status, &template.CreatedAt,
			&template.RejectionReason); err != nil {
			return nil, fmt.Errorf("store: scan pending template: %w", err)
		}
		out = append(out, template)
	}
	return out, rows.Err()
}

// DecideSender approves or rejects a sender ACROSS tenants — an operator action,
// so it deliberately does not run inside a tenant context.
func DecideSender(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID,
	status, reason string) (PendingSender, error) {

	var sender PendingSender
	err := pool.QueryRow(ctx, `
		UPDATE sender_ids SET status = $2, rejection_reason = NULLIF($3,'')
		WHERE id = $1
		RETURNING id, tenant_id, header, channel, country, status, created_at`,
		id, status, reason,
	).Scan(&sender.ID, &sender.TenantID, &sender.Header, &sender.Channel,
		&sender.Country, &sender.Status, &sender.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return PendingSender{}, ErrNotFound
	}
	if err != nil {
		return PendingSender{}, fmt.Errorf("store: decide sender: %w", err)
	}
	if err := pool.QueryRow(ctx, `SELECT name FROM tenants WHERE id = $1`,
		sender.TenantID).Scan(&sender.TenantName); err != nil {
		return PendingSender{}, fmt.Errorf("store: sender tenant name: %w", err)
	}
	return sender, nil
}

func DecideTemplate(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID,
	status, reason string) (PendingTemplate, error) {

	var template PendingTemplate
	err := pool.QueryRow(ctx, `
		UPDATE templates SET status = $2, rejection_reason = NULLIF($3,'')
		WHERE id = $1
		RETURNING id, tenant_id, name, channel, country, body, category, status, created_at`,
		id, status, reason,
	).Scan(&template.ID, &template.TenantID, &template.Name, &template.Channel,
		&template.Country, &template.Body, &template.Category, &template.Status,
		&template.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return PendingTemplate{}, ErrNotFound
	}
	if err != nil {
		return PendingTemplate{}, fmt.Errorf("store: decide template: %w", err)
	}
	if err := pool.QueryRow(ctx, `SELECT name FROM tenants WHERE id = $1`,
		template.TenantID).Scan(&template.TenantName); err != nil {
		return PendingTemplate{}, fmt.Errorf("store: template tenant name: %w", err)
	}
	return template, nil
}

// ListFlaggedTenants is the abuse queue: tenants an operator has flagged and
// not yet decided on.
func ListFlaggedTenants(ctx context.Context, pool *pgxpool.Pool) ([]OperatorTenant, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, name, country, status, created_at, flagged_at, flag_reason, throttled_at,
		       throttled_rate_per_second
		FROM tenants WHERE flagged_at IS NOT NULL ORDER BY flagged_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list flagged tenants: %w", err)
	}
	defer rows.Close()
	out := []OperatorTenant{}
	for rows.Next() {
		var tenant OperatorTenant
		if err := rows.Scan(&tenant.ID, &tenant.Name, &tenant.Country, &tenant.Status,
			&tenant.CreatedAt, &tenant.FlaggedAt, &tenant.FlagReason,
			&tenant.ThrottledAt, &tenant.ThrottledRatePerSecond); err != nil {
			return nil, fmt.Errorf("store: scan flagged tenant: %w", err)
		}
		out = append(out, tenant)
	}
	return out, rows.Err()
}

// ListAllSupportTickets is the operator's view: every tenant's tickets in one
// list. Deliberately not the tenant-scoped ListSupportTickets — an operator
// working a queue needs to see across customers, which is exactly what the
// tenant version must never do.
// SupportTicketFilter narrows the operator ticket list and positions one page.
type SupportTicketFilter struct {
	TenantID *uuid.UUID
	Status   *string
	Category *string
	Cursor   string
	Limit    int
}

// ListAllSupportTickets returns one page of tickets across every tenant, and
// the number matching the filter.
//
// status and category are applied here now. The contract declared both and this
// query read neither, so the console's two dropdowns rendered, accepted a
// choice, and returned the same rows — which reads as "there are no open
// tickets" rather than "the filter does nothing".
func ListAllSupportTickets(ctx context.Context, pool *pgxpool.Pool,
	filter SupportTicketFilter) ([]SupportTicket, int, string, error) {

	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cursorTime, cursorID, err := decodeLedgerCursor(filter.Cursor)
	if err != nil {
		return nil, 0, "", err
	}

	where := `
		WHERE ($1::uuid IS NULL OR t.tenant_id = $1)
		  AND ($2::text IS NULL OR t.status    = $2)
		  AND ($3::text IS NULL OR t.category  = $3)`

	var total int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM support_tickets t`+where,
		filter.TenantID, filter.Status, filter.Category).Scan(&total); err != nil {
		return nil, 0, "", fmt.Errorf("store: count support tickets: %w", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT t.id, t.tenant_id, n.name, t.subject, t.category, t.status,
		       t.created_at, t.updated_at
		FROM support_tickets t JOIN tenants n ON n.id = t.tenant_id`+where+`
		  AND ($4::timestamptz IS NULL OR (t.updated_at, t.id) < ($4, $5))
		ORDER BY t.updated_at DESC, t.id DESC
		LIMIT $6`,
		filter.TenantID, filter.Status, filter.Category, cursorTime, cursorID, limit+1)
	if err != nil {
		return nil, 0, "", fmt.Errorf("store: list all support tickets: %w", err)
	}
	defer rows.Close()
	out := []SupportTicket{}
	for rows.Next() {
		var ticket SupportTicket
		if err := rows.Scan(&ticket.ID, &ticket.TenantID, &ticket.TenantName,
			&ticket.Subject, &ticket.Category, &ticket.Status,
			&ticket.CreatedAt, &ticket.UpdatedAt); err != nil {
			return nil, 0, "", fmt.Errorf("store: scan ticket: %w", err)
		}
		out = append(out, ticket)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, "", err
	}

	next := ""
	if len(out) > limit {
		// Keyed on updated_at, which is what this list is ordered by. Using
		// created_at here would hand back a cursor that does not match the sort
		// and silently skip or repeat rows.
		next = encodeLedgerCursor(out[limit-1].UpdatedAt, out[limit-1].ID)
		out = out[:limit]
	}
	return out, total, next, nil
}

// GetSupportTicketAnyTenant reads one ticket without a tenant context.
func GetSupportTicketAnyTenant(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (
	SupportTicket, []SupportMessage, error) {

	var ticket SupportTicket
	err := pool.QueryRow(ctx, `
		SELECT t.id, t.tenant_id, n.name, t.subject, t.category, t.status,
		       t.created_at, t.updated_at
		FROM support_tickets t JOIN tenants n ON n.id = t.tenant_id
		WHERE t.id = $1`, id,
	).Scan(&ticket.ID, &ticket.TenantID, &ticket.TenantName, &ticket.Subject,
		&ticket.Category, &ticket.Status, &ticket.CreatedAt, &ticket.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SupportTicket{}, nil, ErrNotFound
	}
	if err != nil {
		return SupportTicket{}, nil, fmt.Errorf("store: get ticket: %w", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT id, author, author_name, body, created_at
		FROM support_messages WHERE ticket_id = $1 ORDER BY created_at, id`, id)
	if err != nil {
		return SupportTicket{}, nil, fmt.Errorf("store: ticket messages: %w", err)
	}
	defer rows.Close()
	var messages []SupportMessage
	for rows.Next() {
		var message SupportMessage
		if err := rows.Scan(&message.ID, &message.Author, &message.AuthorName,
			&message.Body, &message.CreatedAt); err != nil {
			return SupportTicket{}, nil, fmt.Errorf("store: scan ticket message: %w", err)
		}
		messages = append(messages, message)
	}
	return ticket, messages, rows.Err()
}

// AddOperatorTicketMessage posts a staff reply and moves the ticket to pending:
// the customer now owes the next word, which is what the queue sorts on.
func AddOperatorTicketMessage(ctx context.Context, pool *pgxpool.Pool, ticketID uuid.UUID,
	authorName, body string) error {

	var tenantID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT tenant_id FROM support_tickets WHERE id = $1`,
		ticketID).Scan(&tenantID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("store: ticket tenant: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO support_messages (tenant_id, ticket_id, author, author_name, body)
		VALUES ($1,$2,'operator',$3,$4)`, tenantID, ticketID, authorName, body); err != nil {
		return fmt.Errorf("store: add operator message: %w", err)
	}
	_, err := pool.Exec(ctx,
		`UPDATE support_tickets SET status = 'pending', updated_at = now() WHERE id = $1`,
		ticketID)
	if err != nil {
		return fmt.Errorf("store: bump ticket: %w", err)
	}
	return nil
}

func SetTicketStatusAnyTenant(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID,
	status string) error {

	tag, err := pool.Exec(ctx,
		`UPDATE support_tickets SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("store: set ticket status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecentSession is a sign-in, used by the operator's user-activity report.
type RecentSession struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	TenantName string
	UserName   string
	UserEmail  string
	CreatedAt  time.Time
}

func ListRecentSessions(ctx context.Context, pool *pgxpool.Pool, limit int) (
	[]RecentSession, error) {

	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := pool.Query(ctx, `
		SELECT s.id, tu.tenant_id, t.name, u.name, u.email, s.created_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		JOIN tenant_users tu ON tu.user_id = u.id
		JOIN tenants t ON t.id = tu.tenant_id
		ORDER BY s.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list recent sessions: %w", err)
	}
	defer rows.Close()
	out := []RecentSession{}
	for rows.Next() {
		var session RecentSession
		if err := rows.Scan(&session.ID, &session.TenantID, &session.TenantName,
			&session.UserName, &session.UserEmail, &session.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan session: %w", err)
		}
		out = append(out, session)
	}
	return out, rows.Err()
}

// PendingRegistration is one compliance registration awaiting an operator
// decision, joined to the tenant that submitted it.
type PendingRegistration struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	TenantName      string
	Country         string
	ObjectKey       string
	Status          string
	RejectionReason *string
	ExternalID      *string
	Fields          []byte
	CreatedAt       time.Time
}

// ListPendingRegistrations returns compliance registrations needing a decision.
//
// This did not exist, and neither did anything that called it. The operator
// approval queue merged senders and templates only, so a customer who submitted
// a DLT or EIN registration sat in pending_review with NO path to approval — no
// screen showed it and no endpoint could act on it. The e2e suite hid that by
// advancing registrations through the /v1/dev/advance-registration test hook,
// which stood in for an operator workflow that was never built.
//
// Same shape as ListPendingTemplates directly above, including the COALESCE
// default, so the three queues in one console behave identically.
func ListPendingRegistrations(ctx context.Context, pool *pgxpool.Pool, status *string) (
	[]PendingRegistration, error) {

	rows, err := pool.Query(ctx, `
		SELECT r.id, r.tenant_id, t.name, r.country, r.object_key, r.status,
		       r.rejection_reason, r.external_id, r.fields, r.created_at
		FROM registrations r JOIN tenants t ON t.id = r.tenant_id
		WHERE r.status = COALESCE($1, 'pending_review')
		ORDER BY r.created_at`, status)
	if err != nil {
		return nil, fmt.Errorf("store: list pending registrations: %w", err)
	}
	defer rows.Close()
	out := []PendingRegistration{}
	for rows.Next() {
		var reg PendingRegistration
		if err := rows.Scan(&reg.ID, &reg.TenantID, &reg.TenantName, &reg.Country,
			&reg.ObjectKey, &reg.Status, &reg.RejectionReason, &reg.ExternalID,
			&reg.Fields, &reg.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan pending registration: %w", err)
		}
		out = append(out, reg)
	}
	return out, rows.Err()
}

// DecideRegistration approves or rejects a compliance registration across
// tenants — an operator action, so like DecideSender it does not run inside a
// tenant context.
//
// Approving stamps external_id when the regime issues one. A real DLT or TCR
// approval carries the regulator's own identifier, and a registration marked
// approved without it cannot be quoted back to the carrier later.
func DecideRegistration(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID,
	status, reason, externalID string) (PendingRegistration, error) {

	var reg PendingRegistration
	err := pool.QueryRow(ctx, `
		UPDATE registrations
		   SET status = $2,
		       rejection_reason = NULLIF($3,''),
		       external_id = COALESCE(NULLIF($4,''), external_id),
		       updated_at = now()
		 WHERE id = $1
		RETURNING id, tenant_id, country, object_key, status, rejection_reason,
		          external_id, fields, created_at`,
		id, status, reason, externalID,
	).Scan(&reg.ID, &reg.TenantID, &reg.Country, &reg.ObjectKey, &reg.Status,
		&reg.RejectionReason, &reg.ExternalID, &reg.Fields, &reg.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return PendingRegistration{}, ErrNotFound
	}
	if err != nil {
		return PendingRegistration{}, fmt.Errorf("store: decide registration: %w", err)
	}
	return reg, nil
}

// ListOperatorEmails returns every staff address. Used by the boot-time check
// that looks for accounts still holding the password published in this
// repository.
func ListOperatorEmails(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT email FROM operator_users ORDER BY email`)
	if err != nil {
		return nil, fmt.Errorf("store: list operator emails: %w", err)
	}
	defer rows.Close()
	var emails []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, fmt.Errorf("store: scan operator email: %w", err)
		}
		emails = append(emails, email)
	}
	return emails, rows.Err()
}

// SelectRoute picks the path a message takes: the highest-priority ACTIVE route
// for a corridor, cheapest first among equals.
//
// The routes table described the network and nothing read it. Priorities could
// be reordered, routes enabled and disabled, and not one message changed —
// every live message was recorded with no carrier at all, so the deliverability
// screens worked only for seeded history. This is the read that makes the
// console's routing decisions real.
//
// Carrier is deliberately not part of the ordering key. Priority ranks the ways
// of reaching ONE carrier (Jio direct ahead of Jio via an aggregator), so
// picking across carriers falls to cost, which is the only honest tiebreak
// between two paths a customer cannot tell apart.
//
// ErrNotFound is a normal answer, not a failure: Email and WhatsApp do not go
// over a carrier at all, and no corridor is required to have a route yet.
func SelectRoute(ctx context.Context, pool *pgxpool.Pool, country, channel string) (Route, error) {
	var route Route
	err := pool.QueryRow(ctx, `
		SELECT id, country, channel, carrier, label, priority, compliance_standing,
		       cost_per_segment_minor, currency, status
		  FROM routes
		 WHERE country = $1 AND channel = $2 AND status = 'active'
		 ORDER BY priority, cost_per_segment_minor, carrier
		 LIMIT 1`, country, channel,
	).Scan(&route.ID, &route.Country, &route.Channel, &route.Carrier,
		&route.Label, &route.Priority, &route.ComplianceStanding,
		&route.CostPerSegmentMinor, &route.Currency, &route.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Route{}, ErrNotFound
	}
	if err != nil {
		return Route{}, fmt.Errorf("store: select route: %w", err)
	}
	return route, nil
}

// SelectRouteForCarrier picks the active route for one specific carrier in a
// corridor.
//
// Needed because a channel can have its gateway fixed by deployment
// configuration rather than by the routes table — RCS goes to whichever of
// Airtel or Vi this deployment holds credentials for. Recording the
// highest-priority route's carrier in that case attributes Airtel's traffic to
// Jio, and the deliverability-by-carrier report then blames the wrong network
// for every failure.
func SelectRouteForCarrier(ctx context.Context, pool *pgxpool.Pool,
	country, channel, carrier string) (Route, error) {

	var route Route
	err := pool.QueryRow(ctx, `
		SELECT id, country, channel, carrier, label, priority, compliance_standing,
		       cost_per_segment_minor, currency, status
		  FROM routes
		 WHERE country = $1 AND channel = $2 AND carrier = $3 AND status = 'active'
		 ORDER BY priority, cost_per_segment_minor
		 LIMIT 1`, country, channel, carrier,
	).Scan(&route.ID, &route.Country, &route.Channel, &route.Carrier,
		&route.Label, &route.Priority, &route.ComplianceStanding,
		&route.CostPerSegmentMinor, &route.Currency, &route.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Route{}, ErrNotFound
	}
	if err != nil {
		return Route{}, fmt.Errorf("store: select route for carrier: %w", err)
	}
	return route, nil
}
