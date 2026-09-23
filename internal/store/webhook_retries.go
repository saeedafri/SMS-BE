package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WebhookRetry is a failed delivery still owed another attempt.
type WebhookRetry struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	EndpointID uuid.UUID
	EventType  string
	Payload    []byte
	Attempt    int
}

// ScheduleWebhookRetry records that attempt is owed to endpoint at at.
func ScheduleWebhookRetry(ctx context.Context, pool *pgxpool.Pool, id Identity,
	endpointID uuid.UUID, eventType string, payload []byte, attempt int, at time.Time) error {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO webhook_retries (tenant_id, endpoint_id, event_type, payload,
			    attempt, next_attempt_at)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			id.TenantID, endpointID, eventType, payload, attempt, at)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: schedule webhook retry: %w", err)
	}
	return nil
}

// DueWebhookRetries lists pending retries whose time has come, across tenants.
func DueWebhookRetries(ctx context.Context, operator *pgxpool.Pool,
	now time.Time, limit int) ([]WebhookRetry, error) {

	rows, err := operator.Query(ctx, `
		SELECT id, tenant_id, endpoint_id, event_type, payload, attempt
		FROM webhook_retries
		WHERE state = 'pending' AND next_attempt_at <= $1
		ORDER BY next_attempt_at LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("store: due webhook retries: %w", err)
	}
	defer rows.Close()
	var due []WebhookRetry
	for rows.Next() {
		var retry WebhookRetry
		if err := rows.Scan(&retry.ID, &retry.TenantID, &retry.EndpointID,
			&retry.EventType, &retry.Payload, &retry.Attempt); err != nil {
			return nil, err
		}
		due = append(due, retry)
	}
	return due, rows.Err()
}

// ClaimWebhookRetry takes a due retry for one delivery attempt, reporting false
// when another instance got there first or it is no longer pending.
//
// The claim pushes next_attempt_at out to leaseUntil, so a process that dies
// mid-attempt leaves the retry to be picked up again once the lease lapses
// rather than stuck. That can deliver the same attempt twice; webhooks are
// at-least-once, and a lost event is the worse failure.
func ClaimWebhookRetry(ctx context.Context, pool *pgxpool.Pool, id Identity,
	retry WebhookRetry, now, leaseUntil time.Time) (bool, error) {

	var claimed bool
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE webhook_retries
			SET next_attempt_at = $4, updated_at = now()
			WHERE id = $1 AND attempt = $2 AND state = 'pending'
			  AND next_attempt_at <= $3`,
			retry.ID, retry.Attempt, now, leaseUntil)
		claimed = tag.RowsAffected() == 1
		return err
	})
	if err != nil {
		return false, fmt.Errorf("store: claim webhook retry: %w", err)
	}
	return claimed, nil
}

// SettleWebhookRetry ends a retry as succeeded or abandoned.
func SettleWebhookRetry(ctx context.Context, pool *pgxpool.Pool, id Identity,
	retryID uuid.UUID, state string) error {

	return updateWebhookRetry(ctx, pool, id, `
		UPDATE webhook_retries SET state = $2, updated_at = now() WHERE id = $1`,
		retryID, state)
}

// RescheduleWebhookRetry moves a retry on to its next attempt.
func RescheduleWebhookRetry(ctx context.Context, pool *pgxpool.Pool, id Identity,
	retryID uuid.UUID, attempt int, at time.Time) error {

	return updateWebhookRetry(ctx, pool, id, `
		UPDATE webhook_retries SET attempt = $2, next_attempt_at = $3, updated_at = now()
		WHERE id = $1`, retryID, attempt, at)
}

func updateWebhookRetry(ctx context.Context, pool *pgxpool.Pool, id Identity,
	query string, args ...any) error {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, query, args...)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: update webhook retry: %w", err)
	}
	return nil
}
