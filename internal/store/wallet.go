package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInsufficientFunds is returned when a charge would overdraw a wallet.
var ErrInsufficientFunds = errors.New("store: insufficient funds")

type WalletBalance struct {
	Currency     string
	BalanceMinor int64
}

// LedgerEntry mirrors the contract: AmountMinor is always positive and the
// direction comes from Type.
type LedgerEntry struct {
	ID                uuid.UUID
	Currency          string
	Type              string
	AmountMinor       int64
	BalanceAfterMinor int64
	Description       string
	CampaignID        *uuid.UUID
	CampaignName      *string
	JourneyID         *uuid.UUID
	JourneyName       *string
	CreatedAt         time.Time
}

// creditTypes move money into a wallet. Everything else moves it out. Keeping
// this in one place means a new entry type cannot accidentally get the wrong
// sign in one code path and the right one in another.
var creditTypes = map[string]bool{
	"topup": true, "auto_recharge": true, "refund": true,
}

func IsCredit(entryType string) bool { return creditTypes[entryType] }

func ListWalletBalances(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]WalletBalance, error) {
	var out []WalletBalance
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT currency, balance_minor FROM wallet_balances ORDER BY currency`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var balance WalletBalance
			if err := rows.Scan(&balance.Currency, &balance.BalanceMinor); err != nil {
				return err
			}
			out = append(out, balance)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: list balances: %w", err)
	}
	return out, nil
}

// AppendLedgerEntry writes one entry and moves the balance in the SAME
// transaction, taking a row lock on the balance first.
//
// The lock is the whole point. Without SELECT ... FOR UPDATE, two concurrent
// charges both read the same starting balance and the second write overwrites
// the first — money silently invented, and no error anywhere to notice it by.
func AppendLedgerEntry(ctx context.Context, pool *pgxpool.Pool, id Identity,
	entry LedgerEntry) (LedgerEntry, error) {

	var created LedgerEntry
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		// Create the wallet on first use, then lock it. ON CONFLICT DO NOTHING
		// keeps two simultaneous first top-ups from racing on the insert.
		if _, err := tx.Exec(ctx,
			`INSERT INTO wallet_balances (tenant_id, currency) VALUES ($1, $2)
			 ON CONFLICT (tenant_id, currency) DO NOTHING`,
			id.TenantID, entry.Currency); err != nil {
			return err
		}

		var balance int64
		if err := tx.QueryRow(ctx,
			`SELECT balance_minor FROM wallet_balances
			 WHERE tenant_id = $1 AND currency = $2 FOR UPDATE`,
			id.TenantID, entry.Currency).Scan(&balance); err != nil {
			return err
		}

		if IsCredit(entry.Type) {
			balance += entry.AmountMinor
		} else {
			if balance < entry.AmountMinor {
				return ErrInsufficientFunds
			}
			balance -= entry.AmountMinor
		}

		if _, err := tx.Exec(ctx,
			`UPDATE wallet_balances SET balance_minor = $1, updated_at = now()
			 WHERE tenant_id = $2 AND currency = $3`,
			balance, id.TenantID, entry.Currency); err != nil {
			return err
		}

		return tx.QueryRow(ctx, `
			INSERT INTO wallet_ledger (tenant_id, currency, entry_type, amount_minor,
			    balance_after_minor, description, campaign_id, campaign_name,
			    journey_id, journey_name)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			RETURNING id, currency, entry_type, amount_minor, balance_after_minor,
			    description, campaign_id, campaign_name, journey_id, journey_name, created_at`,
			id.TenantID, entry.Currency, entry.Type, entry.AmountMinor, balance,
			entry.Description, entry.CampaignID, entry.CampaignName,
			entry.JourneyID, entry.JourneyName,
		).Scan(&created.ID, &created.Currency, &created.Type, &created.AmountMinor,
			&created.BalanceAfterMinor, &created.Description, &created.CampaignID,
			&created.CampaignName, &created.JourneyID, &created.JourneyName, &created.CreatedAt)
	})
	if errors.Is(err, ErrInsufficientFunds) {
		return LedgerEntry{}, ErrInsufficientFunds
	}
	if err != nil {
		return LedgerEntry{}, fmt.Errorf("store: append ledger entry: %w", err)
	}
	return created, nil
}

// LedgerPage returns one page of entries, newest first, using keyset
// pagination over (created_at, id). OFFSET would degrade as the ledger grows,
// and the contract already models cursors as opaque strings.
func LedgerPage(ctx context.Context, pool *pgxpool.Pool, id Identity,
	currency, cursor string, limit int) ([]LedgerEntry, string, error) {

	if limit <= 0 || limit > 200 {
		limit = 50
	}
	cursorTime, cursorID, err := decodeLedgerCursor(cursor)
	if err != nil {
		return nil, "", err
	}

	var entries []LedgerEntry
	err = WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		// Fetch one extra row to learn whether another page exists without a
		// second count query.
		rows, err := tx.Query(ctx, `
			SELECT id, currency, entry_type, amount_minor, balance_after_minor,
			       description, campaign_id, campaign_name, journey_id, journey_name, created_at
			FROM wallet_ledger
			WHERE ($1 = '' OR currency = $1)
			  AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3))
			ORDER BY created_at DESC, id DESC
			LIMIT $4`, currency, cursorTime, cursorID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var entry LedgerEntry
			if err := rows.Scan(&entry.ID, &entry.Currency, &entry.Type, &entry.AmountMinor,
				&entry.BalanceAfterMinor, &entry.Description, &entry.CampaignID,
				&entry.CampaignName, &entry.JourneyID, &entry.JourneyName,
				&entry.CreatedAt); err != nil {
				return err
			}
			entries = append(entries, entry)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", fmt.Errorf("store: ledger page: %w", err)
	}

	next := ""
	if len(entries) > limit {
		last := entries[limit-1]
		entries = entries[:limit]
		next = encodeLedgerCursor(last.CreatedAt, last.ID)
	}
	return entries, next, nil
}

func encodeLedgerCursor(at time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeLedgerCursor(cursor string) (*time.Time, *uuid.UUID, error) {
	if cursor == "" {
		return nil, nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w: malformed cursor", ErrInvalidCursor)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return nil, nil, fmt.Errorf("store: %w: malformed cursor", ErrInvalidCursor)
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w: malformed cursor", ErrInvalidCursor)
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w: malformed cursor", ErrInvalidCursor)
	}
	return &at, &id, nil
}

// ErrInvalidCursor lets handlers answer 422 rather than 500 when a client
// echoes back something we never issued.
var ErrInvalidCursor = errors.New("invalid cursor")

// LedgerSum totals a wallet straight from the entries. It exists so the
// balance can be checked against its own history rather than trusted.
func LedgerSum(ctx context.Context, pool *pgxpool.Pool, id Identity, currency string) (int64, error) {
	var total int64
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT coalesce(sum(
			    CASE WHEN entry_type IN ('topup','auto_recharge','refund')
			         THEN amount_minor ELSE -amount_minor END), 0)
			FROM wallet_ledger WHERE currency = $1`, currency).Scan(&total)
	})
	if err != nil {
		return 0, fmt.Errorf("store: ledger sum: %w", err)
	}
	return total, nil
}

// PaymentMethod is a stored card. Only the brand and last four digits are
// kept — full card data never touches this system, which is the entire point
// of delegating capture to a gateway.
type PaymentMethod struct {
	ID        uuid.UUID
	Brand     string
	Last4     string
	IsDefault bool
}

func ListPaymentMethods(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]PaymentMethod, error) {
	var out []PaymentMethod
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, brand, last4, is_default FROM payment_methods
			 ORDER BY is_default DESC, created_at`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var method PaymentMethod
			if err := rows.Scan(&method.ID, &method.Brand, &method.Last4,
				&method.IsDefault); err != nil {
				return err
			}
			out = append(out, method)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: list payment methods: %w", err)
	}
	return out, nil
}

// AddPaymentMethod makes the first card the default automatically — a tenant
// with cards but no default would break auto-recharge in a way nobody notices
// until a wallet runs dry.
func AddPaymentMethod(ctx context.Context, pool *pgxpool.Pool, id Identity,
	brand, last4 string) (PaymentMethod, error) {

	var created PaymentMethod
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var existing int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM payment_methods`).Scan(&existing); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`INSERT INTO payment_methods (tenant_id, brand, last4, is_default)
			 VALUES ($1, $2, $3, $4)
			 RETURNING id, brand, last4, is_default`,
			id.TenantID, brand, last4, existing == 0,
		).Scan(&created.ID, &created.Brand, &created.Last4, &created.IsDefault)
	})
	if err != nil {
		return PaymentMethod{}, fmt.Errorf("store: add payment method: %w", err)
	}
	return created, nil
}

// SetDefaultPaymentMethod clears the previous default first: a partial unique
// index enforces at most one, so setting a second without clearing would fail.
func SetDefaultPaymentMethod(ctx context.Context, pool *pgxpool.Pool, id Identity,
	methodID uuid.UUID) (PaymentMethod, error) {

	var updated PaymentMethod
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE payment_methods SET is_default = false WHERE is_default`); err != nil {
			return err
		}
		err := tx.QueryRow(ctx,
			`UPDATE payment_methods SET is_default = true WHERE id = $1
			 RETURNING id, brand, last4, is_default`, methodID,
		).Scan(&updated.ID, &updated.Brand, &updated.Last4, &updated.IsDefault)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return PaymentMethod{}, ErrNotFound
	}
	if err != nil {
		return PaymentMethod{}, fmt.Errorf("store: set default payment method: %w", err)
	}
	return updated, nil
}

// RemovePaymentMethod deletes a card and promotes another to default if the
// one removed held that role, so "has cards but no default" never occurs.
func RemovePaymentMethod(ctx context.Context, pool *pgxpool.Pool, id Identity, methodID uuid.UUID) error {
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var wasDefault bool
		err := tx.QueryRow(ctx,
			`DELETE FROM payment_methods WHERE id = $1 RETURNING is_default`,
			methodID).Scan(&wasDefault)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !wasDefault {
			return nil
		}
		_, err = tx.Exec(ctx, `
			UPDATE payment_methods SET is_default = true
			WHERE id = (SELECT id FROM payment_methods ORDER BY created_at LIMIT 1)`)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: remove payment method: %w", err)
	}
	return nil
}

func PaymentMethodExists(ctx context.Context, pool *pgxpool.Pool, id Identity, methodID uuid.UUID) (bool, error) {
	var exists bool
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT exists(SELECT 1 FROM payment_methods WHERE id = $1)`, methodID).Scan(&exists)
	})
	if err != nil {
		return false, fmt.Errorf("store: payment method exists: %w", err)
	}
	return exists, nil
}

// AutoRecharge tops a wallet up automatically when it falls below a threshold.
type AutoRecharge struct {
	Currency          string
	Enabled           bool
	ThresholdMinor    int64
	TopUpMinor        int64
	PaymentMethodID   *uuid.UUID
	LastFailureAt     *time.Time
	LastFailureReason *string
}

func ListAutoRecharge(ctx context.Context, pool *pgxpool.Pool, id Identity) ([]AutoRecharge, error) {
	var out []AutoRecharge
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT currency, enabled, threshold_minor, topup_minor, payment_method_id,
			       last_failure_at, last_failure_reason
			FROM auto_recharge_configs ORDER BY currency`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var config AutoRecharge
			if err := rows.Scan(&config.Currency, &config.Enabled, &config.ThresholdMinor,
				&config.TopUpMinor, &config.PaymentMethodID,
				&config.LastFailureAt, &config.LastFailureReason); err != nil {
				return err
			}
			out = append(out, config)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: list auto-recharge: %w", err)
	}
	return out, nil
}

func UpsertAutoRecharge(ctx context.Context, pool *pgxpool.Pool, id Identity,
	config AutoRecharge) (AutoRecharge, error) {

	var saved AutoRecharge
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO auto_recharge_configs
			    (tenant_id, currency, enabled, threshold_minor, topup_minor, payment_method_id)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (tenant_id, currency) DO UPDATE SET
			    enabled = excluded.enabled,
			    threshold_minor = excluded.threshold_minor,
			    topup_minor = excluded.topup_minor,
			    payment_method_id = excluded.payment_method_id
			RETURNING currency, enabled, threshold_minor, topup_minor, payment_method_id,
			          last_failure_at, last_failure_reason`,
			id.TenantID, config.Currency, config.Enabled, config.ThresholdMinor,
			config.TopUpMinor, config.PaymentMethodID,
		).Scan(&saved.Currency, &saved.Enabled, &saved.ThresholdMinor, &saved.TopUpMinor,
			&saved.PaymentMethodID, &saved.LastFailureAt, &saved.LastFailureReason)
	})
	if err != nil {
		return AutoRecharge{}, fmt.Errorf("store: upsert auto-recharge: %w", err)
	}
	return saved, nil
}

// PricingRate is one rate-card line. Rates are platform-wide, not tenant-owned,
// so this needs no tenant scope. Per-tenant overrides arrive with the operator
// console in Stage 9.
type PricingRate struct {
	Country         string
	Channel         string
	Category        string
	PerSegmentMinor int64
	Currency        string
}

func ListPricingRates(ctx context.Context, pool *pgxpool.Pool) ([]PricingRate, error) {
	rows, err := pool.Query(ctx, `
		SELECT country, channel, coalesce(category, ''), per_segment_minor, currency
		FROM pricing_rates ORDER BY country, channel, category`)
	if err != nil {
		return nil, fmt.Errorf("store: list pricing: %w", err)
	}
	defer rows.Close()

	var out []PricingRate
	for rows.Next() {
		var rate PricingRate
		if err := rows.Scan(&rate.Country, &rate.Channel, &rate.Category,
			&rate.PerSegmentMinor, &rate.Currency); err != nil {
			return nil, fmt.Errorf("store: scan pricing: %w", err)
		}
		out = append(out, rate)
	}
	return out, rows.Err()
}

// FindPricingRate resolves the rate for a country/channel, preferring a
// category-specific line and falling back to the catch-all.
func FindPricingRate(ctx context.Context, pool *pgxpool.Pool,
	country, channel, category string) (PricingRate, error) {

	var rate PricingRate
	err := pool.QueryRow(ctx, `
		SELECT country, channel, coalesce(category, ''), per_segment_minor, currency
		FROM pricing_rates
		WHERE country = $1 AND channel = $2 AND coalesce(category, '') IN ($3, '')
		ORDER BY coalesce(category, '') DESC
		LIMIT 1`, country, channel, category,
	).Scan(&rate.Country, &rate.Channel, &rate.Category,
		&rate.PerSegmentMinor, &rate.Currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return PricingRate{}, ErrNotFound
	}
	if err != nil {
		return PricingRate{}, fmt.Errorf("store: find pricing rate: %w", err)
	}
	return rate, nil
}
