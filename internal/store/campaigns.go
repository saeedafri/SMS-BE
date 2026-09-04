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

	// PausedAt and CancelledAt are both nullable and can both be set: a paused
	// campaign that is then cancelled carries the two instants, and its
	// effective stop time is the EARLIER of them. Preferring the cancel instant
	// jumps a campaign's elapsed time forward by the whole held interval.
	PausedAt    *time.Time
	CancelledAt *time.Time
	// DispatchCursor is where fan-out reached, so a resume continues from the
	// exact recipient a pause stopped at. Empty means from the beginning.
	DispatchCursor string
}

const campaignColumns = `
	c.id, c.name, c.channel, c.country, c.list_id, c.sender_id, c.template_id,
	c.fallback_channel, c.fallback_sender_id, c.fallback_template_id,
	c.status, c.scheduled_at, c.send_started_at, c.recipients,
	c.segments_per_message, c.cost_minor_min, c.cost_minor_max, c.currency,
	c.retry_of, (SELECT r.id FROM campaigns r WHERE r.retry_of = c.id LIMIT 1),
	c.created_at, c.paused_at, c.cancelled_at, coalesce(c.dispatch_cursor, '')`

func scanCampaign(row pgx.Row) (Campaign, error) {
	var campaign Campaign
	err := row.Scan(&campaign.ID, &campaign.Name, &campaign.Channel, &campaign.Country,
		&campaign.ListID, &campaign.SenderID, &campaign.TemplateID,
		&campaign.FallbackChannel, &campaign.FallbackSenderID, &campaign.FallbackTemplateID,
		&campaign.Status, &campaign.ScheduledAt, &campaign.SendStartedAt,
		&campaign.Recipients, &campaign.SegmentsPerMessage, &campaign.CostMinorMin,
		&campaign.CostMinorMax, &campaign.Currency, &campaign.RetryOf,
		&campaign.RetriedByCampaignID, &campaign.CreatedAt,
		&campaign.PausedAt, &campaign.CancelledAt, &campaign.DispatchCursor)
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
		// Never over a halt. Fan-out marks the campaign sending on its way in,
		// and between a resume returning and its dispatch goroutine starting
		// there is a window in which someone can pause or cancel again. Without
		// this guard that second halt is silently undone and the campaign sends
		// anyway — the exact failure the brake exists to prevent.
		_, err := tx.Exec(ctx,
			`UPDATE campaigns SET status = 'sending', send_started_at = now(),
			 updated_at = now()
			 WHERE id = $1 AND status NOT IN ('paused','cancelled')`, campaignID)
		return err
	})
}

// SetCampaignStatus lands the campaign on a terminal status once fan-out ends.
func SetCampaignStatus(ctx context.Context, pool *pgxpool.Pool, id Identity,
	campaignID uuid.UUID, status string) error {

	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		// Cancelled is terminal. The landing writes — fan-out finishing, the
		// stuck-campaign sweep tidying up afterwards — must not turn a campaign
		// somebody deliberately stopped back into 'sent'.
		_, err := tx.Exec(ctx,
			`UPDATE campaigns SET status = $2, updated_at = now()
			 WHERE id = $1 AND status <> 'cancelled'`,
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

// Campaign halt.
//
// The three transitions share one function because they share the thing that
// makes them safe: the row is locked before its status is read, so two halts
// arriving together cannot both decide they were the first. Without the lock a
// concurrent pause and cancel both see 'sending', both write, and the campaign
// ends up in whichever state committed last with the other's timestamp beside
// it.
var (
	// ErrCampaignHaltIllegal is a transition the state machine forbids — a
	// resume on a campaign that is not paused, a pause on a finished one.
	ErrCampaignHaltIllegal = errors.New("store: campaign cannot make that transition")
)

// haltTransitions is the state machine, written out rather than reasoned about
// at each call site.
var haltTransitions = map[string]map[string]bool{
	// Pausing a scheduled campaign is legal and means "do not start at the
	// scheduled time".
	"pause":  {"sending": true, "queued": true, "scheduled": true},
	"resume": {"paused": true},
	// Cancelling a paused campaign is a real path: it is how someone who hit
	// the brake decides not to continue.
	"cancel": {"sending": true, "queued": true, "scheduled": true, "paused": true},
}

// HaltCampaign applies pause, resume or cancel and returns the campaign as it
// now stands. ErrNotFound if the id is not this tenant's — checked before the
// transition, so a probe cannot tell "not yours" from "wrong state".
func HaltCampaign(ctx context.Context, pool *pgxpool.Pool, id Identity,
	campaignID uuid.UUID, action string) (Campaign, error) {

	var campaign Campaign
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx,
			`SELECT status FROM campaigns WHERE id = $1 FOR UPDATE`,
			campaignID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if !haltTransitions[action][status] {
			return fmt.Errorf("%w: %s from %s", ErrCampaignHaltIllegal, action, status)
		}

		var query string
		switch action {
		case "pause":
			query = `UPDATE campaigns SET status = 'paused', paused_at = now(),
			         updated_at = now() WHERE id = $1`
		case "resume":
			// paused_at is cleared rather than kept. Leaving the previous pause
			// instant behind is how a later elapsed-time calculation counts a
			// hold that has already ended.
			query = `UPDATE campaigns SET status = 'sending', paused_at = NULL,
			         updated_at = now() WHERE id = $1`
		case "cancel":
			// paused_at is deliberately NOT cleared. Both instants stand, and
			// the earlier one is the campaign's real stop time.
			query = `UPDATE campaigns SET status = 'cancelled', cancelled_at = now(),
			         updated_at = now() WHERE id = $1`
		default:
			return fmt.Errorf("store: unknown halt action %q", action)
		}
		if _, err := tx.Exec(ctx, query, campaignID); err != nil {
			return err
		}

		row := tx.QueryRow(ctx,
			`SELECT `+campaignColumns+` FROM campaigns c WHERE c.id = $1`, campaignID)
		var scanErr error
		campaign, scanErr = scanCampaign(row)
		return scanErr
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrCampaignHaltIllegal) {
			return Campaign{}, err
		}
		return Campaign{}, fmt.Errorf("store: halt campaign: %w", err)
	}
	return campaign, nil
}

// SaveDispatchCursor records how far fan-out has reached, so a resume picks up
// from the same recipient rather than restarting the list.
func SaveDispatchCursor(ctx context.Context, pool *pgxpool.Pool, id Identity,
	campaignID uuid.UUID, cursor string) error {

	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE campaigns SET dispatch_cursor = $2, updated_at = now() WHERE id = $1`,
			campaignID, cursor)
		return err
	})
}

// CampaignStatus reads just the status. Fan-out calls it between pages, so it
// stays a single indexed lookup rather than a full campaign read.
func CampaignStatus(ctx context.Context, pool *pgxpool.Pool, id Identity,
	campaignID uuid.UUID) (string, error) {

	status := ""
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT status FROM campaigns WHERE id = $1`, campaignID).Scan(&status)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: campaign status: %w", err)
	}
	return status, nil
}
