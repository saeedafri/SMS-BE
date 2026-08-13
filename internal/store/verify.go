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

// VerifyChannelConfig is one channel a service may send an OTP over.
type VerifyChannelConfig struct {
	Channel  string `json:"channel"`
	SenderID string `json:"senderId"`
	Body     string `json:"body"`
}

// VerifyService is a reusable OTP configuration.
type VerifyService struct {
	ID              uuid.UUID
	Name            string
	Channels        []VerifyChannelConfig
	FallbackOrder   []string
	CodeLength      int
	CodeTTLSeconds  int
	MaxAttempts     int
	MaxPerPhone     int
	WindowSeconds   int
	CooldownSeconds int
	RegionAllowlist []string
	CreatedAt       time.Time
}

const verifyServiceColumns = `
	id, name, channels, fallback_order, code_length, code_ttl_seconds,
	max_attempts, max_per_phone, window_seconds, cooldown_seconds,
	region_allowlist, created_at`

func scanVerifyService(row pgx.Row) (VerifyService, error) {
	var service VerifyService
	var channels []byte
	err := row.Scan(&service.ID, &service.Name, &channels, &service.FallbackOrder,
		&service.CodeLength, &service.CodeTTLSeconds, &service.MaxAttempts,
		&service.MaxPerPhone, &service.WindowSeconds, &service.CooldownSeconds,
		&service.RegionAllowlist, &service.CreatedAt)
	if err != nil {
		return VerifyService{}, err
	}
	if len(channels) > 0 {
		_ = json.Unmarshal(channels, &service.Channels)
	}
	return service, nil
}

func ListVerifyServices(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]VerifyService, error) {
	var out []VerifyService
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+verifyServiceColumns+` FROM verify_services ORDER BY created_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			service, err := scanVerifyService(rows)
			if err != nil {
				return err
			}
			out = append(out, service)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: list verify services: %w", err)
	}
	return out, nil
}

func GetVerifyService(ctx context.Context, pool *pgxpool.Pool, id Identity,
	serviceID uuid.UUID) (VerifyService, error) {

	var service VerifyService
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		service, err = scanVerifyService(tx.QueryRow(ctx,
			`SELECT `+verifyServiceColumns+` FROM verify_services WHERE id = $1`, serviceID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return VerifyService{}, ErrNotFound
	}
	if err != nil {
		return VerifyService{}, fmt.Errorf("store: get verify service: %w", err)
	}
	return service, nil
}

func CreateVerifyService(ctx context.Context, pool *pgxpool.Pool, id Identity,
	service VerifyService) (VerifyService, error) {

	channels, err := json.Marshal(service.Channels)
	if err != nil {
		return VerifyService{}, err
	}
	if service.FallbackOrder == nil {
		service.FallbackOrder = []string{}
	}
	if service.RegionAllowlist == nil {
		service.RegionAllowlist = []string{}
	}

	var created VerifyService
	err = WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		created, err = scanVerifyService(tx.QueryRow(ctx, `
			INSERT INTO verify_services (tenant_id, name, channels, fallback_order,
			    code_length, code_ttl_seconds, max_attempts, max_per_phone,
			    window_seconds, cooldown_seconds, region_allowlist)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			RETURNING `+verifyServiceColumns,
			id.TenantID, service.Name, channels, service.FallbackOrder,
			service.CodeLength, service.CodeTTLSeconds, service.MaxAttempts,
			service.MaxPerPhone, service.WindowSeconds, service.CooldownSeconds,
			service.RegionAllowlist))
		return err
	})
	if err != nil {
		return VerifyService{}, fmt.Errorf("store: create verify service: %w", err)
	}
	return created, nil
}

// Verification is one OTP challenge.
type Verification struct {
	ID           uuid.UUID
	ServiceID    uuid.UUID
	Msisdn       string
	Country      string
	Channel      string
	CodeHash     []byte
	Status       string
	AttemptsUsed int
	MaxAttempts  int
	CostMinor    int64
	Currency     string
	FraudFlag    string
	ExpiresAt    time.Time
	VerifiedAt   *time.Time
	CreatedAt    time.Time
}

// CountRecentVerifications powers the per-phone rate limit. Counting requests
// for THIS number rather than for the tenant is what stops one attacker
// burning a tenant's whole budget and locking out every other user.
func CountRecentVerifications(ctx context.Context, pool *pgxpool.Pool, id Identity,
	msisdn string, window time.Duration) (int, error) {

	var count int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM verifications
			WHERE msisdn = $1 AND created_at > now() - $2::interval`,
			msisdn, fmt.Sprintf("%d seconds", int(window.Seconds()))).Scan(&count)
	})
	if err != nil {
		return 0, fmt.Errorf("store: count recent verifications: %w", err)
	}
	return count, nil
}

func CreateVerification(ctx context.Context, pool *pgxpool.Pool, id Identity,
	verification Verification) (Verification, error) {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO verifications (tenant_id, service_id, msisdn, country, channel,
			    code_hash, max_attempts, cost_minor, currency, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			RETURNING id, status, attempts_used, created_at`,
			id.TenantID, verification.ServiceID, verification.Msisdn, verification.Country,
			verification.Channel, verification.CodeHash, verification.MaxAttempts,
			verification.CostMinor, verification.Currency, verification.ExpiresAt,
		).Scan(&verification.ID, &verification.Status, &verification.AttemptsUsed,
			&verification.CreatedAt)
	})
	if err != nil {
		return Verification{}, fmt.Errorf("store: create verification: %w", err)
	}
	return verification, nil
}

// GetVerificationForUpdate locks the row so two simultaneous check requests
// cannot each read the same attempt count and both be allowed. Without the
// lock the attempt limit is advisory, and an attacker who fires guesses in
// parallel gets far more than max_attempts of them.
func GetVerificationForUpdate(ctx context.Context, tx pgx.Tx,
	verificationID uuid.UUID) (Verification, error) {

	var verification Verification
	err := tx.QueryRow(ctx, `
		SELECT id, service_id, msisdn, country, channel, code_hash, status,
		       attempts_used, max_attempts, cost_minor, currency, expires_at, created_at
		FROM verifications WHERE id = $1 FOR UPDATE`, verificationID,
	).Scan(&verification.ID, &verification.ServiceID, &verification.Msisdn,
		&verification.Country, &verification.Channel, &verification.CodeHash,
		&verification.Status, &verification.AttemptsUsed, &verification.MaxAttempts,
		&verification.CostMinor, &verification.Currency, &verification.ExpiresAt,
		&verification.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Verification{}, ErrNotFound
	}
	return verification, err
}

// CheckVerification applies one guess inside a single locked transaction, so
// the read of the attempt count and the write that increments it cannot be
// interleaved with another guess.
func CheckVerification(ctx context.Context, pool *pgxpool.Pool, id Identity,
	verificationID uuid.UUID,
	decide func(Verification) (status string, attemptsUsed int, err error),
) (Verification, error) {

	var result Verification
	// The decision's outcome is captured OUT here, never returned from the
	// transaction closure. Returning it would abort the transaction and roll
	// back the attempt increment, so every wrong guess would be forgotten and
	// the attempt limit would never be reached — unlimited guesses at a
	// six-digit code. A wrong code is a normal result, not a database error.
	var outcome error

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		verification, err := GetVerificationForUpdate(ctx, tx, verificationID)
		if err != nil {
			return err
		}
		status, attemptsUsed, decideErr := decide(verification)
		outcome = decideErr

		verifiedAt := "NULL"
		if status == "verified" {
			verifiedAt = "now()"
		}
		if err := tx.QueryRow(ctx, `
			UPDATE verifications
			SET status = $2, attempts_used = $3, verified_at = `+verifiedAt+`
			WHERE id = $1
			RETURNING id, service_id, msisdn, channel, status, attempts_used,
			          max_attempts, expires_at, created_at`,
			verificationID, status, attemptsUsed,
		).Scan(&result.ID, &result.ServiceID, &result.Msisdn, &result.Channel,
			&result.Status, &result.AttemptsUsed, &result.MaxAttempts,
			&result.ExpiresAt, &result.CreatedAt); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	return result, outcome
}

// ListVerifications returns a service's attempt history.
func ListVerifications(ctx context.Context, pool *pgxpool.Pool, id Identity,
	serviceID uuid.UUID, limit int) ([]Verification, int, error) {

	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []Verification
	var total int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM verifications WHERE service_id = $1`,
			serviceID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, service_id, msisdn, country, channel, status, attempts_used,
			       max_attempts, cost_minor, currency, fraud_flag, expires_at, created_at
			FROM verifications WHERE service_id = $1
			ORDER BY created_at DESC, id DESC LIMIT $2`, serviceID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var verification Verification
			if err := rows.Scan(&verification.ID, &verification.ServiceID,
				&verification.Msisdn, &verification.Country, &verification.Channel,
				&verification.Status, &verification.AttemptsUsed, &verification.MaxAttempts,
				&verification.CostMinor, &verification.Currency, &verification.FraudFlag,
				&verification.ExpiresAt, &verification.CreatedAt); err != nil {
				return err
			}
			out = append(out, verification)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, fmt.Errorf("store: list verifications: %w", err)
	}
	return out, total, nil
}
