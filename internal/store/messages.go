package store

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
)

// MessageRecord is one message as held in ClickHouse.
type MessageRecord struct {
	TenantID     uuid.UUID
	ID           uuid.UUID
	CampaignID   *uuid.UUID
	CampaignName *string
	Channel      string
	Country      string
	SenderHeader string
	TemplateID   *uuid.UUID
	Msisdn       string
	Email        *string
	Status       string
	ErrorCode    *string
	ErrorClass   *string
	FraudFlag    string
	Segments     uint8
	CostMinor    int64
	Currency     string
	RouteID      *string
	CarrierRef   *string
	CreatedAt    time.Time
	SentAt       *time.Time
	DeliveredAt  *time.Time
	UpdatedAt    time.Time
	Version      uint64
}

// InsertMessages writes a batch. ClickHouse is built for batched inserts and
// punished by row-at-a-time ones, so the send path always accumulates a batch
// before calling this — never one insert per message.
func InsertMessages(ctx context.Context, conn driver.Conn, records []MessageRecord) error {
	if len(records) == 0 {
		return nil
	}
	batch, err := conn.PrepareBatch(ctx, `INSERT INTO messages`)
	if err != nil {
		return fmt.Errorf("store: prepare message batch: %w", err)
	}
	for _, record := range records {
		if err := batch.Append(
			record.TenantID, record.ID, record.CampaignID, record.CampaignName,
			nil, nil, nil, // journey id/name, conversation id — Stage 11
			record.Channel, record.Country, record.SenderHeader, record.TemplateID,
			record.Msisdn, record.Email, record.Status, nil,
			record.ErrorCode, record.ErrorClass, record.FraudFlag,
			record.Segments, record.CostMinor, record.Currency,
			record.RouteID, record.CarrierRef,
			record.CreatedAt, record.SentAt, record.DeliveredAt,
			record.UpdatedAt, record.Version,
		); err != nil {
			return fmt.Errorf("store: append message: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("store: send message batch: %w", err)
	}
	return nil
}

// MessageEvent is one entry in the append-only transition log.
type MessageEvent struct {
	TenantID   uuid.UUID
	MessageID  uuid.UUID
	FromState  string
	ToState    string
	ErrorCode  *string
	Detail     string
	OccurredAt time.Time
}

func InsertMessageEvents(ctx context.Context, conn driver.Conn, events []MessageEvent) error {
	if len(events) == 0 {
		return nil
	}
	batch, err := conn.PrepareBatch(ctx, `INSERT INTO message_events`)
	if err != nil {
		return fmt.Errorf("store: prepare event batch: %w", err)
	}
	for _, event := range events {
		if err := batch.Append(event.TenantID, event.MessageID, event.FromState,
			event.ToState, event.ErrorCode, event.Detail, event.OccurredAt); err != nil {
			return fmt.Errorf("store: append event: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("store: send event batch: %w", err)
	}
	return nil
}

// MessageFilter is the logs explorer's query.
type MessageFilter struct {
	Status     string
	Channel    string
	ErrorClass string
	CampaignID *uuid.UUID
	Cursor     string
	Limit      int
}

// QueryMessages reads the logs explorer page.
//
// FINAL is required because ReplacingMergeTree merges asynchronously: without
// it a message that changed state twice can appear twice, once per version.
// FINAL costs read performance, which is the accepted trade for never showing
// a user two copies of their own message.
func QueryMessages(ctx context.Context, conn driver.Conn, tenantID uuid.UUID,
	filter MessageFilter) ([]MessageRecord, uint64, string, error) {

	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	cursorTime, cursorID, err := decodeLedgerCursor(filter.Cursor)
	if err != nil {
		return nil, 0, "", err
	}

	where := "tenant_id = ?"
	args := []any{tenantID}
	if filter.Status != "" {
		where += " AND status = ?"
		args = append(args, filter.Status)
	}
	if filter.Channel != "" {
		where += " AND channel = ?"
		args = append(args, filter.Channel)
	}
	if filter.ErrorClass != "" {
		where += " AND error_class = ?"
		args = append(args, filter.ErrorClass)
	}
	if filter.CampaignID != nil {
		where += " AND campaign_id = ?"
		args = append(args, *filter.CampaignID)
	}

	var total uint64
	if err := conn.QueryRow(ctx,
		`SELECT count() FROM messages FINAL WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, "", fmt.Errorf("store: count messages: %w", err)
	}

	pageWhere, pageArgs := where, append([]any{}, args...)
	if cursorTime != nil {
		pageWhere += " AND (created_at, id) < (?, ?)"
		pageArgs = append(pageArgs, *cursorTime, *cursorID)
	}
	pageArgs = append(pageArgs, limit+1)

	rows, err := conn.Query(ctx, `
		SELECT id, campaign_id, campaign_name, channel, msisdn, email, status,
		       error_code, error_class, fraud_flag, segments, created_at,
		       sent_at, delivered_at, updated_at
		FROM messages FINAL
		WHERE `+pageWhere+`
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, pageArgs...)
	if err != nil {
		return nil, 0, "", fmt.Errorf("store: query messages: %w", err)
	}
	defer rows.Close()

	var out []MessageRecord
	for rows.Next() {
		var record MessageRecord
		if err := rows.Scan(&record.ID, &record.CampaignID, &record.CampaignName,
			&record.Channel, &record.Msisdn, &record.Email, &record.Status,
			&record.ErrorCode, &record.ErrorClass, &record.FraudFlag, &record.Segments,
			&record.CreatedAt, &record.SentAt, &record.DeliveredAt,
			&record.UpdatedAt); err != nil {
			return nil, 0, "", fmt.Errorf("store: scan message: %w", err)
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, "", fmt.Errorf("store: iterate messages: %w", err)
	}

	next := ""
	if len(out) > limit {
		next = encodeLedgerCursor(out[limit-1].CreatedAt, out[limit-1].ID)
		out = out[:limit]
	}
	return out, total, next, nil
}

// LoadMessageState reads one message's current state, for applying a receipt.
func LoadMessageState(ctx context.Context, conn driver.Conn, tenantID, messageID uuid.UUID) (MessageRecord, error) {
	var record MessageRecord
	err := conn.QueryRow(ctx, `
		SELECT id, status, segments, cost_minor, currency, campaign_id, channel,
		       country, sender_header, msisdn, created_at, version
		FROM messages FINAL WHERE tenant_id = ? AND id = ?`,
		tenantID, messageID,
	).Scan(&record.ID, &record.Status, &record.Segments, &record.CostMinor,
		&record.Currency, &record.CampaignID, &record.Channel, &record.Country,
		&record.SenderHeader, &record.Msisdn, &record.CreatedAt, &record.Version)
	if err != nil {
		return MessageRecord{}, ErrNotFound
	}
	record.TenantID = tenantID
	return record, nil
}

// RollupRow is one hourly aggregate. Rollups are permanent while raw rows age
// out, so every analytics read comes from here.
type RollupRow struct {
	TenantID     uuid.UUID
	Hour         time.Time
	Channel      string
	Country      string
	Status       string
	MessageCount uint64
	SegmentCount uint64
	CostMinor    int64
	Currency     string
}

func InsertRollups(ctx context.Context, conn driver.Conn, rows []RollupRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := conn.PrepareBatch(ctx, `INSERT INTO message_rollup_hourly`)
	if err != nil {
		return fmt.Errorf("store: prepare rollup batch: %w", err)
	}
	for _, row := range rows {
		if err := batch.Append(row.TenantID, row.Hour, row.Channel, row.Country,
			row.Status, row.MessageCount, row.SegmentCount, row.CostMinor,
			row.Currency); err != nil {
			return fmt.Errorf("store: append rollup: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("store: send rollup batch: %w", err)
	}
	return nil
}

// FindStaleMessages returns messages that a carrier accepted but never reported
// on, older than the validity window. It deliberately runs unscoped across every
// tenant: the reconciler is a system process, not a request, and a tenant whose
// carrier went silent is precisely the tenant who cannot ask for their money
// back themselves.
func FindStaleMessages(ctx context.Context, conn driver.Conn,
	olderThan time.Time, limit int) ([]MessageRecord, error) {

	rows, err := conn.Query(ctx, `
		SELECT tenant_id, id, status, segments, cost_minor, currency, campaign_id,
		       channel, country, sender_header, msisdn, created_at, version
		FROM messages FINAL
		WHERE status IN ('submitted', 'accepted') AND updated_at < ?
		ORDER BY updated_at ASC
		LIMIT ?`, olderThan, limit)
	if err != nil {
		return nil, fmt.Errorf("store: find stale messages: %w", err)
	}
	defer rows.Close()

	var out []MessageRecord
	for rows.Next() {
		var record MessageRecord
		if err := rows.Scan(&record.TenantID, &record.ID, &record.Status,
			&record.Segments, &record.CostMinor, &record.Currency, &record.CampaignID,
			&record.Channel, &record.Country, &record.SenderHeader, &record.Msisdn,
			&record.CreatedAt, &record.Version); err != nil {
			return nil, fmt.Errorf("store: scan stale message: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// CampaignCounts is the delivery breakdown shown on a campaign's detail page.
type CampaignCounts struct {
	Queued    int
	Sent      int
	Delivered int
	Failed    int
	Read      int
}

// CountCampaignMessages groups a campaign's messages by contract status.
//
// It reads the raw messages table rather than the hourly rollup because the
// rollup is keyed by the hour a transition happened: a message that went
// queued then delivered contributes a row to both, so summing the rollup would
// count it twice. FINAL on the message rows gives one row per message.
func CountCampaignMessages(ctx context.Context, conn driver.Conn, tenantID,
	campaignID uuid.UUID) (CampaignCounts, error) {

	rows, err := conn.Query(ctx, `
		SELECT status, count() FROM messages FINAL
		WHERE tenant_id = ? AND campaign_id = ?
		GROUP BY status`, tenantID, campaignID)
	if err != nil {
		return CampaignCounts{}, fmt.Errorf("store: count campaign messages: %w", err)
	}
	defer rows.Close()

	var counts CampaignCounts
	for rows.Next() {
		var status string
		var total uint64
		if err := rows.Scan(&status, &total); err != nil {
			return CampaignCounts{}, fmt.Errorf("store: scan campaign count: %w", err)
		}
		// Mapped through the same collapse the logs use, so the campaign page
		// and the message list can never disagree about one message.
		switch status {
		case "queued", "submitting":
			counts.Queued += int(total)
		case "submitted", "accepted":
			counts.Sent += int(total)
		case "delivered":
			counts.Delivered += int(total)
		case "undelivered", "rejected", "expired":
			counts.Failed += int(total)
		}
	}
	return counts, rows.Err()
}
