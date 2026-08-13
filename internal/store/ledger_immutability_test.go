package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// The ledger being append-only is the foundation of every billing guarantee we
// make. A convention enforced only by code review stops holding the first time
// someone runs a "quick fix" script against the database — so it is a trigger,
// and this proves the trigger actually fires.
//
// The attempts below use the ADMIN connection, which owns the tables and
// bypasses RLS. That is deliberate: if even a superuser cannot rewrite history,
// nobody can.
func TestLedgerRejectsUpdateAndDelete(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	admin := seedTenants(t, ctx, tenantID)

	var entryID uuid.UUID
	if err := admin.QueryRow(ctx, `
		INSERT INTO wallet_ledger (tenant_id, currency, entry_type, amount_minor, balance_after_minor)
		VALUES ($1, 'INR', 'topup', 50000, 50000)
		RETURNING id`, tenantID).Scan(&entryID); err != nil {
		t.Fatalf("seed ledger entry: %v", err)
	}

	t.Run("update is rejected", func(t *testing.T) {
		_, err := admin.Exec(ctx,
			`UPDATE wallet_ledger SET amount_minor = 1 WHERE id = $1`, entryID)
		if err == nil {
			t.Fatal("UPDATE on wallet_ledger succeeded; the append-only trigger is not firing")
		}
		if !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("UPDATE failed for the wrong reason: %v", err)
		}
	})

	t.Run("delete is rejected", func(t *testing.T) {
		_, err := admin.Exec(ctx, `DELETE FROM wallet_ledger WHERE id = $1`, entryID)
		if err == nil {
			t.Fatal("DELETE on wallet_ledger succeeded; the append-only trigger is not firing")
		}
		if !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("DELETE failed for the wrong reason: %v", err)
		}
	})

	t.Run("the entry is still intact", func(t *testing.T) {
		var amount int64
		if err := admin.QueryRow(ctx,
			`SELECT amount_minor FROM wallet_ledger WHERE id = $1`, entryID).Scan(&amount); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if amount != 50000 {
			t.Fatalf("amount_minor = %d after the rejected mutations, want 50000", amount)
		}
	})

	// Inserting must still work — an append-only table that rejects appends
	// would pass the two tests above and be useless.
	t.Run("insert is still allowed", func(t *testing.T) {
		if _, err := admin.Exec(ctx, `
			INSERT INTO wallet_ledger (tenant_id, currency, entry_type, amount_minor, balance_after_minor)
			VALUES ($1, 'INR', 'charge', 1200, 48800)`, tenantID); err != nil {
			t.Fatalf("INSERT was rejected: %v", err)
		}
	})
}

// The application role has no UPDATE or DELETE grant on the ledger either.
// The trigger and the missing privilege are two locks on the same door; this
// checks the second one.
func TestApplicationRoleHasNoLedgerMutationPrivilege(t *testing.T) {
	ctx := context.Background()
	pool := appPool(t, ctx)

	for _, statement := range []string{
		`UPDATE wallet_ledger SET amount_minor = 1`,
		`DELETE FROM wallet_ledger`,
	} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Fatalf("the application role executed %q; it should lack the privilege", statement)
		}
	}
}

// Amounts are always positive, with the sign implied by entry_type. A negative
// amount would let one row mean two things depending on who read it.
func TestLedgerRejectsNonPositiveAmounts(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	admin := seedTenants(t, ctx, tenantID)

	for _, amount := range []int64{0, -100} {
		_, err := admin.Exec(ctx, `
			INSERT INTO wallet_ledger (tenant_id, currency, entry_type, amount_minor, balance_after_minor)
			VALUES ($1, 'INR', 'topup', $2, 0)`, tenantID, amount)
		if err == nil {
			t.Errorf("an amount_minor of %d was accepted, want rejected", amount)
		}
	}
}

var _ = store.ErrNotFound
