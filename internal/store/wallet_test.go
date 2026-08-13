package store_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saeedafri/sms-be/internal/store"
)

func walletFixture(t *testing.T) (context.Context, *pgxpool.Pool, store.Identity) {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	tenantID := uuid.New()
	seedTenants(t, ctx, tenantID)
	return ctx, appPool(t, ctx), store.Identity{TenantID: tenantID}
}

func TestCreditsAndChargesMoveTheBalance(t *testing.T) {
	ctx, pool, id := walletFixture(t)

	topup, err := store.AppendLedgerEntry(ctx, pool, id, store.LedgerEntry{
		Currency: "INR", Type: "topup", AmountMinor: 100_000,
	})
	if err != nil {
		t.Fatalf("topup: %v", err)
	}
	if topup.BalanceAfterMinor != 100_000 {
		t.Fatalf("balanceAfter = %d, want 100000", topup.BalanceAfterMinor)
	}

	charge, err := store.AppendLedgerEntry(ctx, pool, id, store.LedgerEntry{
		Currency: "INR", Type: "charge", AmountMinor: 30_000,
	})
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	// The stored amount stays positive; only the balance reflects direction.
	if charge.AmountMinor != 30_000 {
		t.Errorf("charge amountMinor = %d, want a positive 30000", charge.AmountMinor)
	}
	if charge.BalanceAfterMinor != 70_000 {
		t.Fatalf("balanceAfter = %d, want 70000", charge.BalanceAfterMinor)
	}

	balances, err := store.ListWalletBalances(ctx, pool, id)
	if err != nil {
		t.Fatalf("list balances: %v", err)
	}
	if len(balances) != 1 || balances[0].BalanceMinor != 70_000 {
		t.Fatalf("balances = %+v, want a single INR balance of 70000", balances)
	}
}

// A charge that would overdraw must be refused and must write nothing at all —
// a rejected charge that still left a ledger row would corrupt the history it
// was supposed to protect.
func TestOverdraftIsRefusedAndWritesNothing(t *testing.T) {
	ctx, pool, id := walletFixture(t)

	if _, err := store.AppendLedgerEntry(ctx, pool, id, store.LedgerEntry{
		Currency: "INR", Type: "topup", AmountMinor: 1_000,
	}); err != nil {
		t.Fatalf("topup: %v", err)
	}

	_, err := store.AppendLedgerEntry(ctx, pool, id, store.LedgerEntry{
		Currency: "INR", Type: "charge", AmountMinor: 5_000,
	})
	if !errors.Is(err, store.ErrInsufficientFunds) {
		t.Fatalf("err = %v, want ErrInsufficientFunds", err)
	}

	entries, _, err := store.LedgerPage(ctx, pool, id, "INR", "", 50)
	if err != nil {
		t.Fatalf("ledger page: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("ledger has %d entries after a refused charge, want 1", len(entries))
	}
	sum, err := store.LedgerSum(ctx, pool, id, "INR")
	if err != nil {
		t.Fatalf("ledger sum: %v", err)
	}
	if sum != 1_000 {
		t.Fatalf("ledger sum = %d, want 1000", sum)
	}
}

// The invariant that everything else rests on. Twenty concurrent charges
// against one wallet must leave sum(ledger) == balance exactly.
//
// Without SELECT ... FOR UPDATE this fails: two transactions read the same
// starting balance and the second overwrites the first, inventing money with
// no error raised anywhere.
func TestBalanceMatchesLedgerUnderConcurrentWrites(t *testing.T) {
	ctx, pool, id := walletFixture(t)

	if _, err := store.AppendLedgerEntry(ctx, pool, id, store.LedgerEntry{
		Currency: "INR", Type: "topup", AmountMinor: 1_000_000,
	}); err != nil {
		t.Fatalf("topup: %v", err)
	}

	const writers, each = 20, 1_000
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.AppendLedgerEntry(ctx, pool, id, store.LedgerEntry{
				Currency: "INR", Type: "charge", AmountMinor: each,
			}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent charge failed: %v", err)
	}

	balances, err := store.ListWalletBalances(ctx, pool, id)
	if err != nil {
		t.Fatalf("list balances: %v", err)
	}
	want := int64(1_000_000 - writers*each)
	if balances[0].BalanceMinor != want {
		t.Fatalf("balance = %d, want %d — concurrent writes lost updates",
			balances[0].BalanceMinor, want)
	}

	sum, err := store.LedgerSum(ctx, pool, id, "INR")
	if err != nil {
		t.Fatalf("ledger sum: %v", err)
	}
	if sum != balances[0].BalanceMinor {
		t.Fatalf("sum(ledger) = %d but balance = %d — the invariant is broken",
			sum, balances[0].BalanceMinor)
	}
}

func TestWalletsAreSeparatePerCurrency(t *testing.T) {
	ctx, pool, id := walletFixture(t)

	for _, currency := range []string{"INR", "USD"} {
		if _, err := store.AppendLedgerEntry(ctx, pool, id, store.LedgerEntry{
			Currency: currency, Type: "topup", AmountMinor: 5_000,
		}); err != nil {
			t.Fatalf("topup %s: %v", currency, err)
		}
	}
	if _, err := store.AppendLedgerEntry(ctx, pool, id, store.LedgerEntry{
		Currency: "INR", Type: "charge", AmountMinor: 2_000,
	}); err != nil {
		t.Fatalf("charge: %v", err)
	}

	balances, err := store.ListWalletBalances(ctx, pool, id)
	if err != nil {
		t.Fatalf("list balances: %v", err)
	}
	if len(balances) != 2 {
		t.Fatalf("got %d balances, want 2", len(balances))
	}
	byCurrency := map[string]int64{}
	for _, balance := range balances {
		byCurrency[balance.Currency] = balance.BalanceMinor
	}
	if byCurrency["INR"] != 3_000 || byCurrency["USD"] != 5_000 {
		t.Fatalf("balances = %v; charging INR should not touch USD", byCurrency)
	}
}

// Keyset pagination must return every entry exactly once. An off-by-one in the
// cursor comparison silently drops or repeats a row at each page boundary,
// which on a ledger means a customer's statement does not add up.
func TestLedgerPaginationCoversEveryEntryExactlyOnce(t *testing.T) {
	ctx, pool, id := walletFixture(t)

	const total = 25
	if _, err := store.AppendLedgerEntry(ctx, pool, id, store.LedgerEntry{
		Currency: "INR", Type: "topup", AmountMinor: 1_000_000,
	}); err != nil {
		t.Fatalf("topup: %v", err)
	}
	for i := range total - 1 {
		if _, err := store.AppendLedgerEntry(ctx, pool, id, store.LedgerEntry{
			Currency: "INR", Type: "charge", AmountMinor: int64(i + 1),
		}); err != nil {
			t.Fatalf("charge %d: %v", i, err)
		}
	}

	seen := map[uuid.UUID]int{}
	cursor := ""
	pages := 0
	for {
		entries, next, err := store.LedgerPage(ctx, pool, id, "INR", cursor, 7)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, entry := range entries {
			seen[entry.ID]++
		}
		pages++
		if next == "" {
			break
		}
		cursor = next
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}

	if len(seen) != total {
		t.Fatalf("saw %d distinct entries across %d pages, want %d", len(seen), pages, total)
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("entry %s appeared %d times", id, count)
		}
	}
}

func TestLedgerRejectsAMalformedCursor(t *testing.T) {
	ctx, pool, id := walletFixture(t)

	if _, _, err := store.LedgerPage(ctx, pool, id, "INR", "!!!not-base64!!!", 10); !errors.Is(err, store.ErrInvalidCursor) {
		t.Fatalf("err = %v, want ErrInvalidCursor", err)
	}
}
