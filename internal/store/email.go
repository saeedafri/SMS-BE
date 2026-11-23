package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EmailIdentity derives an email sender's domain state from its records, and
// never the other way: every required record verified is verified, any failed
// is failed, otherwise pending. DMARC is never required, so it never blocks.
// A sender with no records has not started.
func (s SenderID) EmailIdentity() (status, dkim, mailFrom string) {
	if len(s.DNSRecords) == 0 {
		return "not_started", "not_started", "not_started"
	}
	fold := func(keep func(SenderDNSRecord) bool) string {
		state, any := "verified", false
		for _, record := range s.DNSRecords {
			if !keep(record) {
				continue
			}
			any = true
			switch record.Status {
			case "failed":
				return "failed"
			case "pending":
				state = "pending"
			}
		}
		if !any {
			return "not_started"
		}
		return state
	}
	dkim = fold(func(r SenderDNSRecord) bool { return r.Type == "DKIM" })
	mailFrom = fold(func(r SenderDNSRecord) bool {
		return r.Type == "MAIL_FROM_MX" || r.Type == "MAIL_FROM_SPF"
	})
	status = fold(func(r SenderDNSRecord) bool { return r.Required })
	return status, dkim, mailFrom
}

// SaveEmailCheck records what a DNS check found: each record type's status,
// and when every record and the sender were last checked. It never creates a
// record.
func SaveEmailCheck(ctx context.Context, pool *pgxpool.Pool, id Identity, senderID uuid.UUID,
	statuses map[string]string, checkedAt time.Time) error {

	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		for recordType, status := range statuses {
			if _, err := tx.Exec(ctx, `UPDATE sender_dns_records SET status = $3
				WHERE sender_id = $1 AND record_type = $2`, senderID, recordType, status); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE sender_dns_records SET last_checked_at = $2
			WHERE sender_id = $1`, senderID, checkedAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE sender_ids SET email_checked_at = $2 WHERE id = $1`,
			senderID, checkedAt)
		return err
	})
}

// FirstEmailSender is a tenant's oldest email sender, for the health and
// operator reads, or ErrNotFound when it has none.
func FirstEmailSender(ctx context.Context, pool *pgxpool.Pool, id Identity) (SenderID, error) {
	var sender SenderID
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		sender, err = scanSender(tx.QueryRow(ctx, `SELECT `+senderColumns+` FROM sender_ids
			WHERE channel = 'EMAIL' ORDER BY created_at LIMIT 1`))
		if err != nil {
			return err
		}
		senders := []SenderID{sender}
		if err := LoadSenderDNSRecords(ctx, tx, senders); err != nil {
			return err
		}
		sender = senders[0]
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SenderID{}, ErrNotFound
	}
	if err != nil {
		return SenderID{}, fmt.Errorf("store: first email sender: %w", err)
	}
	return sender, nil
}

// EmailSendingStatus is a tenant's email standing.
func EmailSendingStatus(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID) (string, error) {
	var status string
	err := WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT email_sending_status FROM tenants WHERE id = $1`,
			tenantID).Scan(&status)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: email sending status: %w", err)
	}
	return status, nil
}

// SetEmailSendingStatus moves a tenant's standing from one of these states to
// another, or reports ErrConflict when it is in none of them.
func SetEmailSendingStatus(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID,
	from []string, to string) error {

	tag, err := pool.Exec(ctx, `UPDATE tenants SET email_sending_status = $3
		WHERE id = $1 AND email_sending_status = ANY($2)`, tenantID, from, to)
	if err != nil {
		return fmt.Errorf("store: set email sending status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// UnsubscribeTokens returns each address's unsubscribe token, minting one
// for an address that has none. One token per tenant and address.
func UnsubscribeTokens(ctx context.Context, pool *pgxpool.Pool, id Identity, emails []string,
	life time.Duration) (map[string]string, error) {

	out := map[string]string{}
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT email, token FROM email_unsubscribe_tokens
			WHERE email = ANY($1) AND expires_at > now()`, emails)
		if err != nil {
			return err
		}
		for rows.Next() {
			var email, token string
			if err := rows.Scan(&email, &token); err != nil {
				rows.Close()
				return err
			}
			out[email] = token
		}
		rows.Close()
		for _, email := range emails {
			if _, have := out[email]; have || email == "" {
				continue
			}
			raw := make([]byte, 24)
			if _, err := rand.Read(raw); err != nil {
				return err
			}
			token := hex.EncodeToString(raw)
			if _, err := tx.Exec(ctx, `INSERT INTO email_unsubscribe_tokens
				(token, tenant_id, email, expires_at) VALUES ($1, $2, $3, now() + $4::interval)`,
				token, id.TenantID, email, fmt.Sprintf("%d seconds", int(life.Seconds()))); err != nil {
				return err
			}
			out[email] = token
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: unsubscribe tokens: %w", err)
	}
	return out, nil
}

// UnsubscribeTarget is what an unexpired token names.
type UnsubscribeTarget struct {
	TenantID   uuid.UUID
	TenantName string
	Email      string
}

// FindUnsubscribeToken reads a token with no session, through the operator
// pool. Unknown and expired are the same answer: ErrNotFound.
func FindUnsubscribeToken(ctx context.Context, operator *pgxpool.Pool, token string) (UnsubscribeTarget, error) {
	var target UnsubscribeTarget
	err := operator.QueryRow(ctx, `
		SELECT t.tenant_id, n.name, t.email FROM email_unsubscribe_tokens t
		JOIN tenants n ON n.id = t.tenant_id
		WHERE t.token = $1 AND t.expires_at > now()`, token).
		Scan(&target.TenantID, &target.TenantName, &target.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return UnsubscribeTarget{}, ErrNotFound
	}
	if err != nil {
		return UnsubscribeTarget{}, fmt.Errorf("store: unsubscribe token: %w", err)
	}
	return target, nil
}

// EmailOptedOut says whether every contact holding this address has opted
// out of email, and whether any contact holds it at all.
func EmailOptedOut(ctx context.Context, pool *pgxpool.Pool, id Identity, email string) (bool, error) {
	var outstanding int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM contacts
			WHERE lower(email) = lower($1) AND coalesce(consent ->> 'EMAIL', 'unknown') <> 'opted_out'`,
			email).Scan(&outstanding)
	})
	if err != nil {
		return false, fmt.Errorf("store: email opted out: %w", err)
	}
	return outstanding == 0, nil
}

// OptOutEmail sets EMAIL consent to opted out for every contact with this
// address, and says how many changed.
func OptOutEmail(ctx context.Context, pool *pgxpool.Pool, id Identity, email string) (int, error) {
	var changed int64
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE contacts
			SET consent = consent || '{"EMAIL":"opted_out"}'::jsonb, updated_at = now()
			WHERE lower(email) = lower($1) AND coalesce(consent ->> 'EMAIL', 'unknown') <> 'opted_out'`,
			email)
		changed = tag.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("store: opt out email: %w", err)
	}
	return int(changed), nil
}

// ClaimEmailEvent records a provider event as applied, and says false when it
// already was: a redelivered event changes nothing.
func ClaimEmailEvent(ctx context.Context, pool *pgxpool.Pool, id Identity, eventID string) (bool, error) {
	var claimed bool
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO email_event_receipts (event_id, tenant_id)
			VALUES ($1, $2) ON CONFLICT (event_id) DO NOTHING`, eventID, id.TenantID)
		claimed = tag.RowsAffected() == 1
		return err
	})
	if err != nil {
		return false, fmt.Errorf("store: claim email event: %w", err)
	}
	return claimed, nil
}

// EmailEventRecord is one provider event on one message.
type EmailEventRecord struct {
	TenantID, MessageID uuid.UUID
	EventID, Type       string
	At                  time.Time
	Detail              *string
	BounceType          *string
	BounceSubType       *string
	BotLikely           bool
}

func InsertEmailEvents(ctx context.Context, conn driver.Conn, events []EmailEventRecord) error {
	if len(events) == 0 {
		return nil
	}
	batch, err := conn.PrepareBatch(ctx, `INSERT INTO email_events (tenant_id, message_id,
		event_id, type, at, detail, bounce_type, bounce_sub_type, bot_likely)`)
	if err != nil {
		return fmt.Errorf("store: prepare email events: %w", err)
	}
	for _, e := range events {
		if err := batch.Append(e.TenantID, e.MessageID, e.EventID, e.Type, e.At, e.Detail,
			e.BounceType, e.BounceSubType, e.BotLikely); err != nil {
			return fmt.Errorf("store: append email event: %w", err)
		}
	}
	return batch.Send()
}

// EmailEventsFor reads the provider's events on these messages, in order.
func EmailEventsFor(ctx context.Context, conn driver.Conn, tenantID uuid.UUID,
	messageIDs []uuid.UUID) (map[uuid.UUID][]EmailEventRecord, error) {

	out := map[uuid.UUID][]EmailEventRecord{}
	if len(messageIDs) == 0 {
		return out, nil
	}
	rows, err := conn.Query(ctx, `SELECT message_id, event_id, type, at, detail, bounce_type,
		       bounce_sub_type, bot_likely
		FROM email_events FINAL WHERE tenant_id = ? AND message_id IN (?)
		ORDER BY at, event_id`, tenantID, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("store: email events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e EmailEventRecord
		if err := rows.Scan(&e.MessageID, &e.EventID, &e.Type, &e.At, &e.Detail, &e.BounceType,
			&e.BounceSubType, &e.BotLikely); err != nil {
			return nil, fmt.Errorf("store: email events: %w", err)
		}
		e.TenantID = tenantID
		out[e.MessageID] = append(out[e.MessageID], e)
	}
	return out, rows.Err()
}

// EmailHealthCounts are a tenant's deliveries, counted bounces and complaints
// since a time. A bounce the provider never attempted (the address was
// already suppressed) is in neither count.
type EmailHealthCounts struct {
	Deliveries, Bounces, Complaints uint64
}

func QueryEmailHealth(ctx context.Context, conn driver.Conn, tenantID uuid.UUID,
	since time.Time, suppressedSubTypes []string) (EmailHealthCounts, error) {

	var counts EmailHealthCounts
	err := conn.QueryRow(ctx, `
		SELECT countIf(type = 'delivery'),
		       countIf(type = 'bounce' AND bounce_type = 'permanent'
		               AND ifNull(bounce_sub_type, '') NOT IN (?)),
		       countIf(type = 'complaint')
		FROM email_events FINAL WHERE tenant_id = ? AND at >= ?`,
		suppressedSubTypes, tenantID, since).Scan(&counts.Deliveries, &counts.Bounces, &counts.Complaints)
	if err != nil {
		return EmailHealthCounts{}, fmt.Errorf("store: email health: %w", err)
	}
	return counts, nil
}
