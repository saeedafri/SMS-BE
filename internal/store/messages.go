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

	// Carrier is the CarrierId the message went out on, from the contract's
	// enum. Distinct from CarrierRef, which is the carrier's own reference for
	// this one message. Empty until route selection records which carrier
	// carried it; the deliverability report excludes empty rather than guessing.
	Carrier     string
	CreatedAt   time.Time
	SentAt      *time.Time
	DeliveredAt *time.Time
	UpdatedAt   time.Time
	Version     uint64
}

// InsertMessages writes a batch. ClickHouse is built for batched inserts and
// punished by row-at-a-time ones, so the send path always accumulates a batch
// before calling this — never one insert per message.
func InsertMessages(ctx context.Context, conn driver.Conn, records []MessageRecord) error {
	if len(records) == 0 {
		return nil
	}
	// Columns named explicitly. An unqualified INSERT binds to the table's
	// column order, so adding any column later silently turns every send into
	// an "expected N arguments, got N-1" failure — which is exactly what
	// happened when the carrier column was added.
	batch, err := conn.PrepareBatch(ctx, `INSERT INTO messages (
		tenant_id, id, campaign_id, campaign_name, journey_id, journey_name,
		conversation_id, channel, country, sender_header, template_id, msisdn,
		email, status, delivered_channel, error_code, error_class, fraud_flag,
		segments, cost_minor, currency, route_id, carrier_ref, carrier,
		created_at, sent_at, delivered_at, updated_at, version)`)
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
			record.RouteID, record.CarrierRef, record.Carrier,
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
		       error_code, error_class, fraud_flag, segments, cost_minor, currency,
		       created_at, sent_at, delivered_at, updated_at
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
			&record.CostMinor, &record.Currency,
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

// LoadMessageState reads the fields a delivery report needs in order to write
// the next version of a row.
//
// Every column read here is a column the settle path re-writes. The table is a
// ReplacingMergeTree, so the new version REPLACES the old one wholesale — a
// field that is loaded but not carried forward is not merely stale, it is
// erased. `email` was missing from this list, so the moment a delivery report
// landed, an Email message lost the address it was sent to and the campaign
// showed the contact's phone number instead.
//
// Anything added to the insert belongs here too.
// LoadMessageState reads one message's current state, for applying a receipt.
func LoadMessageState(ctx context.Context, conn driver.Conn, tenantID, messageID uuid.UUID) (MessageRecord, error) {
	var record MessageRecord
	// Every column a later version has to carry forward.
	//
	// A settled message REPLACES its previous row, so whatever this does not
	// read is erased the moment a delivery report lands. That is how carrier,
	// route_id and carrier_ref used to vanish from every delivered message:
	// the deliverability-by-carrier report only ever saw messages that never
	// settled, and — worse — a second webhook for the same message could no
	// longer find it, because carrier_ref is the only key an Airtel callback
	// carries.
	err := conn.QueryRow(ctx, `
		SELECT id, status, segments, cost_minor, currency, campaign_id,
		       campaign_name, channel, country, sender_header, template_id,
		       msisdn, email, route_id, carrier_ref, carrier,
		       created_at, sent_at, version
		FROM messages FINAL WHERE tenant_id = ? AND id = ?`,
		tenantID, messageID,
	).Scan(&record.ID, &record.Status, &record.Segments, &record.CostMinor,
		&record.Currency, &record.CampaignID, &record.CampaignName,
		&record.Channel, &record.Country, &record.SenderHeader, &record.TemplateID,
		&record.Msisdn, &record.Email, &record.RouteID, &record.CarrierRef,
		&record.Carrier, &record.CreatedAt, &record.SentAt, &record.Version)
	if err != nil {
		return MessageRecord{}, ErrNotFound
	}
	record.TenantID = tenantID
	return record, nil
}

// GetMessage reads one message for display.
//
// Deliberately NOT LoadMessageState. That function feeds the settle path, where
// its column list is a safety property — a column it fails to read is erased
// when a delivery report replaces the row — so coupling a display read to it
// would mean every field the UI wants becomes a field settlement has to carry.
// This one reads what MessageLogEntry needs and nothing has to carry it
// anywhere.
func GetMessage(ctx context.Context, conn driver.Conn, tenantID, messageID uuid.UUID) (
	MessageRecord, error) {

	var record MessageRecord
	err := conn.QueryRow(ctx, `
		SELECT id, campaign_id, campaign_name, channel, country, sender_header,
		       msisdn, email, status, error_code, error_class, fraud_flag,
		       segments, cost_minor, currency, carrier, carrier_ref,
		       created_at, sent_at, delivered_at, updated_at
		FROM messages FINAL WHERE tenant_id = ? AND id = ?`,
		tenantID, messageID,
	).Scan(&record.ID, &record.CampaignID, &record.CampaignName, &record.Channel,
		&record.Country, &record.SenderHeader, &record.Msisdn, &record.Email,
		&record.Status, &record.ErrorCode, &record.ErrorClass, &record.FraudFlag,
		&record.Segments, &record.CostMinor, &record.Currency, &record.Carrier,
		&record.CarrierRef, &record.CreatedAt, &record.SentAt, &record.DeliveredAt,
		&record.UpdatedAt)
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

// FindMessageTenant resolves which tenant owns a message.
//
// Delivery reports arrive carrying a carrier reference and our message id, but
// no tenant — the carrier has no concept of one. Every settlement path needs
// the tenant before it can scope anything, so this lookup deliberately runs
// unscoped, and it returns ONLY the tenant id so a caller cannot use it to read
// another tenant's message content.
func FindMessageTenant(ctx context.Context, conn driver.Conn, messageID uuid.UUID) (uuid.UUID, error) {
	var tenantID uuid.UUID
	err := conn.QueryRow(ctx,
		`SELECT tenant_id FROM messages WHERE id = ? LIMIT 1`, messageID).Scan(&tenantID)
	if err != nil {
		return uuid.Nil, ErrNotFound
	}
	return tenantID, nil
}

// FindMessageByCarrierRef resolves a message from the carrier's OWN id.
//
// This exists because Airtel's webhooks carry no field we control. Their
// delivery events quote the messageRequestId they issued at submit time and
// nothing else, so the only way back to a Relay message — and therefore to the
// tenant whose wallet is holding money against it — is the carrier reference we
// stored alongside it.
//
// Vi needs none of this: it lets the sender supply the message id, so Relay
// sends its own uuid and gets it back. Both paths end in the same settlement
// code; only the lookup differs.
//
// An empty carrier reference matches nothing on purpose. Every message that has
// not reached a carrier has a null one, and a blank lookup would otherwise pick
// an arbitrary unsent message and settle it.
func FindMessageByCarrierRef(ctx context.Context, conn driver.Conn,
	carrierRef string) (tenantID uuid.UUID, messageID uuid.UUID, err error) {

	if carrierRef == "" {
		return uuid.Nil, uuid.Nil, ErrNotFound
	}
	// ReplacingMergeTree keeps every version until a merge runs, so an
	// un-merged message has several rows and an unqualified read could return
	// the pre-submit one, whose carrier_ref is null. Ordering by version picks
	// the newest.
	err = conn.QueryRow(ctx, `
		SELECT tenant_id, id FROM messages
		 WHERE carrier_ref = ?
		 ORDER BY version DESC
		 LIMIT 1`, carrierRef).Scan(&tenantID, &messageID)
	if err != nil {
		return uuid.Nil, uuid.Nil, ErrNotFound
	}
	return tenantID, messageID, nil
}
