package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChatbotFlow answers inbound keywords on one channel.
type ChatbotFlow struct {
	ID         uuid.UUID
	Name       string
	Channel    string
	SenderID   uuid.UUID
	TemplateID *uuid.UUID
	ReplyBody  *string
	Active     bool
	Keywords   []string
	CreatedAt  time.Time
}

// ErrKeywordTaken: another flow already answers one of these keywords.
var ErrKeywordTaken = errors.New("store: keyword already answered by another flow")

// NormaliseKeyword is how a reply and a keyword are compared: trimmed, lower
// case, inner whitespace collapsed.
func NormaliseKeyword(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

const flowSelect = `SELECT f.id, f.name, f.channel, f.sender_id, f.template_id, f.reply_body,
	f.active, f.created_at,
	coalesce((SELECT array_agg(k.keyword ORDER BY k.keyword) FROM chatbot_keywords k
	          WHERE k.flow_id = f.id), '{}')
	FROM chatbot_flows f`

func scanFlow(row pgx.Row) (ChatbotFlow, error) {
	var f ChatbotFlow
	err := row.Scan(&f.ID, &f.Name, &f.Channel, &f.SenderID, &f.TemplateID, &f.ReplyBody,
		&f.Active, &f.CreatedAt, &f.Keywords)
	return f, err
}

func ListChatbotFlows(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]ChatbotFlow, error) {
	flows := []ChatbotFlow{}
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, flowSelect+` ORDER BY f.created_at DESC, f.id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			flow, err := scanFlow(rows)
			if err != nil {
				return err
			}
			flows = append(flows, flow)
		}
		return rows.Err()
	})
	return flows, err
}

func GetChatbotFlow(ctx context.Context, pool *pgxpool.Pool, id Identity, flowID uuid.UUID) (ChatbotFlow, error) {
	var flow ChatbotFlow
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		flow, err = scanFlow(tx.QueryRow(ctx, flowSelect+` WHERE f.id = $1`, flowID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ChatbotFlow{}, ErrNotFound
	}
	return flow, err
}

func writeKeywords(ctx context.Context, tx pgx.Tx, id Identity, flow ChatbotFlow) error {
	if _, err := tx.Exec(ctx, `DELETE FROM chatbot_keywords WHERE flow_id = $1`, flow.ID); err != nil {
		return err
	}
	for _, keyword := range flow.Keywords {
		if _, err := tx.Exec(ctx, `INSERT INTO chatbot_keywords (tenant_id, channel, keyword, flow_id)
			VALUES ($1,$2,$3,$4)`, id.TenantID, flow.Channel, keyword, flow.ID); err != nil {
			return err
		}
	}
	return nil
}

func keywordConflict(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && strings.Contains(pgErr.ConstraintName, "chatbot_keywords") {
		return ErrKeywordTaken
	}
	return err
}

// CreateChatbotFlow inserts a flow and its keywords as one unit.
func CreateChatbotFlow(ctx context.Context, pool *pgxpool.Pool, id Identity, flow ChatbotFlow) (ChatbotFlow, error) {
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO chatbot_flows (tenant_id, name, channel, sender_id, template_id, reply_body, active)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id, created_at`,
			id.TenantID, flow.Name, flow.Channel, flow.SenderID, flow.TemplateID,
			flow.ReplyBody, flow.Active).Scan(&flow.ID, &flow.CreatedAt); err != nil {
			return err
		}
		return writeKeywords(ctx, tx, id, flow)
	})
	if err != nil {
		return ChatbotFlow{}, keywordConflict(fmt.Errorf("store: create chatbot: %w", err))
	}
	return flow, nil
}

// UpdateChatbotFlow replaces a flow's settings and keywords.
func UpdateChatbotFlow(ctx context.Context, pool *pgxpool.Pool, id Identity, flow ChatbotFlow) error {
	return keywordConflict(WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE chatbot_flows SET name=$2, template_id=$3, reply_body=$4,
			active=$5, sender_id=$6, updated_at=now() WHERE id=$1`,
			flow.ID, flow.Name, flow.TemplateID, flow.ReplyBody, flow.Active, flow.SenderID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return writeKeywords(ctx, tx, id, flow)
	}))
}

func DeleteChatbotFlow(ctx context.Context, pool *pgxpool.Pool, id Identity, flowID uuid.UUID) error {
	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM chatbot_flows WHERE id = $1`, flowID)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return err
	})
}

// FlowForKeyword is the active flow answering this reply on this channel, if any.
func FlowForKeyword(ctx context.Context, pool *pgxpool.Pool, id Identity, channel, text string) (ChatbotFlow, bool, error) {
	var flow ChatbotFlow
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var err error
		flow, err = scanFlow(tx.QueryRow(ctx, flowSelect+`
			JOIN chatbot_keywords k ON k.flow_id = f.id
			WHERE k.channel = $1 AND k.keyword = $2 AND f.active`, channel, NormaliseKeyword(text)))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ChatbotFlow{}, false, nil
	}
	if err != nil {
		return ChatbotFlow{}, false, fmt.Errorf("store: flow for keyword: %w", err)
	}
	return flow, true, nil
}
