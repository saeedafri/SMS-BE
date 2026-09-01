package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SupportTicket is one customer support thread.
type SupportTicket struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	TenantName string
	Subject    string
	Category   string
	Status     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// SupportMessage is one post in a ticket thread.
type SupportMessage struct {
	ID         uuid.UUID
	Author     string
	AuthorName string
	Body       string
	CreatedAt  time.Time
}

// ListSupportTickets returns the tenant's tickets, narrowed by status and
// category when given. Nil means no filter.
// ListSupportTickets pages one tenant's tickets and reports how many match.
//
// The operator sibling has paged since it was written; this one had no way to
// ask for a second page, so the customer Support screen could only ever show
// the first slice of its own tickets.
func ListSupportTickets(ctx context.Context, pool *pgxpool.Pool, id Identity,
	status, category *string, cursor string, limit int) ([]SupportTicket, int, string, error) {

	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cursorTime, cursorID, err := decodeLedgerCursor(cursor)
	if err != nil {
		return nil, 0, "", err
	}

	var out []SupportTicket
	var total int
	err = WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM support_tickets t
			WHERE ($1::text IS NULL OR t.status   = $1)
			  AND ($2::text IS NULL OR t.category = $2)`,
			status, category).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT t.id, t.tenant_id, n.name, t.subject, t.category, t.status,
			       t.created_at, t.updated_at
			FROM support_tickets t JOIN tenants n ON n.id = t.tenant_id
			WHERE ($1::text IS NULL OR t.status   = $1)
			  AND ($2::text IS NULL OR t.category = $2)
			  AND ($3::timestamptz IS NULL OR (t.updated_at, t.id) < ($3, $4))
			ORDER BY t.updated_at DESC, t.id DESC
			LIMIT $5`, status, category, cursorTime, cursorID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ticket SupportTicket
			if err := rows.Scan(&ticket.ID, &ticket.TenantID, &ticket.TenantName,
				&ticket.Subject, &ticket.Category, &ticket.Status,
				&ticket.CreatedAt, &ticket.UpdatedAt); err != nil {
				return err
			}
			out = append(out, ticket)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, "", fmt.Errorf("store: list support tickets: %w", err)
	}

	next := ""
	if len(out) > limit {
		next = encodeLedgerCursor(out[limit-1].UpdatedAt, out[limit-1].ID)
		out = out[:limit]
	}
	return out, total, next, nil
}

func GetSupportTicket(ctx context.Context, pool *pgxpool.Pool, id Identity,
	ticketID uuid.UUID) (SupportTicket, []SupportMessage, error) {

	var ticket SupportTicket
	var messages []SupportMessage
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT t.id, t.tenant_id, n.name, t.subject, t.category, t.status,
			       t.created_at, t.updated_at
			FROM support_tickets t JOIN tenants n ON n.id = t.tenant_id
			WHERE t.id = $1`, ticketID,
		).Scan(&ticket.ID, &ticket.TenantID, &ticket.TenantName, &ticket.Subject,
			&ticket.Category, &ticket.Status, &ticket.CreatedAt, &ticket.UpdatedAt); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, author, author_name, body, created_at
			FROM support_messages WHERE ticket_id = $1 ORDER BY created_at, id`, ticketID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var message SupportMessage
			if err := rows.Scan(&message.ID, &message.Author, &message.AuthorName,
				&message.Body, &message.CreatedAt); err != nil {
				return err
			}
			messages = append(messages, message)
		}
		return rows.Err()
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SupportTicket{}, nil, ErrNotFound
	}
	if err != nil {
		return SupportTicket{}, nil, fmt.Errorf("store: get support ticket: %w", err)
	}
	return ticket, messages, nil
}

// CreateSupportTicket opens a ticket with its first message in one
// transaction: a ticket with no body is not a support request, it is an
// orphan row someone has to guess the meaning of.
func CreateSupportTicket(ctx context.Context, pool *pgxpool.Pool, id Identity,
	subject, category, body, authorName string) (SupportTicket, error) {

	var ticket SupportTicket
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO support_tickets (tenant_id, subject, category)
			VALUES ($1,$2,$3) RETURNING id, created_at, updated_at`,
			id.TenantID, subject, category,
		).Scan(&ticket.ID, &ticket.CreatedAt, &ticket.UpdatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO support_messages (tenant_id, ticket_id, author, author_name, body)
			VALUES ($1,$2,'customer',$3,$4)`,
			id.TenantID, ticket.ID, authorName, body); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT name FROM tenants WHERE id = $1`,
			id.TenantID).Scan(&ticket.TenantName)
	})
	if err != nil {
		return SupportTicket{}, fmt.Errorf("store: create support ticket: %w", err)
	}
	ticket.TenantID, ticket.Subject = id.TenantID, subject
	ticket.Category, ticket.Status = category, "open"
	return ticket, nil
}

// AddSupportMessage appends to a thread and bumps the ticket.
//
// A customer replying to a resolved ticket reopens it: otherwise their reply
// lands in a closed thread nobody is watching, which is the single most
// common way support systems lose people.
func AddSupportMessage(ctx context.Context, pool *pgxpool.Pool, id Identity,
	ticketID uuid.UUID, author, authorName, body string) (SupportMessage, error) {

	var message SupportMessage
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT true FROM support_tickets WHERE id = $1`, ticketID).Scan(&exists); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO support_messages (tenant_id, ticket_id, author, author_name, body)
			VALUES ($1,$2,$3,$4,$5)
			RETURNING id, author, author_name, body, created_at`,
			id.TenantID, ticketID, author, authorName, body,
		).Scan(&message.ID, &message.Author, &message.AuthorName,
			&message.Body, &message.CreatedAt); err != nil {
			return err
		}
		status := "pending"
		if author == "customer" {
			status = "open"
		}
		_, err := tx.Exec(ctx,
			`UPDATE support_tickets SET status = $2, updated_at = now() WHERE id = $1`,
			ticketID, status)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SupportMessage{}, ErrNotFound
	}
	if err != nil {
		return SupportMessage{}, fmt.Errorf("store: add support message: %w", err)
	}
	return message, nil
}

func SetSupportTicketStatus(ctx context.Context, pool *pgxpool.Pool, id Identity,
	ticketID uuid.UUID, status string) (SupportTicket, error) {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE support_tickets SET status = $2, updated_at = now() WHERE id = $1`,
			ticketID, status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return SupportTicket{}, err
	}
	ticket, _, err := GetSupportTicket(ctx, pool, id, ticketID)
	return ticket, err
}

// Conversation is a two-way thread with one contact.
type Conversation struct {
	ID                 uuid.UUID
	ContactID          uuid.UUID
	ContactName        string
	Country            string
	Identity           string
	Channel            string
	Status             string
	Unread             bool
	Suppressed         bool
	LastMessagePreview string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ConversationMessage is one message in a thread.
type ConversationMessage struct {
	ID             uuid.UUID
	ConversationID uuid.UUID
	Direction      string
	Body           string
	KeywordMatched *string
	Segments       *int
	Status         *string
	CreatedAt      time.Time
}

const conversationColumns = `
	c.id, c.contact_id, ct.msisdn,
	-- The contact's display name, falling back to their number.
	--
	-- This used to be the msisdn unconditionally, with a comment explaining it
	-- was a placeholder "until contacts carry names". Contacts have carried
	-- names since the audience work landed; the placeholder simply outlived the
	-- reason for it, so the inbox listed every thread as a phone number even
	-- though it knew the person was called Priya.
	COALESCE(NULLIF(ct.fields ->> 'firstName', ''), ct.msisdn),
	-- The contact's country, because a reply is priced by corridor: the same
	-- sentence costs a different amount to India than to the US.
	ct.country,
	c.channel, c.status, c.unread,
	EXISTS (SELECT 1 FROM suppressions s
	        WHERE s.tenant_id = c.tenant_id AND s.identity = ct.msisdn),
	COALESCE((SELECT m.body FROM conversation_messages m
	          WHERE m.conversation_id = c.id
	          ORDER BY m.created_at DESC, m.id DESC LIMIT 1), ''),
	c.created_at, c.updated_at`

func scanConversation(row pgx.Row) (Conversation, error) {
	var conversation Conversation
	err := row.Scan(&conversation.ID, &conversation.ContactID, &conversation.Identity,
		&conversation.ContactName, &conversation.Country,
		&conversation.Channel, &conversation.Status,
		&conversation.Unread, &conversation.Suppressed,
		&conversation.LastMessagePreview,
		&conversation.CreatedAt, &conversation.UpdatedAt)
	return conversation, err
}

// ConversationFilter narrows the inbox. Every field is optional and a nil one
// matches everything, so an absent query parameter behaves as "no filter".
//
// The inbox is the screen where filtering matters most: it is the only place an
// agent works through a queue, and a filter that silently does nothing means
// they answer the wrong messages first.
type ConversationFilter struct {
	Channel *string
	Status  *string
	Unread  *bool
	Cursor  string
	Limit   int
}

// ListConversations pages the inbox and reports how many threads match.
//
// limit and cursor were declared by the contract and read by nothing: asking
// for three threads returned every one. total was the length of that response,
// which looked correct only because nothing was ever cut.
func ListConversations(ctx context.Context, pool *pgxpool.Pool, id Identity,
	filter ConversationFilter) ([]Conversation, int, string, error) {

	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	cursorTime, cursorID, err := decodeLedgerCursor(filter.Cursor)
	if err != nil {
		return nil, 0, "", err
	}

	var out []Conversation
	var total int
	err = WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM conversations c
			WHERE ($1::text IS NULL OR c.channel = $1)
			  AND ($2::text IS NULL OR c.status  = $2)
			  AND ($3::bool IS NULL OR c.unread  = $3)`,
			filter.Channel, filter.Status, filter.Unread).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT `+conversationColumns+`
			FROM conversations c JOIN contacts ct ON ct.id = c.contact_id
			WHERE ($1::text IS NULL OR c.channel = $1)
			  AND ($2::text IS NULL OR c.status  = $2)
			  -- Only filters when the caller asked. unread=false is a real
			  -- request for read threads, not the same as omitting it.
			  AND ($3::bool IS NULL OR c.unread  = $3)
			  AND ($4::timestamptz IS NULL OR (c.updated_at, c.id) < ($4, $5))
			ORDER BY c.updated_at DESC, c.id DESC
			LIMIT $6`,
			filter.Channel, filter.Status, filter.Unread, cursorTime, cursorID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			conversation, err := scanConversation(rows)
			if err != nil {
				return err
			}
			out = append(out, conversation)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, "", fmt.Errorf("store: list conversations: %w", err)
	}

	next := ""
	if len(out) > limit {
		next = encodeLedgerCursor(out[limit-1].UpdatedAt, out[limit-1].ID)
		out = out[:limit]
	}
	return out, total, next, nil
}

func GetConversation(ctx context.Context, pool *pgxpool.Pool, id Identity,
	conversationID uuid.UUID) (Conversation, []ConversationMessage, error) {

	var conversation Conversation
	var messages []ConversationMessage
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		conversation, err = scanConversation(tx.QueryRow(ctx, `
			SELECT `+conversationColumns+`
			FROM conversations c JOIN contacts ct ON ct.id = c.contact_id
			WHERE c.id = $1`, conversationID))
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, conversation_id, direction, body, keyword_matched,
			       segments, status, created_at
			FROM conversation_messages WHERE conversation_id = $1
			ORDER BY created_at, id`, conversationID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var message ConversationMessage
			if err := rows.Scan(&message.ID, &message.ConversationID, &message.Direction,
				&message.Body, &message.KeywordMatched, &message.Segments,
				&message.Status, &message.CreatedAt); err != nil {
				return err
			}
			messages = append(messages, message)
		}
		return rows.Err()
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, nil, ErrNotFound
	}
	if err != nil {
		return Conversation{}, nil, fmt.Errorf("store: get conversation: %w", err)
	}
	return conversation, messages, nil
}

// AppendConversationMessage adds a message and bumps the thread. An inbound
// message marks the thread unread; an outbound one clears it, because sending
// a reply means the operator has read what came before.
func AppendConversationMessage(ctx context.Context, pool *pgxpool.Pool, id Identity,
	message ConversationMessage) (ConversationMessage, error) {

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO conversation_messages (tenant_id, conversation_id, direction,
			    body, keyword_matched, segments, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			RETURNING id, conversation_id, direction, body, keyword_matched,
			          segments, status, created_at`,
			id.TenantID, message.ConversationID, message.Direction, message.Body,
			message.KeywordMatched, message.Segments, message.Status,
		).Scan(&message.ID, &message.ConversationID, &message.Direction, &message.Body,
			&message.KeywordMatched, &message.Segments, &message.Status,
			&message.CreatedAt); err != nil {
			return err
		}
		// An inbound message reopens a closed thread.
		//
		// Closing a conversation says "this is dealt with". A customer replying
		// afterwards is the definition of it not being dealt with, and leaving
		// the thread closed buries their message: closed threads are filtered
		// out of the default inbox view, so the reply is invisible until
		// somebody goes looking for it. Marking it unread is not enough on its
		// own — an unread message inside a closed thread is still hidden.
		//
		// Outbound messages leave the status alone: an agent replying inside a
		// closed thread has not reopened anything.
		_, err := tx.Exec(ctx, `
			UPDATE conversations
			   SET unread = $2,
			       status = CASE WHEN $2 AND status = 'closed' THEN 'open'
			                     ELSE status END,
			       updated_at = now()
			 WHERE id = $1`,
			message.ConversationID, message.Direction == "inbound")
		return err
	})
	if err != nil {
		return ConversationMessage{}, fmt.Errorf("store: append conversation message: %w", err)
	}
	return message, nil
}

func SetConversationStatus(ctx context.Context, pool *pgxpool.Pool, id Identity,
	conversationID uuid.UUID, status string) error {

	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE conversations SET status = $2, updated_at = now() WHERE id = $1`,
			conversationID, status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func MarkConversationRead(ctx context.Context, pool *pgxpool.Pool, id Identity,
	conversationID uuid.UUID) error {

	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE conversations SET unread = false WHERE id = $1`, conversationID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// EnsureConversation finds or opens the thread for a contact on a channel.
// The unique constraint does the deduplication, so two simultaneous inbound
// messages cannot open two threads with the same person.
func EnsureConversation(ctx context.Context, pool *pgxpool.Pool, id Identity,
	contactID uuid.UUID, channel string) (uuid.UUID, error) {

	var conversationID uuid.UUID
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO conversations (tenant_id, contact_id, channel)
			VALUES ($1,$2,$3)
			ON CONFLICT (tenant_id, contact_id, channel)
			DO UPDATE SET updated_at = now()
			RETURNING id`, id.TenantID, contactID, channel).Scan(&conversationID)
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("store: ensure conversation: %w", err)
	}
	return conversationID, nil
}

// stopKeywords are the words that mean "stop messaging me". Matching is on the
// whole trimmed body, case-insensitive: a message that merely CONTAINS "stop"
// ("please don't stop the offers") is not an opt-out, and treating it as one
// would silently suppress a customer who asked for the opposite.
var stopKeywords = map[string]bool{
	"STOP": true, "UNSUBSCRIBE": true, "CANCEL": true, "END": true, "QUIT": true, "OPTOUT": true,
}

// ReceiveInboundMessage records a message from a contact: it opens or finds the
// thread, stores the message, and honours a STOP-class keyword by suppressing
// the contact.
//
// The suppression is the point. An inbound STOP that only appended a message
// would leave the person opted out in the transcript but still reachable by the
// next campaign, which is the compliance failure regulators fine for.
func ReceiveInboundMessage(ctx context.Context, pool *pgxpool.Pool, id Identity,
	contactID uuid.UUID, channel, body string) (ConversationMessage, error) {

	conversationID, err := EnsureConversation(ctx, pool, id, contactID, channel)
	if err != nil {
		return ConversationMessage{}, err
	}

	message := ConversationMessage{ConversationID: conversationID, Direction: "inbound", Body: body}
	keyword := strings.ToUpper(strings.TrimSpace(body))
	if stopKeywords[keyword] {
		message.KeywordMatched = &keyword
	}
	message, err = AppendConversationMessage(ctx, pool, id, message)
	if err != nil {
		return ConversationMessage{}, err
	}
	if message.KeywordMatched == nil {
		return message, nil
	}

	// The suppression key is the address the person actually messaged from, so
	// an email opt-out suppresses the email and an SMS one the msisdn. Keying
	// both off the msisdn would leave the other channel reachable.
	var msisdn string
	var email *string
	if err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT msisdn, email FROM contacts WHERE id = $1`,
			contactID).Scan(&msisdn, &email)
	}); err != nil {
		return ConversationMessage{}, fmt.Errorf("store: inbound contact lookup: %w", err)
	}

	suppression := Suppression{Identity: msisdn, Msisdn: &msisdn,
		Reason: "opted_out_keyword", Note: "Replied " + keyword}
	if channel == "EMAIL" && email != nil && *email != "" {
		suppression = Suppression{Identity: *email, Email: email,
			Reason: "opted_out_keyword", Note: "Replied " + keyword}
	}
	if _, err := AddSuppression(ctx, pool, id, suppression); err != nil {
		return ConversationMessage{}, err
	}
	return message, nil
}
