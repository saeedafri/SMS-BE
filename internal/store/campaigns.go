package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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

// ListCampaigns returns one page of campaigns, newest first, with the total
// across every page. Ordering is by created_at so the list is stable while
// someone pages it — the same order every other list in the product uses.
// CampaignFilter narrows the campaign list before it is paged.
//
// Before, never after: all three of these ran in the browser over the whole
// collection, and paging the list without moving them would have turned a
// working search into a search-within-this-page — the same trap the operator
// search test breaks on purpose to prevent.
type CampaignFilter struct {
	Status  *string
	Channel *string
	// Search is a case-insensitive substring over the campaign name.
	Search *string
	Page   int
	Limit  int
}

func ListCampaigns(ctx context.Context, pool *pgxpool.Pool, id Identity,
	filter CampaignFilter) ([]Campaign, int, error) {

	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	offset := pageOffset(filter.Page, limit)

	// One clause list, used by the count and the page query, so the total can
	// never describe a different set from the rows.
	const where = `
		WHERE ($1::text IS NULL OR c.status  = $1)
		  AND ($2::text IS NULL OR c.channel = $2)
		  AND ($3::text IS NULL OR c.name ILIKE '%' || $3 || '%')`

	var out []Campaign
	var total int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM campaigns c`+where,
			filter.Status, filter.Channel, filter.Search).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT `+campaignColumns+` FROM campaigns c`+where+`
			 ORDER BY c.created_at DESC, c.id DESC
			 LIMIT $4 OFFSET $5`,
			filter.Status, filter.Channel, filter.Search, limit, offset)
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
		return nil, 0, fmt.Errorf("store: list campaigns: %w", err)
	}
	return out, total, nil
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

// CampaignRecipient is one member of a campaign's audience and what became of
// it.
type CampaignRecipient struct {
	ContactID  uuid.UUID
	Identity   string
	Dispatched bool
}

// ListCancelledRecipients names the recipients a cancelled campaign never
// reached — the question counts.cancelled counts but cannot name.
//
// Derived rather than stored, deliberately: fan-out writes a message row when
// it reaches a recipient, so a campaign cancelled at 30,000 of 100,000 has
// 30,000 rows and the other 70,000 have none. Writing rows to say "this did not
// happen" would make cancelling a large campaign an expensive write at exactly
// the moment someone is trying to stop it.
//
// ONLY the cancelled half is derived this way, and that split is the point.
// A cancelled recipient is a COUNTERFACTUAL — who this run would have reached
// had it continued — so it is necessarily read against the list and the consent
// map as they stand now, and it carries the full audience rule. The dispatched
// half is a RECORD, and a record is not re-derived: it is read from the message
// log by DispatchedRecipients. Filtering that half by today's consent answered
// a question about what a campaign did with a fact about what is true today,
// and a contact who opted out after being messaged vanished from the campaign's
// own account of itself.
//
// The limits below therefore apply to the cancelled half alone. The list is
// mutable and the cursor is a position in it, so a list edited after the run
// changes the answer retroactively. Contacts created after the campaign started
// are excluded — the walk is newest-first, so without that guard a contact
// added last week would sort into the region the run had already passed.
//
// What cannot be excluded is a contact that existed before the run and joined
// the LIST afterwards: contact_list_members records no timestamp, so there is
// nothing to compare. Recording membership time would close it, and that is a
// migration rather than a query.
// Takes an offset rather than a page number because its caller pages across
// two blocks and this is the second one. A limit of 0 asks for the total
// alone — the caller needs it for the envelope even on a page the dispatched
// half filled completely.
func ListCancelledRecipients(ctx context.Context, pool *pgxpool.Pool, id Identity,
	campaign Campaign, offset, limit int) ([]CampaignRecipient, int, error) {

	if limit < 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	// Only a cancelled campaign has recipients it never reached. Anything else
	// dispatched its whole list, whatever the cursor happens to say.
	if campaign.Status != "cancelled" || campaign.ListID == nil {
		return nil, 0, nil
	}

	cursorTime, cursorID, err := decodeDispatchCursor(campaign.DispatchCursor)
	if err != nil {
		return nil, 0, fmt.Errorf("store: campaign recipients: %w", err)
	}

	// The audience as it stood for this run. send_started_at is null for a
	// campaign cancelled before it began, in which case nothing was dispatched
	// and every member is cancelled.
	where := `
		FROM contacts c
		WHERE EXISTS (SELECT 1 FROM contact_list_members m
		              WHERE m.contact_id = c.id AND m.list_id = $1)
		  AND ($4::timestamptz IS NULL OR c.created_at <= $4)` + reachableOnChannel
	args := []any{*campaign.ListID, campaign.Channel == "EMAIL", campaign.Channel,
		campaign.SendStartedAt}

	// Fan-out walks created_at DESC, id DESC and saves the cursor after each
	// page, so everything strictly older than the cursor is what it never
	// reached. No cursor at all means it was stopped before its first page and
	// the whole list went unreached.
	//
	// Appended with its arguments in the same statement, so the query cannot
	// name a placeholder the args slice does not supply — which is how this
	// function last returned a 500 on every campaign that had a list.
	if cursorTime != nil {
		where += ` AND (c.created_at, c.id) < ($5::timestamptz, $6::uuid)`
		args = append(args, cursorTime, cursorID)
	}

	var out []CampaignRecipient
	var total int
	err = WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) `+where, args...).Scan(&total); err != nil {
			return err
		}
		if limit == 0 {
			return nil
		}
		rows, err := tx.Query(ctx,
			`SELECT c.id, c.msisdn, coalesce(c.email, '') `+where+`
			 ORDER BY c.created_at DESC, c.id DESC
			 LIMIT $`+strconv.Itoa(len(args)+1)+` OFFSET $`+strconv.Itoa(len(args)+2),
			append(args, limit, offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var recipient CampaignRecipient
			var email string
			if err := rows.Scan(&recipient.ContactID, &recipient.Identity, &email); err != nil {
				return err
			}
			// Email campaigns address the mailbox; everything else the number.
			if campaign.Channel == "EMAIL" && email != "" {
				recipient.Identity = email
			}
			out = append(out, recipient)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, fmt.Errorf("store: campaign recipients: %w", err)
	}
	return out, total, nil
}
