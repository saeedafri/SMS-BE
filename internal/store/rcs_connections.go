package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RCSConnection is one RCS operator account. Platform configuration, like
// Connection: no tenant and no row-level security.
type RCSConnection struct {
	ID          uuid.UUID
	Label       string
	Vendor      string
	Environment string
	Settings    map[string]string

	// SecretsSealed is ciphertext and never leaves the server.
	SecretsSealed *string
	SecretsSetAt  *time.Time

	Status        string
	HealthStatus  string
	LastError     *string
	LastCheckedAt *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const rcsConnectionColumns = `id, label, vendor, environment, settings, secrets_sealed,
	secrets_set_at, status, health_status, last_error, last_checked_at, created_at, updated_at`

func scanRCSConnection(row pgx.Row) (RCSConnection, error) {
	var c RCSConnection
	var settings []byte
	err := row.Scan(&c.ID, &c.Label, &c.Vendor, &c.Environment, &settings, &c.SecretsSealed,
		&c.SecretsSetAt, &c.Status, &c.HealthStatus, &c.LastError, &c.LastCheckedAt,
		&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return c, err
	}
	c.Settings = map[string]string{}
	if err := json.Unmarshal(settings, &c.Settings); err != nil {
		return c, fmt.Errorf("store: rcs connection settings: %w", err)
	}
	return c, nil
}

// ListRCSConnections returns every account, narrowed to one environment when
// given.
func ListRCSConnections(ctx context.Context, pool *pgxpool.Pool, environment *string) ([]RCSConnection, error) {
	rows, err := pool.Query(ctx, `SELECT `+rcsConnectionColumns+` FROM rcs_connections
		WHERE ($1::text IS NULL OR environment = $1)
		ORDER BY vendor, environment, label`, environment)
	if err != nil {
		return nil, fmt.Errorf("store: list rcs connections: %w", err)
	}
	defer rows.Close()
	out := []RCSConnection{}
	for rows.Next() {
		connection, err := scanRCSConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, connection)
	}
	return out, rows.Err()
}

func GetRCSConnection(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (RCSConnection, error) {
	connection, err := scanRCSConnection(pool.QueryRow(ctx,
		`SELECT `+rcsConnectionColumns+` FROM rcs_connections WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return RCSConnection{}, ErrNotFound
	}
	if err != nil {
		return RCSConnection{}, fmt.Errorf("store: get rcs connection: %w", err)
	}
	return connection, nil
}

// CreateRCSConnection inserts an account, always disabled.
func CreateRCSConnection(ctx context.Context, pool *pgxpool.Pool, c RCSConnection) (RCSConnection, error) {
	// An account with no settings of its own takes every default, and a nil map
	// marshals to JSON null, which the object check refuses.
	if c.Settings == nil {
		c.Settings = map[string]string{}
	}
	settings, err := json.Marshal(c.Settings)
	if err != nil {
		return RCSConnection{}, err
	}
	created, err := scanRCSConnection(pool.QueryRow(ctx, `
		INSERT INTO rcs_connections (label, vendor, environment, settings, secrets_sealed, secrets_set_at)
		VALUES ($1, $2, $3, $4, $5, CASE WHEN $5::text IS NULL THEN NULL ELSE now() END)
		RETURNING `+rcsConnectionColumns,
		c.Label, c.Vendor, c.Environment, settings, c.SecretsSealed))
	if err != nil {
		return RCSConnection{}, fmt.Errorf("store: create rcs connection: %w", err)
	}
	return created, nil
}

// SetRCSConnectionSecrets replaces the sealed secrets.
func SetRCSConnectionSecrets(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, sealed string) error {
	tag, err := pool.Exec(ctx, `UPDATE rcs_connections
		SET secrets_sealed = $2, secrets_set_at = now(), updated_at = now() WHERE id = $1`, id, sealed)
	if err != nil {
		return fmt.Errorf("store: set rcs connection secrets: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRCSConnectionStatus enables or disables an account. Enabling a second
// account for the same operator and environment is ErrConflict.
func SetRCSConnectionStatus(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, status string) (RCSConnection, error) {
	updated, err := scanRCSConnection(pool.QueryRow(ctx, `UPDATE rcs_connections
		SET status = $2, updated_at = now() WHERE id = $1 RETURNING `+rcsConnectionColumns, id, status))
	if errors.Is(err, pgx.ErrNoRows) {
		return RCSConnection{}, ErrNotFound
	}
	if isUniqueViolation(err) {
		return RCSConnection{}, ErrConflict
	}
	if err != nil {
		return RCSConnection{}, fmt.Errorf("store: set rcs connection status: %w", err)
	}
	return updated, nil
}

// RecordRCSConnectionHealth stores the outcome of the last check. It does not
// move updated_at, which is what tells the reload a row's configuration
// changed.
func RecordRCSConnectionHealth(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID,
	health string, lastError *string) error {

	_, err := pool.Exec(ctx, `UPDATE rcs_connections
		SET health_status = $2, last_error = $3, last_checked_at = now() WHERE id = $1`,
		id, health, lastError)
	if err != nil {
		return fmt.Errorf("store: record rcs connection health: %w", err)
	}
	return nil
}

// RCSCarriersByPriority orders operators by the corridor's route priority,
// highest first. Operators with no active route follow, in the order given.
func RCSCarriersByPriority(ctx context.Context, pool *pgxpool.Pool, country string,
	carriers []string) ([]string, error) {

	rows, err := pool.Query(ctx, `
		SELECT carrier FROM routes
		WHERE country = $1 AND channel = 'RCS' AND status = 'active' AND carrier = ANY($2)
		GROUP BY carrier ORDER BY min(priority)`, country, carriers)
	if err != nil {
		return nil, fmt.Errorf("store: rcs carriers by priority: %w", err)
	}
	defer rows.Close()
	ordered := make([]string, 0, len(carriers))
	seen := map[string]bool{}
	for rows.Next() {
		var carrier string
		if err := rows.Scan(&carrier); err != nil {
			return nil, err
		}
		ordered, seen[carrier] = append(ordered, carrier), true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, carrier := range carriers {
		if !seen[carrier] {
			ordered = append(ordered, carrier)
		}
	}
	return ordered, nil
}
