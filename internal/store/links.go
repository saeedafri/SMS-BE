package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ShortLink is one tracked destination.
type ShortLink struct {
	Code          string
	Destination   string
	CampaignID    *uuid.UUID
	Recipient     *string
	Label         *string
	AppendClickID bool
	ExpiresAt     *time.Time
	CreatedAt     time.Time
	Clicks        int
	HumanClicks   int
}

// LinkClick is one hit on a link.
type LinkClick struct {
	ID          uuid.UUID
	Code        string
	ClickedAt   time.Time
	IsBot       bool
	Device      string
	OS          string
	Recipient   *string
	CampaignID  *uuid.UUID
	Destination string
}

var ErrLinkExpired = errors.New("store: link expired")

const codeAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// NewLinkCode is eight characters from an alphabet with no look-alikes
// (0/O, 1/l/I). 55^8 is ~8e13, so a guess lands on a real link about never.
func NewLinkCode() string {
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	for i, b := range raw {
		raw[i] = codeAlphabet[int(b)%len(codeAlphabet)]
	}
	return string(raw)
}

// CreateShortLinks inserts links for a tenant, retrying a code collision.
func CreateShortLinks(ctx context.Context, pool *pgxpool.Pool, id Identity,
	links []ShortLink) ([]ShortLink, error) {

	out := make([]ShortLink, len(links))
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		for i, link := range links {
			for attempt := 0; ; attempt++ {
				link.Code = NewLinkCode()
				if _, err := tx.Exec(ctx, `SAVEPOINT link_code`); err != nil {
					return err
				}
				err := tx.QueryRow(ctx, `
					INSERT INTO short_links (code, tenant_id, destination, campaign_id,
					    recipient, label, append_click_id, expires_at)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at`,
					link.Code, id.TenantID, link.Destination, link.CampaignID,
					link.Recipient, link.Label, link.AppendClickID, link.ExpiresAt).
					Scan(&link.CreatedAt)
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "23505" && attempt < 5 {
					if _, rbErr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT link_code`); rbErr != nil {
						return rbErr
					}
					continue
				}
				if err != nil {
					return err
				}
				out[i] = link
				break
			}
		}
		return nil
	})
	return out, err
}

// LinkFilter narrows list and click queries.
type LinkFilter struct {
	CampaignID  *uuid.UUID
	Code        string
	From, To    time.Time
	IncludeBots bool
}

// ListShortLinks pages a tenant's links, newest first, with their click counts.
func ListShortLinks(ctx context.Context, pool *pgxpool.Pool, id Identity,
	filter LinkFilter, page, limit int) ([]ShortLink, int, error) {

	var links []ShortLink
	var total int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		where := `WHERE ($1::uuid IS NULL OR l.campaign_id = $1)`
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM short_links l `+where,
			filter.CampaignID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT l.code, l.destination, l.campaign_id, l.recipient, l.label,
			       l.append_click_id, l.expires_at, l.created_at,
			       count(c.id)::int, count(c.id) FILTER (WHERE NOT c.is_bot)::int
			FROM short_links l LEFT JOIN link_clicks c ON c.code = l.code `+where+`
			GROUP BY l.code ORDER BY l.created_at DESC, l.code
			LIMIT $2 OFFSET $3`, filter.CampaignID, limit, (page-1)*limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l ShortLink
			if err := rows.Scan(&l.Code, &l.Destination, &l.CampaignID, &l.Recipient,
				&l.Label, &l.AppendClickID, &l.ExpiresAt, &l.CreatedAt,
				&l.Clicks, &l.HumanClicks); err != nil {
				return err
			}
			links = append(links, l)
		}
		return rows.Err()
	})
	return links, total, err
}

// ListLinkClicks pages click logs, newest first. Bots are left out unless asked.
func ListLinkClicks(ctx context.Context, pool *pgxpool.Pool, id Identity,
	filter LinkFilter, page, limit int) ([]LinkClick, int, error) {

	var clicks []LinkClick
	var total int
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		where := `WHERE ($1::uuid IS NULL OR l.campaign_id = $1)
		          AND ($2 = '' OR c.code = $2)
		          AND ($3::timestamptz IS NULL OR c.clicked_at >= $3)
		          AND ($4::timestamptz IS NULL OR c.clicked_at < $4)
		          AND ($5 OR NOT c.is_bot)`
		args := []any{filter.CampaignID, filter.Code, nullTime(filter.From),
			nullTime(filter.To), filter.IncludeBots}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM link_clicks c
			JOIN short_links l ON l.code = c.code `+where, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(`
			SELECT c.id, c.code, c.clicked_at, c.is_bot, c.device, c.os,
			       l.recipient, l.campaign_id, l.destination
			FROM link_clicks c JOIN short_links l ON l.code = c.code `+where+`
			ORDER BY c.clicked_at DESC, c.id LIMIT $%d OFFSET $%d`, 6, 7),
			append(args, limit, (page-1)*limit)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c LinkClick
			if err := rows.Scan(&c.ID, &c.Code, &c.ClickedAt, &c.IsBot, &c.Device,
				&c.OS, &c.Recipient, &c.CampaignID, &c.Destination); err != nil {
				return err
			}
			clicks = append(clicks, c)
		}
		return rows.Err()
	})
	return clicks, total, err
}

// LinkStats is the headline numbers for a set of links.
type LinkStats struct {
	Links        int
	Clicks       int
	HumanClicks  int
	UniqueClicks int
	ByDevice     map[string]int
	ByDay        []DayCount
}

// DayCount is a human-click count for one calendar day (UTC).
type DayCount struct {
	Day    time.Time
	Clicks int
}

func LinkStatistics(ctx context.Context, pool *pgxpool.Pool, id Identity,
	filter LinkFilter) (LinkStats, error) {

	stats := LinkStats{ByDevice: map[string]int{}, ByDay: []DayCount{}}
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		scope := `FROM short_links l LEFT JOIN link_clicks c ON c.code = l.code
		          AND ($2::timestamptz IS NULL OR c.clicked_at >= $2)
		          AND ($3::timestamptz IS NULL OR c.clicked_at < $3)
		          WHERE ($1::uuid IS NULL OR l.campaign_id = $1)`
		args := []any{filter.CampaignID, nullTime(filter.From), nullTime(filter.To)}
		if err := tx.QueryRow(ctx, `SELECT count(DISTINCT l.code)::int, count(c.id)::int,
			count(c.id) FILTER (WHERE NOT c.is_bot)::int,
			count(DISTINCT c.code) FILTER (WHERE NOT c.is_bot)::int `+scope, args...).
			Scan(&stats.Links, &stats.Clicks, &stats.HumanClicks, &stats.UniqueClicks); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT c.device, count(*)::int `+scope+`
			AND c.id IS NOT NULL AND NOT c.is_bot GROUP BY c.device`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var device string
			var n int
			if err := rows.Scan(&device, &n); err != nil {
				rows.Close()
				return err
			}
			stats.ByDevice[device] = n
		}
		rows.Close()
		days, err := tx.Query(ctx, `SELECT date_trunc('day', c.clicked_at AT TIME ZONE 'UTC'), count(*)::int `+scope+`
			AND c.id IS NOT NULL AND NOT c.is_bot GROUP BY 1 ORDER BY 1`, args...)
		if err != nil {
			return err
		}
		defer days.Close()
		for days.Next() {
			var d DayCount
			if err := days.Scan(&d.Day, &d.Clicks); err != nil {
				return err
			}
			stats.ByDay = append(stats.ByDay, d)
		}
		return days.Err()
	})
	return stats, err
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// ResolveLink is the redirect's read, on the admin pool, by code alone: the
// recipient has no session and the code is what authorises it.
func ResolveLink(ctx context.Context, admin *pgxpool.Pool, code string, now time.Time) (
	ShortLink, uuid.UUID, error) {

	var l ShortLink
	var tenant uuid.UUID
	err := admin.QueryRow(ctx, `
		SELECT code, destination, append_click_id, expires_at, tenant_id
		FROM short_links WHERE code = $1`, code).
		Scan(&l.Code, &l.Destination, &l.AppendClickID, &l.ExpiresAt, &tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShortLink{}, uuid.Nil, ErrNotFound
	}
	if err != nil {
		return ShortLink{}, uuid.Nil, err
	}
	if l.ExpiresAt != nil && !l.ExpiresAt.After(now) {
		return l, tenant, ErrLinkExpired
	}
	return l, tenant, nil
}

// RecordClick writes a hit. Best effort by design: a recipient is redirected
// whether or not the log write works.
func RecordClick(ctx context.Context, admin *pgxpool.Pool, tenant uuid.UUID, code string,
	isBot bool, device, osName, ipHash, userAgent string) error {

	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	_, err := admin.Exec(ctx, `
		INSERT INTO link_clicks (code, tenant_id, is_bot, device, os, ip_hash, user_agent)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''))`,
		code, tenant, isBot, device, osName, ipHash, userAgent)
	return err
}
