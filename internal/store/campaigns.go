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

// Campaign is a batch send. Its per-message rows live in ClickHouse; the
// campaign row itself is small, mutable and foreign-keyed, so it stays here.
type Campaign struct {
	ID                 uuid.UUID
	Name               string
	Channel            string
	Country            string
	ListID             *uuid.UUID
	SenderID           uuid.UUID
	TemplateID         uuid.UUID
	FallbackChannel    *string
	FallbackSenderID   *uuid.UUID
	FallbackTemplateID *uuid.UUID
	Status             string
	ScheduledAt        *time.Time
	SendStartedAt      *time.Time
	Recipients         int
	SegmentsPerMessage int
	CostMinorMin       int64
	CostMinorMax       int64
	Currency           string
	RetryOf            *uuid.UUID
	// RetriedByCampaignID is derived, not stored: it is whichever campaign
	// names this one as its retry_of. Storing both directions would let them
	// disagree.
	RetriedByCampaignID *uuid.UUID
	CreatedAt           time.Time
}

const campaignColumns = `
	c.id, c.name, c.channel, c.country, c.list_id, c.sender_id, c.template_id,
	c.fallback_channel, c.fallback_sender_id, c.fallback_template_id,
	c.status, c.scheduled_at, c.send_started_at, c.recipients,
	c.segments_per_message, c.cost_minor_min, c.cost_minor_max, c.currency,
	c.retry_of, (SELECT r.id FROM campaigns r WHERE r.retry_of = c.id LIMIT 1),
	c.created_at`

func scanCampaign(row pgx.Row) (Campaign, error) {
	var campaign Campaign
	err := row.Scan(&campaign.ID, &campaign.Name, &campaign.Channel, &campaign.Country,
		&campaign.ListID, &campaign.SenderID, &campaign.TemplateID,
		&campaign.FallbackChannel, &campaign.FallbackSenderID, &campaign.FallbackTemplateID,
		&campaign.Status, &campaign.ScheduledAt, &campaign.SendStartedAt,
		&campaign.Recipients, &campaign.SegmentsPerMessage, &campaign.CostMinorMin,
		&campaign.CostMinorMax, &campaign.Currency, &campaign.RetryOf,
		&campaign.RetriedByCampaignID, &campaign.CreatedAt)
	return campaign, err
}

func ListCampaigns(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]Campaign, error) {
	var out []Campaign
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+campaignColumns+` FROM campaigns c ORDER BY c.created_at DESC, c.id DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			campaign, err := scanCampaign(rows)
			if err != nil {
				return err
			}
			out = append(out, campaign)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: list campaigns: %w", err)
	}
	return out, nil
}

func GetCampaign(ctx context.Context, pool *pgxpool.Pool, id Identity,
	campaignID uuid.UUID) (Campaign, error) {

	var campaign Campaign
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		campaign, err = scanCampaign(tx.QueryRow(ctx,
			`SELECT `+campaignColumns+` FROM campaigns c WHERE c.id = $1`, campaignID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	if err != nil {
		return Campaign{}, fmt.Errorf("store: get campaign: %w", err)
	}
	return campaign, nil
}

func CreateCampaign(ctx context.Context, pool *pgxpool.Pool, id Identity,
	campaign Campaign) (Campaign, error) {

	var created Campaign
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var newID uuid.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO campaigns (tenant_id, name, channel, country, list_id,
			    sender_id, template_id, fallback_channel, fallback_sender_id,
			    fallback_template_id, status, scheduled_at, recipients,
			    segments_per_message, cost_minor_min, cost_minor_max, currency, retry_of)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
			RETURNING id`,
			id.TenantID, campaign.Name, campaign.Channel, campaign.Country,
			campaign.ListID, campaign.SenderID, campaign.TemplateID,
			campaign.FallbackChannel, campaign.FallbackSenderID, campaign.FallbackTemplateID,
			campaign.Status, campaign.ScheduledAt, campaign.Recipients,
			campaign.SegmentsPerMessage, campaign.CostMinorMin, campaign.CostMinorMax,
			campaign.Currency, campaign.RetryOf,
		).Scan(&newID); err != nil {
			return err
		}
		var err error
		created, err = scanCampaign(tx.QueryRow(ctx,
			`SELECT `+campaignColumns+` FROM campaigns c WHERE c.id = $1`, newID))
		return err
	})
	if err != nil {
		return Campaign{}, fmt.Errorf("store: create campaign: %w", err)
	}
	return created, nil
}

// MarkCampaignSending records that fan-out has begun. Separate from creation so
// a scheduled campaign can sit queued without a send-started timestamp that
// would misreport when it actually ran.
func MarkCampaignSending(ctx context.Context, pool *pgxpool.Pool, id Identity,
	campaignID uuid.UUID) error {

	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE campaigns SET status = 'sending', send_started_at = now(),
			 updated_at = now() WHERE id = $1`, campaignID)
		return err
	})
}

// SetCampaignStatus lands the campaign on a terminal status once fan-out ends.
func SetCampaignStatus(ctx context.Context, pool *pgxpool.Pool, id Identity,
	campaignID uuid.UUID, status string) error {

	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE campaigns SET status = $2, updated_at = now() WHERE id = $1`,
			campaignID, status)
		return err
	})
}

// StuckCampaign is a campaign that started sending and never landed.
type StuckCampaign struct {
	ID        uuid.UUID
	Name      string
	StartedAt time.Time
}

// FindStuckCampaigns lists campaigns left in 'sending' since before cutoff.
//
// Fan-out sets 'sending' and then sets a terminal status when it finishes. Any
// path between those two that does not return normally — a ClickHouse blip
// mid-page, a deploy, a panic — leaves the row at 'sending' with nothing left
// running to move it. The customer sees a campaign that has been sending for
// days, and the delivered-versus-failed split they are billed against never
// appears.
func FindStuckCampaigns(ctx context.Context, pool *pgxpool.Pool, id Identity,
	cutoff time.Time, limit int) ([]StuckCampaign, error) {

	var stuck []StuckCampaign
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, name, COALESCE(send_started_at, updated_at)
			  FROM campaigns
			 WHERE status = 'sending'
			   AND COALESCE(send_started_at, updated_at) < $1
			 ORDER BY COALESCE(send_started_at, updated_at)
			 LIMIT $2`, cutoff, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row StuckCampaign
			if err := rows.Scan(&row.ID, &row.Name, &row.StartedAt); err != nil {
				return err
			}
			stuck = append(stuck, row)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: find stuck campaigns: %w", err)
	}
	return stuck, nil
}
