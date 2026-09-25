package store

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The operator console's view of what every tenant sent: campaigns, journeys
// and the messages under them. Every function here reads across tenants, so
// every one takes the OPERATOR pool — on the tenant pool RLS answers with an
// empty list, not an error.

// Author is the person behind a send, as recorded when they did it.
type Author struct {
	UserID *uuid.UUID
	Name   *string
	Email  *string
}

// SendOwner is a campaign or journey as a page of messages needs it: its
// name, which campaign messages do not carry, and who is answerable for it.
type SendOwner struct {
	Name   string
	Author Author
}

// OperatorCampaign is one campaign row in the console, with its tenant and
// the person who created it.
type OperatorCampaign struct {
	TenantID        uuid.UUID
	TenantName      string
	ID              uuid.UUID
	Name            string
	Channel         string
	FallbackChannel *string
	Country         string
	Status          string
	SenderHeader    string
	TemplateName    string
	ListName        *string
	Recipients      int
	Withheld        int
	CostMinorMin    int64
	CostMinorMax    int64
	Currency        string
	RetryOf         *uuid.UUID
	CreatedBy       Author
	CreatedAt       time.Time
	ScheduledAt     *time.Time
	SendStartedAt   *time.Time
	PausedAt        *time.Time
	CancelledAt     *time.Time
}

// OperatorSendFilter narrows the campaign and journey lists. Nil and zero
// values mean "any".
type OperatorSendFilter struct {
	TenantID *uuid.UUID
	Status   *string
	Channel  *string
	// Search matches the campaign or journey name, and the creator's name or
	// email, case-insensitively.
	Search *string
	From   *time.Time
	To     *time.Time
	Page   int
	Limit  int
}

func (f OperatorSendFilter) pageLimit() (int, int) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	return limit, pageOffset(f.Page, limit)
}

func ListOperatorCampaigns(ctx context.Context, pool *pgxpool.Pool,
	filter OperatorSendFilter) ([]OperatorCampaign, int, error) {

	limit, offset := filter.pageLimit()
	// A fallback leg counts as the campaign's channel too: "every RCS
	// campaign" includes the SMS campaigns that fell back to RCS and the RCS
	// ones that fell back to SMS, because both put RCS messages on a handset.
	const where = `
		WHERE ($1::uuid IS NULL OR c.tenant_id = $1)
		  AND ($2::text IS NULL OR c.status = $2)
		  AND ($3::text IS NULL OR c.channel = $3 OR c.fallback_channel = $3)
		  AND ($4::text IS NULL OR c.name ILIKE '%' || $4 || '%'
		       OR c.created_by_name ILIKE '%' || $4 || '%'
		       OR c.created_by_email ILIKE '%' || $4 || '%')
		  AND ($5::timestamptz IS NULL OR c.created_at >= $5)
		  AND ($6::timestamptz IS NULL OR c.created_at <  $6)`
	args := []any{filter.TenantID, filter.Status, filter.Channel, filter.Search,
		filter.From, filter.To}

	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM campaigns c`+where,
		args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: count operator campaigns: %w", err)
	}
	rows, err := pool.Query(ctx, `
		SELECT c.tenant_id, t.name, c.id, c.name, c.channel, c.fallback_channel,
		       c.country, c.status, coalesce(s.header, ''), coalesce(tp.name, ''),
		       l.name, c.recipients,
		       (SELECT count(*) FROM withheld_contacts w WHERE w.campaign_id = c.id),
		       c.cost_minor_min, c.cost_minor_max, c.currency, c.retry_of,
		       c.created_by_user_id, c.created_by_name, c.created_by_email,
		       c.created_at, c.scheduled_at, c.send_started_at, c.paused_at, c.cancelled_at
		FROM campaigns c
		JOIN tenants t          ON t.id = c.tenant_id
		LEFT JOIN sender_ids s  ON s.id = c.sender_id
		LEFT JOIN templates tp  ON tp.id = c.template_id
		LEFT JOIN contact_lists l ON l.id = c.list_id`+where+`
		ORDER BY c.created_at DESC, c.id DESC
		LIMIT $7 OFFSET $8`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: list operator campaigns: %w", err)
	}
	defer rows.Close()
	var out []OperatorCampaign
	for rows.Next() {
		var c OperatorCampaign
		if err := rows.Scan(&c.TenantID, &c.TenantName, &c.ID, &c.Name, &c.Channel,
			&c.FallbackChannel, &c.Country, &c.Status, &c.SenderHeader, &c.TemplateName,
			&c.ListName, &c.Recipients, &c.Withheld, &c.CostMinorMin, &c.CostMinorMax,
			&c.Currency, &c.RetryOf, &c.CreatedBy.UserID, &c.CreatedBy.Name,
			&c.CreatedBy.Email, &c.CreatedAt, &c.ScheduledAt, &c.SendStartedAt,
			&c.PausedAt, &c.CancelledAt); err != nil {
			return nil, 0, fmt.Errorf("store: scan operator campaign: %w", err)
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// OperatorJourney is one journey row in the console.
type OperatorJourney struct {
	TenantID    uuid.UUID
	TenantName  string
	ID          uuid.UUID
	Name        string
	Status      string
	TriggerType string
	ListName    *string
	Channels    []string
	Recipients  int
	Enrolled    int
	Withheld    int
	CreatedBy   Author
	ActivatedBy Author
	CreatedAt   time.Time
	ActivatedAt *time.Time
}

func ListOperatorJourneys(ctx context.Context, pool *pgxpool.Pool,
	filter OperatorSendFilter) ([]OperatorJourney, int, error) {

	limit, offset := filter.pageLimit()
	const where = `
		WHERE ($1::uuid IS NULL OR j.tenant_id = $1)
		  AND ($2::text IS NULL OR j.status = $2)
		  AND ($3::text IS NULL OR EXISTS (SELECT 1 FROM jsonb_array_elements(j.steps) s
		                                   WHERE s->>'channel' = $3))
		  AND ($4::text IS NULL OR j.name ILIKE '%' || $4 || '%'
		       OR j.created_by_name ILIKE '%' || $4 || '%'
		       OR j.created_by_email ILIKE '%' || $4 || '%')
		  AND ($5::timestamptz IS NULL OR j.created_at >= $5)
		  AND ($6::timestamptz IS NULL OR j.created_at <  $6)`
	args := []any{filter.TenantID, filter.Status, filter.Channel, filter.Search,
		filter.From, filter.To}

	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM journeys j`+where,
		args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: count operator journeys: %w", err)
	}
	rows, err := pool.Query(ctx, `
		SELECT j.tenant_id, t.name, j.id, j.name, j.status, j.trigger_type, l.name,
		       ARRAY(SELECT DISTINCT s->>'channel' FROM jsonb_array_elements(j.steps) s
		             WHERE s->>'channel' IS NOT NULL ORDER BY 1),
		       j.recipients,
		       (SELECT count(*) FROM journey_enrollments e WHERE e.journey_id = j.id),
		       (SELECT count(*) FROM withheld_contacts w WHERE w.journey_id = j.id),
		       j.created_by_user_id, j.created_by_name, j.created_by_email,
		       j.activated_by_user_id, j.activated_by_name, j.activated_by_email,
		       j.created_at, j.activated_at
		FROM journeys j
		JOIN tenants t ON t.id = j.tenant_id
		LEFT JOIN contact_lists l ON l.id = j.trigger_list_id`+where+`
		ORDER BY j.created_at DESC, j.id DESC
		LIMIT $7 OFFSET $8`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: list operator journeys: %w", err)
	}
	defer rows.Close()
	var out []OperatorJourney
	for rows.Next() {
		var j OperatorJourney
		if err := rows.Scan(&j.TenantID, &j.TenantName, &j.ID, &j.Name, &j.Status,
			&j.TriggerType, &j.ListName, &j.Channels, &j.Recipients, &j.Enrolled,
			&j.Withheld, &j.CreatedBy.UserID, &j.CreatedBy.Name, &j.CreatedBy.Email,
			&j.ActivatedBy.UserID, &j.ActivatedBy.Name, &j.ActivatedBy.Email,
			&j.CreatedAt, &j.ActivatedAt); err != nil {
			return nil, 0, fmt.Errorf("store: scan operator journey: %w", err)
		}
		out = append(out, j)
	}
	return out, total, rows.Err()
}

// SendCounts is one campaign's or journey's messages by contract status.
type SendCounts struct {
	CampaignCounts
	Total     int
	CostMinor int64
}

// CountMessagesByOwner counts the messages of several campaigns (column
// "campaign_id") or journeys ("journey_id") across tenants, in one query.
//
// tenant_id is in the WHERE even though the owner ids are unique on their
// own: it leads the table's sort key, so it is what lets ClickHouse skip every
// other tenant's parts instead of scanning the warehouse for a page of twenty.
func CountMessagesByOwner(ctx context.Context, conn driver.Conn, column string,
	tenantIDs, ownerIDs []uuid.UUID) (map[uuid.UUID]SendCounts, error) {

	out := map[uuid.UUID]SendCounts{}
	if len(ownerIDs) == 0 {
		return out, nil
	}
	if column != "campaign_id" && column != "journey_id" {
		return nil, fmt.Errorf("store: cannot count messages by %q", column)
	}
	rows, err := conn.Query(ctx, `
		SELECT assumeNotNull(`+column+`), status, count(),
		       countIf(read_at IS NOT NULL), sum(cost_minor)
		FROM messages FINAL
		WHERE tenant_id IN (?) AND `+column+` IN (?)
		GROUP BY `+column+`, status`, tenantIDs, ownerIDs)
	if err != nil {
		return nil, fmt.Errorf("store: count messages by owner: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var owner uuid.UUID
		var status string
		var total, read uint64
		var cost int64
		if err := rows.Scan(&owner, &status, &total, &read, &cost); err != nil {
			return nil, fmt.Errorf("store: scan owner count: %w", err)
		}
		counts := out[owner]
		counts.addTo(status, int(total))
		counts.Read += int(read)
		counts.Total += int(total)
		counts.CostMinor += cost
		out[owner] = counts
	}
	return out, rows.Err()
}

// OperatorMessageFilter is the console's message search. The time window is
// required — it is what bounds a cross-tenant read to the partitions it needs.
type OperatorMessageFilter struct {
	TenantID   *uuid.UUID
	CampaignID *uuid.UUID
	JourneyID  *uuid.UUID
	// Channel matches the channel that carried it as well as the one asked
	// for, so an RCS campaign's SMS fallbacks show up under SMS.
	Channel *string
	// Statuses is nil for any, and a set of internal states otherwise.
	Statuses []string
	// ReadOnly narrows to messages with a read receipt.
	ReadOnly bool
	// Recipient matches the phone number or email exactly, or any number
	// ending in it — operators are usually handed the last digits.
	Recipient *string
	// Source is "campaign", "journey" or "api".
	Source *string
	From   time.Time
	To     time.Time
	Page   int
	Limit  int
}

func (f OperatorMessageFilter) where() (string, []any) {
	where := "created_at >= ? AND created_at < ?"
	args := []any{f.From, f.To}
	if f.TenantID != nil {
		where += " AND tenant_id = ?"
		args = append(args, *f.TenantID)
	}
	if f.CampaignID != nil {
		where += " AND campaign_id = ?"
		args = append(args, *f.CampaignID)
	}
	if f.JourneyID != nil {
		where += " AND journey_id = ?"
		args = append(args, *f.JourneyID)
	}
	if f.Channel != nil {
		where += " AND (channel = ? OR delivered_channel = ?)"
		args = append(args, *f.Channel, *f.Channel)
	}
	if f.Statuses != nil {
		where += " AND status IN (?)"
		args = append(args, f.Statuses)
	}
	if f.ReadOnly {
		where += " AND read_at IS NOT NULL"
	}
	if f.Recipient != nil {
		where += " AND (msisdn = ? OR email = ? OR endsWith(msisdn, ?))"
		args = append(args, *f.Recipient, *f.Recipient, *f.Recipient)
	}
	if f.Source != nil {
		switch *f.Source {
		case "campaign":
			where += " AND campaign_id IS NOT NULL"
		case "journey":
			where += " AND journey_id IS NOT NULL"
		case "api":
			where += " AND campaign_id IS NULL AND journey_id IS NULL"
		}
	}
	return where, args
}

// operatorMessageColumns is every column the console shows, in scan order.
const operatorMessageColumns = `
	tenant_id, id, campaign_id, campaign_name, journey_id, journey_name, channel,
	` + deliveredChannelColumn + `, country, sender_header, template_id, msisdn, email,
	status, error_code, error_class, segments, cost_minor, currency, carrier, route_id,
	carrier_ref, sent_by_kind, sent_by_id, created_at, sent_at, delivered_at, read_at,
	updated_at`

func scanOperatorMessage(scan func(...any) error) (MessageRecord, error) {
	var r MessageRecord
	err := scan(&r.TenantID, &r.ID, &r.CampaignID, &r.CampaignName, &r.JourneyID,
		&r.JourneyName, &r.Channel, &r.DeliveredChannel, &r.Country, &r.SenderHeader,
		&r.TemplateID, &r.Msisdn, &r.Email, &r.Status, &r.ErrorCode, &r.ErrorClass,
		&r.Segments, &r.CostMinor, &r.Currency, &r.Carrier, &r.RouteID, &r.CarrierRef,
		&r.SentByKind, &r.SentByID, &r.CreatedAt, &r.SentAt, &r.DeliveredAt, &r.ReadAt,
		&r.UpdatedAt)
	return r, err
}

func ListOperatorMessages(ctx context.Context, conn driver.Conn,
	filter OperatorMessageFilter) ([]MessageRecord, uint64, error) {

	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where, args := filter.where()
	var total uint64
	if err := conn.QueryRow(ctx, `SELECT count() FROM messages FINAL WHERE `+where,
		args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: count operator messages: %w", err)
	}
	out, err := queryOperatorMessages(ctx, conn, where+`
		ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, limit, pageOffset(filter.Page, limit)))
	return out, total, err
}

// EachOperatorMessage walks every message the filter matches, newest first,
// up to max rows, for the CSV export.
func EachOperatorMessage(ctx context.Context, conn driver.Conn,
	filter OperatorMessageFilter, max int, visit func(MessageRecord) error) error {

	where, args := filter.where()
	rows, err := conn.Query(ctx, `SELECT `+operatorMessageColumns+`
		FROM messages FINAL WHERE `+where+`
		ORDER BY created_at DESC, id DESC LIMIT ?`, append(args, max)...)
	if err != nil {
		return fmt.Errorf("store: export operator messages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		record, err := scanOperatorMessage(rows.Scan)
		if err != nil {
			return fmt.Errorf("store: scan exported message: %w", err)
		}
		if err := visit(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func queryOperatorMessages(ctx context.Context, conn driver.Conn, where string,
	args []any) ([]MessageRecord, error) {

	rows, err := conn.Query(ctx, `SELECT `+operatorMessageColumns+`
		FROM messages FINAL WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list operator messages: %w", err)
	}
	defer rows.Close()
	var out []MessageRecord
	for rows.Next() {
		record, err := scanOperatorMessage(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("store: scan operator message: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// GetOperatorMessage reads one message of any tenant.
func GetOperatorMessage(ctx context.Context, conn driver.Conn,
	messageID uuid.UUID) (MessageRecord, error) {

	tenantID, err := FindMessageTenant(ctx, conn, messageID)
	if err != nil {
		return MessageRecord{}, err
	}
	out, err := queryOperatorMessages(ctx, conn, "tenant_id = ? AND id = ?",
		[]any{tenantID, messageID})
	if err != nil {
		return MessageRecord{}, err
	}
	if len(out) == 0 {
		return MessageRecord{}, ErrNotFound
	}
	return out[0], nil
}

// MessageEvents is a message's transition log, oldest first. It ages out
// after 30 days (001_messages.sql), sooner than the message itself.
func MessageEvents(ctx context.Context, conn driver.Conn, tenantID,
	messageID uuid.UUID) ([]MessageEvent, error) {

	rows, err := conn.Query(ctx, `
		SELECT from_state, to_state, error_code, detail, occurred_at
		FROM message_events WHERE tenant_id = ? AND message_id = ?
		ORDER BY occurred_at, to_state`, tenantID, messageID)
	if err != nil {
		return nil, fmt.Errorf("store: message events: %w", err)
	}
	defer rows.Close()
	var out []MessageEvent
	for rows.Next() {
		event := MessageEvent{TenantID: tenantID, MessageID: messageID}
		if err := rows.Scan(&event.FromState, &event.ToState, &event.ErrorCode,
			&event.Detail, &event.OccurredAt); err != nil {
			return nil, fmt.Errorf("store: scan message event: %w", err)
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

// MessageTally is one group of the summary: a tenant, a channel and a status.
// Channel is the one that carried the message, or for a message that never
// left — a refusal — the one it was sent on, so no group is ever blank.
type MessageTally struct {
	TenantID  uuid.UUID
	Channel   string
	Status    string
	Messages  int
	Read      int
	Segments  int
	CostMinor int64
	Currency  string
}

// TallyOperatorMessages groups the filtered messages by tenant, the channel
// that carried them, status and currency.
//
// From the message rows rather than the hourly rollup: the rollup gets a row
// per TRANSITION, so a message that went queued, sent, delivered is in it three
// times. Summing it gives a number three times the truth.
func TallyOperatorMessages(ctx context.Context, conn driver.Conn,
	filter OperatorMessageFilter) ([]MessageTally, error) {

	where, args := filter.where()
	rows, err := conn.Query(ctx, `
		SELECT tenant_id, coalesce(delivered_channel, channel) AS carried, status, count(),
		       countIf(read_at IS NOT NULL), sum(segments), sum(cost_minor), currency
		FROM messages FINAL WHERE `+where+`
		GROUP BY tenant_id, carried, status, currency`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: tally operator messages: %w", err)
	}
	defer rows.Close()
	var out []MessageTally
	for rows.Next() {
		var tally MessageTally
		var channel *string
		var messages, read, segments uint64
		if err := rows.Scan(&tally.TenantID, &channel, &tally.Status, &messages, &read,
			&segments, &tally.CostMinor, &tally.Currency); err != nil {
			return nil, fmt.Errorf("store: scan tally: %w", err)
		}
		if channel != nil {
			tally.Channel = *channel
		}
		tally.Messages, tally.Read, tally.Segments = int(messages), int(read), int(segments)
		out = append(out, tally)
	}
	return out, rows.Err()
}

// SenderLabel is who a message's sent_by names, resolved for display.
type SenderLabel struct {
	Name  string
	Email string
	// KeyPrefix is the visible start of an API key, e.g. "sk_live_ab12".
	KeyPrefix string
}

// SenderLabels resolves users and API keys by id, in two queries for a page.
// Names are resolved when read rather than stored per message; a removed
// user or a deleted key resolves to nothing and the id is still shown.
func SenderLabels(ctx context.Context, pool *pgxpool.Pool,
	users, keys []uuid.UUID) (map[uuid.UUID]SenderLabel, error) {

	out := map[uuid.UUID]SenderLabel{}
	if len(users) > 0 {
		rows, err := pool.Query(ctx,
			`SELECT id, name, email::text FROM users WHERE id = ANY($1)`, users)
		if err != nil {
			return nil, fmt.Errorf("store: sender users: %w", err)
		}
		for rows.Next() {
			var id uuid.UUID
			var label SenderLabel
			if err := rows.Scan(&id, &label.Name, &label.Email); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = label
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if len(keys) > 0 {
		rows, err := pool.Query(ctx,
			`SELECT id, name, key_prefix FROM api_keys WHERE id = ANY($1)`, keys)
		if err != nil {
			return nil, fmt.Errorf("store: sender keys: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			var label SenderLabel
			if err := rows.Scan(&id, &label.Name, &label.KeyPrefix); err != nil {
				return nil, err
			}
			out[id] = label
		}
		return out, rows.Err()
	}
	return out, nil
}

// SendOwners resolves the campaigns and journeys behind a page of messages:
// the name, and who is answerable — a campaign's creator, and whoever last
// switched a journey on (its creator, if nobody is recorded as having done so).
func SendOwners(ctx context.Context, pool *pgxpool.Pool,
	campaigns, journeys []uuid.UUID) (map[uuid.UUID]SendOwner, error) {

	out := map[uuid.UUID]SendOwner{}
	if len(campaigns) == 0 && len(journeys) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT id, name, created_by_user_id, created_by_name, created_by_email
		FROM campaigns WHERE id = ANY($1)
		UNION ALL
		SELECT id, name, coalesce(activated_by_user_id, created_by_user_id),
		       coalesce(activated_by_name, created_by_name),
		       coalesce(activated_by_email, created_by_email)
		FROM journeys WHERE id = ANY($2)`, campaigns, journeys)
	if err != nil {
		return nil, fmt.Errorf("store: send authors: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var owner SendOwner
		if err := rows.Scan(&id, &owner.Name, &owner.Author.UserID, &owner.Author.Name,
			&owner.Author.Email); err != nil {
			return nil, err
		}
		out[id] = owner
	}
	return out, rows.Err()
}
