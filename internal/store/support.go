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

func ListSupportTickets(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]SupportTicket, error) {
	var out []SupportTicket
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT t.id, t.tenant_id, n.name, t.subject, t.category, t.status,
			       t.created_at, t.updated_at
			FROM support_tickets t JOIN tenants n ON n.id = t.tenant_id
			ORDER BY t.updated_at DESC, t.id DESC`)
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
		return nil, fmt.Errorf("store: list support tickets: %w", err)
	}
	return out, nil
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
	c.id, c.contact_id, ct.msisdn, c.channel, c.status, c.unread,
	EXISTS (SELECT 1 FROM suppressions s
	        WHERE s.tenant_id = c.tenant_id AND s.identity = ct.msisdn),
	COALESCE((SELECT m.body FROM conversation_messages m
	          WHERE m.conversation_id = c.id
	          ORDER BY m.created_at DESC, m.id DESC LIMIT 1), ''),
	c.created_at, c.updated_at`

func scanConversation(row pgx.Row) (Conversation, error) {
	var conversation Conversation
	err := row.Scan(&conversation.ID, &conversation.ContactID, &conversation.Identity,
		&conversation.Channel, &conversation.Status, &conversation.Unread,
		&conversation.Suppressed, &conversation.LastMessagePreview,
		&conversation.CreatedAt, &conversation.UpdatedAt)
	// The contact's display name is their identity until contacts carry names.
	conversation.ContactName = conversation.Identity
	return conversation, err
}

func ListConversations(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]Conversation, error) {
	var out []Conversation
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT `+conversationColumns+`
			FROM conversations c JOIN contacts ct ON ct.id = c.contact_id
			ORDER BY c.updated_at DESC, c.id DESC`)
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
		return nil, fmt.Errorf("store: list conversations: %w", err)
	}
	return out, nil
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
		_, err := tx.Exec(ctx,
			`UPDATE conversations SET unread = $2, updated_at = now() WHERE id = $1`,
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
