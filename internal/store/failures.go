package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saeedafri/sms-be/internal/domain/failures"
)

// FailureRule is a dictionary row with its bookkeeping.
type FailureRule struct {
	failures.Rule
	Version   int
	UpdatedBy *string
	UpdatedAt *time.Time
}

// ErrDuplicateRule is a rule answering the same question as an existing one.
var ErrDuplicateRule = errors.New("store: a rule for that code already exists")

const failureRuleColumns = `id, channel, source, route_id, vendor, match_kind, match_value, layer,
	code, title, detail, fix, fix_kind, retryable, auto_retried, customer_visible, version,
	updated_by, updated_at`

func scanFailureRule(row pgx.Row) (FailureRule, error) {
	var r FailureRule
	err := row.Scan(&r.ID, &r.Channel, &r.Source, &r.RouteID, &r.Vendor, &r.MatchKind,
		&r.MatchValue, &r.Layer, &r.Code, &r.Title, &r.Detail, &r.Fix, &r.FixKind,
		&r.Retryable, &r.AutoRetried, &r.CustomerVisible, &r.Version, &r.UpdatedBy, &r.UpdatedAt)
	return r, err
}

// FailureRules is the whole dictionary, in id order.
func FailureRules(ctx context.Context, pool *pgxpool.Pool) ([]FailureRule, error) {
	rows, err := pool.Query(ctx, `SELECT `+failureRuleColumns+` FROM failure_reasons ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: failure rules: %w", err)
	}
	defer rows.Close()
	var out []FailureRule
	for rows.Next() {
		rule, err := scanFailureRule(rows)
		if err != nil {
			return nil, fmt.Errorf("store: failure rules: %w", err)
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

const failureRulesKey = "failure-rules"

// CachedFailureRules is the dictionary for resolution, behind the hot cache.
// Create and update forget it, so an edit shows on the next read.
func CachedFailureRules(ctx context.Context, pool *pgxpool.Pool, cache *HotCache) ([]failures.Rule, error) {
	if value, found := cache.Get(failureRulesKey); found {
		return value.([]failures.Rule), nil
	}
	rows, err := FailureRules(ctx, pool)
	if err != nil {
		return nil, err
	}
	rules := make([]failures.Rule, len(rows))
	for i, row := range rows {
		rules[i] = row.Rule
	}
	cache.Put(failureRulesKey, rules)
	return rules, nil
}

// ForgetFailureRules drops the cached dictionary after an edit.
func ForgetFailureRules(cache *HotCache) { cache.Forget(failureRulesKey) }

// GetFailureRule reads one rule.
func GetFailureRule(ctx context.Context, pool *pgxpool.Pool, id string) (FailureRule, error) {
	rule, err := scanFailureRule(pool.QueryRow(ctx,
		`SELECT `+failureRuleColumns+` FROM failure_reasons WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return FailureRule{}, ErrNotFound
	}
	if err != nil {
		return FailureRule{}, fmt.Errorf("store: get failure rule: %w", err)
	}
	return rule, nil
}

// CreateFailureRule adds a rule, numbered from the sequence.
func CreateFailureRule(ctx context.Context, pool *pgxpool.Pool, rule failures.Rule, by string) (FailureRule, error) {
	created, err := scanFailureRule(pool.QueryRow(ctx, `
		INSERT INTO failure_reasons (id, channel, source, route_id, vendor, match_kind, match_value,
		    layer, code, title, detail, fix, fix_kind, retryable, customer_visible, updated_by, updated_at)
		VALUES ('fr-' || lpad(nextval('failure_reason_ids')::text, 4, '0'),
		    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, now())
		RETURNING `+failureRuleColumns,
		rule.Channel, rule.Source, rule.RouteID, rule.Vendor, rule.MatchKind, rule.MatchValue,
		rule.Layer, rule.Code, rule.Title, rule.Detail, rule.Fix, rule.FixKind, rule.Retryable,
		rule.CustomerVisible, by))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return FailureRule{}, ErrDuplicateRule
	}
	if err != nil {
		return FailureRule{}, fmt.Errorf("store: create failure rule: %w", err)
	}
	return created, nil
}

// FailureRulePatch is the editable half of a rule. A nil field is left alone;
// the Set flags say a nullable field was sent, null included.
type FailureRulePatch struct {
	Layer, Title                  *string
	Detail, Fix, FixKind          *string
	DetailSet, FixSet, FixKindSet bool
	Retryable, CustomerVisible    *bool
}

// UpdateFailureRule applies a patch as the rule's next version.
func UpdateFailureRule(ctx context.Context, pool *pgxpool.Pool, id string, patch FailureRulePatch,
	by string) (FailureRule, error) {

	updated, err := scanFailureRule(pool.QueryRow(ctx, `
		UPDATE failure_reasons SET
		    layer = coalesce($2, layer),
		    title = coalesce($3, title),
		    detail = CASE WHEN $5 THEN $4 ELSE detail END,
		    fix = CASE WHEN $7 THEN $6 ELSE fix END,
		    fix_kind = CASE WHEN $9 THEN $8 ELSE fix_kind END,
		    retryable = coalesce($10, retryable),
		    customer_visible = coalesce($11, customer_visible),
		    version = version + 1, updated_by = $12, updated_at = now()
		WHERE id = $1
		RETURNING `+failureRuleColumns,
		id, patch.Layer, patch.Title, patch.Detail, patch.DetailSet, patch.Fix, patch.FixSet,
		patch.FixKind, patch.FixKindSet, patch.Retryable, patch.CustomerVisible, by))
	if errors.Is(err, pgx.ErrNoRows) {
		return FailureRule{}, ErrNotFound
	}
	if err != nil {
		return FailureRule{}, fmt.Errorf("store: update failure rule: %w", err)
	}
	return updated, nil
}

// FailedCode is one distinct failure in the message log: who carried it, how
// it failed, its raw code, and how often and when it was seen.
type FailedCode struct {
	Channel, Status, ErrorCode, RouteID, Carrier string
	Count                                        uint64
	FirstSeen, LastSeen                          time.Time
}

// FailedCodes groups every real (not simulated) failed or refused message by
// the question it asks the dictionary, across tenants.
// A nil tenant reads every tenant's.
func FailedCodes(ctx context.Context, conn driver.Conn, tenantID *uuid.UUID) ([]FailedCode, error) {
	where, args := "", []any{}
	if tenantID != nil {
		where, args = " AND tenant_id = ?", append(args, *tenantID)
	}
	rows, err := conn.Query(ctx, `
		SELECT coalesce(delivered_channel, channel) AS carried, status, error_code,
		       ifNull(route_id, '') AS route, carrier,
		       count() AS seen, min(created_at), max(updated_at)
		FROM messages FINAL
		WHERE status IN ('rejected', 'carrier_rejected', 'undelivered', 'expired')
		  AND error_code IS NOT NULL AND simulated = false`+where+`
		GROUP BY carried, status, error_code, route, carrier`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: failed codes: %w", err)
	}
	defer rows.Close()
	var out []FailedCode
	for rows.Next() {
		var code FailedCode
		var errorCode *string
		if err := rows.Scan(&code.Channel, &code.Status, &errorCode, &code.RouteID, &code.Carrier,
			&code.Count, &code.FirstSeen, &code.LastSeen); err != nil {
			return nil, fmt.Errorf("store: failed codes: %w", err)
		}
		if errorCode != nil {
			code.ErrorCode = *errorCode
		}
		out = append(out, code)
	}
	return out, rows.Err()
}
