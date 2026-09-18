package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saeedafri/sms-be/internal/domain/billing"
)

// Postpaid credit on account: reads and writes for the two tables that hold
// facts, and nothing that derives anything. Every derived figure — allocation,
// status, received, outstanding, totals — is computed in
// internal/domain/billing/postpaid.go from what these functions return.
//
// Keeping the derivation out of SQL is deliberate. The rules are the part most
// likely to be got wrong, and in Go they are unit-testable without a database
// and mutation-checkable one rule at a time.

// AccountLedger is everything one tenant's postpaid picture is derived from.
type AccountLedger struct {
	Invoices []billing.Invoice
	Payments []billing.Payment
}

// LoadAccountLedger reads one tenant's invoices and payments.
//
// Both in one call because no caller wants one without the other: an invoice's
// figures are meaningless without the payments that settled it, and a payment's
// allocations are meaningless without the invoices it settled.
func LoadAccountLedger(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID) (
	AccountLedger, error) {

	var ledger AccountLedger
	err := WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		invoices, err := scanInvoices(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		payments, err := scanPayments(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		ledger.Invoices, ledger.Payments = invoices, payments
		return nil
	})
	return ledger, err
}

func scanInvoices(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]billing.Invoice, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, number, currency, taxable_minor, tax_rate_percent, tax_minor,
		       total_minor, issued_at, due_at, reference,
		       COALESCE(note, ''), COALESCE(issued_by, '')
		FROM account_invoices WHERE tenant_id = $1
		ORDER BY issued_at DESC, number DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	invoices := []billing.Invoice{}
	for rows.Next() {
		var invoice billing.Invoice
		if err := rows.Scan(&invoice.ID, &invoice.Number, &invoice.Currency,
			&invoice.TaxableMinor, &invoice.TaxRatePercent, &invoice.TaxMinor,
			&invoice.TotalMinor, &invoice.IssuedAt, &invoice.DueAt,
			&invoice.Reference, &invoice.Note, &invoice.IssuedBy); err != nil {
			return nil, err
		}
		invoices = append(invoices, invoice)
	}
	return invoices, rows.Err()
}

func scanPayments(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]billing.Payment, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, currency, amount_minor, reference, received_at, recorded_at,
		       COALESCE(recorded_by, ''), voided_at, COALESCE(void_reason, ''),
		       COALESCE(voided_by, '')
		FROM tenant_payments WHERE tenant_id = $1
		ORDER BY received_at DESC, recorded_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	payments := []billing.Payment{}
	for rows.Next() {
		var payment billing.Payment
		if err := rows.Scan(&payment.ID, &payment.Currency, &payment.AmountMinor,
			&payment.Reference, &payment.ReceivedAt, &payment.RecordedAt,
			&payment.RecordedBy, &payment.VoidedAt, &payment.VoidReason,
			&payment.VoidedBy); err != nil {
			return nil, err
		}
		payments = append(payments, payment)
	}
	return payments, rows.Err()
}

// IssueCredit is one credit issued on account: the money into the wallet and
// the invoice for it.
type IssueCredit struct {
	Currency       string
	AmountMinor    int64
	TaxRatePercent int
	TaxMinor       int64
	TotalMinor     int64
	DueAt          time.Time
	Reference      string
	Note           string
	IssuedBy       string
}

// IssueCreditOnAccount loads the wallet and raises the invoice for it, in ONE
// transaction.
//
// One transaction because the two are one act. Credit in the wallet with no
// invoice is money given away; an invoice with no credit is a bill for nothing.
// A failure between them would produce one or the other and nothing in the
// system would notice.
//
// A reference is issued to a tenant once — the account_invoices_reference index
// enforces it, so two operators booking the same credit at the same moment
// cannot both succeed: the second is ErrConflict and the wallet moves once.
func IssueCreditOnAccount(ctx context.Context, pool *pgxpool.Pool, id Identity,
	credit IssueCredit) (LedgerEntry, billing.Invoice, error) {

	var entry LedgerEntry
	var invoice billing.Invoice

	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		// Serialise issuance for this tenant, so two credits cannot read the
		// same highest invoice number and both claim the next one. Held for the
		// transaction, released on commit or rollback without any unlock call.
		if _, err := tx.Exec(ctx,
			`SELECT pg_advisory_xact_lock(hashtext('account_invoices:' || $1::text))`,
			id.TenantID); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO wallet_balances (tenant_id, currency) VALUES ($1, $2)
			 ON CONFLICT (tenant_id, currency) DO NOTHING`,
			id.TenantID, credit.Currency); err != nil {
			return err
		}
		var balance int64
		if err := tx.QueryRow(ctx,
			`SELECT balance_minor FROM wallet_balances
			 WHERE tenant_id = $1 AND currency = $2 FOR UPDATE`,
			id.TenantID, credit.Currency).Scan(&balance); err != nil {
			return err
		}
		// Only the taxable amount reaches the wallet. The tax is owed, not
		// spendable — crediting the gross would hand the tenant sending power
		// they never bought.
		balance += credit.AmountMinor
		if _, err := tx.Exec(ctx,
			`UPDATE wallet_balances SET balance_minor = $1, updated_at = now()
			 WHERE tenant_id = $2 AND currency = $3`,
			balance, id.TenantID, credit.Currency); err != nil {
			return err
		}

		number, err := nextInvoiceNumber(ctx, tx, id.TenantID, credit.DueAt)
		if err != nil {
			return err
		}

		if err := tx.QueryRow(ctx, `
			INSERT INTO account_invoices (tenant_id, number, currency, taxable_minor,
			    tax_rate_percent, tax_minor, total_minor, due_at, reference, note, issued_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), NULLIF($11, ''))
			RETURNING id, number, currency, taxable_minor, tax_rate_percent, tax_minor,
			    total_minor, issued_at, due_at, reference,
			    COALESCE(note, ''), COALESCE(issued_by, '')`,
			id.TenantID, number, credit.Currency, credit.AmountMinor,
			credit.TaxRatePercent, credit.TaxMinor, credit.TotalMinor,
			credit.DueAt, credit.Reference, credit.Note, credit.IssuedBy,
		).Scan(&invoice.ID, &invoice.Number, &invoice.Currency, &invoice.TaxableMinor,
			&invoice.TaxRatePercent, &invoice.TaxMinor, &invoice.TotalMinor,
			&invoice.IssuedAt, &invoice.DueAt, &invoice.Reference,
			&invoice.Note, &invoice.IssuedBy); err != nil {
			if isUniqueViolation(err) {
				return ErrConflict
			}
			return err
		}

		// The ledger describes the credit by the invoice it raised, which is
		// what an operator reconciling the two screens needs to see. The
		// uniqueness that used to live on this string now lives on the invoice.
		if err := tx.QueryRow(ctx, `
			INSERT INTO wallet_ledger (tenant_id, currency, entry_type, amount_minor,
			    balance_after_minor, description)
			VALUES ($1, $2, 'topup', $3, $4, $5)
			RETURNING id, currency, entry_type, amount_minor, balance_after_minor,
			    description, campaign_id, campaign_name, journey_id, journey_name, created_at`,
			id.TenantID, credit.Currency, credit.AmountMinor, balance,
			"Credit on account "+invoice.Number,
		).Scan(&entry.ID, &entry.Currency, &entry.Type, &entry.AmountMinor,
			&entry.BalanceAfterMinor, &entry.Description, &entry.CampaignID,
			&entry.CampaignName, &entry.JourneyID, &entry.JourneyName,
			&entry.CreatedAt); err != nil {
			return err
		}
		return nil
	})
	return entry, invoice, err
}

// nextInvoiceNumber is INV-YYYY-MM-NNN, sequential per tenant within a month.
//
// Derived from what exists rather than from a counter, so a reset or a restore
// cannot leave the sequence ahead of the records. The month is taken from the
// due date's own location, so the number a tenant reads agrees with the month
// the invoice is due in.
func nextInvoiceNumber(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	issuedAt time.Time) (string, error) {

	prefix := fmt.Sprintf("INV-%04d-%02d-", issuedAt.Year(), int(issuedAt.Month()))
	var highest *string
	if err := tx.QueryRow(ctx,
		`SELECT max(number) FROM account_invoices
		 WHERE tenant_id = $1 AND number LIKE $2 || '%'`,
		tenantID, prefix).Scan(&highest); err != nil {
		return "", err
	}
	next := 1
	if highest != nil && len(*highest) > len(prefix) {
		var used int
		if _, err := fmt.Sscanf((*highest)[len(prefix):], "%d", &used); err == nil {
			next = used + 1
		}
	}
	return fmt.Sprintf("%s%03d", prefix, next), nil
}

// RecordPayment books one bank transfer.
//
// Recorded once per transfer rather than once per invoice it settles: a single
// transfer routinely settles several, and splitting it by hand would mean
// entering the same UTR twice — which defeats the duplicate guard that exists
// to stop one transfer being banked twice.
func RecordPayment(ctx context.Context, pool *pgxpool.Pool, id Identity,
	payment billing.Payment) (string, error) {

	var created string
	err := WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO tenant_payments (tenant_id, currency, amount_minor, reference,
			    received_at, recorded_by)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''))
			RETURNING id`,
			id.TenantID, payment.Currency, payment.AmountMinor, payment.Reference,
			payment.ReceivedAt, payment.RecordedBy).Scan(&created)
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return err
	})
	return created, err
}

// VoidPayment reverses a payment without deleting it.
//
// The row stays, carrying why and by whom. Because allocation is derived, that
// single column write is the whole of it: the next read excludes the payment
// and every invoice it had settled owes its money again, with no unwinding
// logic to get wrong.
//
// ErrNotFound when no such payment belongs to this tenant, ErrConflict when it
// is already void — the second press of a button, which must not overwrite the
// first reason.
func VoidPayment(ctx context.Context, pool *pgxpool.Pool, id Identity,
	paymentID uuid.UUID, reason, by string) error {

	return WithTenant(ctx, pool, id.TenantID, func(tx pgx.Tx) error {
		var alreadyVoid bool
		if err := tx.QueryRow(ctx,
			`SELECT voided_at IS NOT NULL FROM tenant_payments
			 WHERE id = $1 AND tenant_id = $2 FOR UPDATE`,
			paymentID, id.TenantID).Scan(&alreadyVoid); err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return err
		}
		if alreadyVoid {
			return ErrConflict
		}
		_, err := tx.Exec(ctx,
			`UPDATE tenant_payments
			 SET voided_at = now(), void_reason = $1, voided_by = NULLIF($2, '')
			 WHERE id = $3 AND tenant_id = $4`,
			reason, by, paymentID, id.TenantID)
		return err
	})
}

// SetCreditLimit caps what a tenant may owe, or removes the cap.
//
// Lowering a limit below what a tenant already owes is allowed: it stops
// further credit without rewriting history, which is what an operator reaching
// for this control in a hurry actually means.
func SetCreditLimit(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID,
	limitMinor *int64, currency string) error {

	tag, err := pool.Exec(ctx,
		`UPDATE tenants SET credit_limit_minor = $1,
		        credit_limit_currency = CASE WHEN $1::bigint IS NULL THEN NULL ELSE $2 END
		 WHERE id = $3`, limitMinor, currency, tenantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
