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

// Connection is one operator SMPP bind.
//
// Platform configuration, not tenant data: there is no tenant_id and no
// row-level security policy, because a bind belongs to Textify's relationship
// with an operator rather than to any customer. The boundary is that only an
// authenticated operator reaches the handler.
type Connection struct {
	ID          uuid.UUID
	Label       string
	Carrier     string
	Environment string
	Host        string
	Port        int
	SystemID    string
	SystemType  *string
	BindType    string

	// PasswordEncrypted is ciphertext and never leaves this layer. Nothing in
	// the API package reads it; the send path decrypts it when it binds.
	PasswordEncrypted *string
	PasswordSetAt     *time.Time

	MaxTps                  int
	WindowSize              int
	EnquireLinkSeconds      int
	ReconnectBackoffSeconds int

	Status       string
	HealthStatus string
	LastBoundAt  *time.Time
	LastError    *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const connectionColumns = `id, label, carrier, environment, host, port, system_id,
	system_type, bind_type, password_encrypted, password_set_at, max_tps, window_size,
	enquire_link_seconds, reconnect_backoff_seconds, status, health_status,
	last_bound_at, last_error, created_at, updated_at`

func scanConnection(row pgx.Row) (Connection, error) {
	var c Connection
	err := row.Scan(&c.ID, &c.Label, &c.Carrier, &c.Environment, &c.Host, &c.Port,
		&c.SystemID, &c.SystemType, &c.BindType, &c.PasswordEncrypted, &c.PasswordSetAt,
		&c.MaxTps, &c.WindowSize, &c.EnquireLinkSeconds, &c.ReconnectBackoffSeconds,
		&c.Status, &c.HealthStatus, &c.LastBoundAt, &c.LastError, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// ListConnections returns every bind, newest first, narrowed by carrier and
// environment when either is given.
//
// Unpaged on purpose: the count is bounded by how many operator binds the
// platform holds, not by customer growth.
func ListConnections(ctx context.Context, pool *pgxpool.Pool, carrier, environment *string) (
	[]Connection, error) {

	rows, err := pool.Query(ctx, `
		SELECT `+connectionColumns+` FROM connections
		WHERE ($1::text IS NULL OR carrier     = $1)
		  AND ($2::text IS NULL OR environment = $2)
		ORDER BY carrier, environment, label`, carrier, environment)
	if err != nil {
		return nil, fmt.Errorf("store: list connections: %w", err)
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		connection, err := scanConnection(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan connection: %w", err)
		}
		out = append(out, connection)
	}
	return out, rows.Err()
}

func GetConnection(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (Connection, error) {
	connection, err := scanConnection(pool.QueryRow(ctx,
		`SELECT `+connectionColumns+` FROM connections WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	if err != nil {
		return Connection{}, fmt.Errorf("store: get connection: %w", err)
	}
	return connection, nil
}

// CreateConnection inserts a bind. Status is not a parameter: a connection is
// always created disabled, and enabling it is a separate audited decision.
func CreateConnection(ctx context.Context, pool *pgxpool.Pool, c Connection) (Connection, error) {
	created, err := scanConnection(pool.QueryRow(ctx, `
		INSERT INTO connections (label, carrier, environment, host, port, system_id,
		    system_type, bind_type, password_encrypted, password_set_at, max_tps,
		    window_size, enquire_link_seconds, reconnect_backoff_seconds)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING `+connectionColumns,
		c.Label, c.Carrier, c.Environment, c.Host, c.Port, c.SystemID,
		c.SystemType, c.BindType, c.PasswordEncrypted, c.PasswordSetAt, c.MaxTps,
		c.WindowSize, c.EnquireLinkSeconds, c.ReconnectBackoffSeconds))
	if isUniqueViolation(err) {
		return Connection{}, ErrConflict
	}
	if err != nil {
		return Connection{}, fmt.Errorf("store: create connection: %w", err)
	}
	return created, nil
}

// ConnectionPatch carries only the fields a caller actually supplied. A nil
// field is untouched — in particular a nil PasswordEncrypted leaves the stored
// password and its set-at timestamp exactly as they were.
type ConnectionPatch struct {
	Label                   *string
	Carrier                 *string
	Environment             *string
	Host                    *string
	Port                    *int
	SystemID                *string
	SystemType              *string
	BindType                *string
	PasswordEncrypted       *string
	MaxTps                  *int
	WindowSize              *int
	EnquireLinkSeconds      *int
	ReconnectBackoffSeconds *int
}

// UpdateConnection applies a patch.
//
// COALESCE per column rather than a built string of SET clauses: the field list
// is fixed by the contract, and assembling SQL from whichever fields happened to
// be present is how an update path drifts from its create sibling.
func UpdateConnection(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID,
	patch ConnectionPatch) (Connection, error) {

	// password_set_at moves only when a password is actually supplied, so the
	// console's "last rotated" is the truth rather than "last edited".
	updated, err := scanConnection(pool.QueryRow(ctx, `
		UPDATE connections SET
		    label                     = COALESCE($2, label),
		    carrier                   = COALESCE($3, carrier),
		    environment               = COALESCE($4, environment),
		    host                      = COALESCE($5, host),
		    port                      = COALESCE($6, port),
		    system_id                 = COALESCE($7, system_id),
		    system_type               = COALESCE($8, system_type),
		    bind_type                 = COALESCE($9, bind_type),
		    password_encrypted        = COALESCE($10, password_encrypted),
		    password_set_at           = CASE WHEN $10::text IS NULL THEN password_set_at
		                                     ELSE now() END,
		    max_tps                   = COALESCE($11, max_tps),
		    window_size               = COALESCE($12, window_size),
		    enquire_link_seconds      = COALESCE($13, enquire_link_seconds),
		    reconnect_backoff_seconds = COALESCE($14, reconnect_backoff_seconds),
		    updated_at                = now()
		WHERE id = $1
		RETURNING `+connectionColumns,
		id, patch.Label, patch.Carrier, patch.Environment, patch.Host, patch.Port,
		patch.SystemID, patch.SystemType, patch.BindType, patch.PasswordEncrypted,
		patch.MaxTps, patch.WindowSize, patch.EnquireLinkSeconds,
		patch.ReconnectBackoffSeconds))
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	if isUniqueViolation(err) {
		return Connection{}, ErrConflict
	}
	if err != nil {
		return Connection{}, fmt.Errorf("store: update connection: %w", err)
	}
	return updated, nil
}

// SetConnectionStatus enables or disables a bind.
func SetConnectionStatus(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, status string) (
	Connection, error) {

	updated, err := scanConnection(pool.QueryRow(ctx, `
		UPDATE connections SET status = $2, updated_at = now() WHERE id = $1
		RETURNING `+connectionColumns, id, status))
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	if err != nil {
		return Connection{}, fmt.Errorf("store: set connection status: %w", err)
	}
	return updated, nil
}

// CountRoutesUsingConnection reports how many corridors point at a bind, so a
// delete can refuse with a number the operator can act on rather than a bare
// foreign-key error.
func CountRoutesUsingConnection(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (int, error) {
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM routes WHERE connection_id = $1`, id).Scan(&count); err != nil {
		return 0, fmt.Errorf("store: count routes using connection: %w", err)
	}
	return count, nil
}

func DeleteConnection(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) error {
	tag, err := pool.Exec(ctx, `DELETE FROM connections WHERE id = $1`, id)
	if isForeignKeyViolation(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("store: delete connection: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordConnectionHealth stores the outcome of a bind attempt. Deliberately
// separate from status: proving a bind works and putting live traffic on it are
// two different decisions, and testing must never enable anything.
func RecordConnectionHealth(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID,
	health string, lastError *string, boundAt *time.Time) error {

	_, err := pool.Exec(ctx, `
		UPDATE connections
		   SET health_status  = $2,
		       last_error     = $3,
		       last_bound_at  = COALESCE($4, last_bound_at),
		       updated_at     = now()
		 WHERE id = $1`, id, health, lastError, boundAt)
	if err != nil {
		return fmt.Errorf("store: record connection health: %w", err)
	}
	return nil
}
