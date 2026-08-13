package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/verify"
	"github.com/saeedafri/sms-be/internal/store"
)

// The attempt limit is the only thing standing between an attacker and a
// six-digit code. This exists because it was broken once in exactly the way a
// passing eyeball test would not catch: the wrong guess was applied, the
// response looked right, and the increment was silently rolled back because the
// outcome error was returned from inside the transaction. Every guess was
// therefore "the first guess" and the limit was never reached.
func TestWrongGuessesAreCountedAndTheLimitIsReached(t *testing.T) {
	pgURL, adminURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_DATABASE_ADMIN_URL")
	if pgURL == "" || adminURL == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DATABASE_ADMIN_URL not set")
	}
	ctx := context.Background()

	pool, err := store.Open(ctx, pgURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	admin, err := store.Open(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	t.Cleanup(admin.Close)

	tenantID := uuid.New()
	if _, err := admin.Exec(ctx,
		`INSERT INTO tenants (id, name, country) VALUES ($1, 'Verify Co', 'IN')`,
		tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	identity := store.Identity{TenantID: tenantID}
	service, err := store.CreateVerifyService(ctx, pool, identity, store.VerifyService{
		Name: "Login", CodeLength: 6, CodeTTLSeconds: 300, MaxAttempts: 3,
		MaxPerPhone: 5, WindowSeconds: 3600, CooldownSeconds: 60,
		Channels: []store.VerifyChannelConfig{{Channel: "SMS", Body: "Code {{code}}"}},
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	created, err := store.CreateVerification(ctx, pool, identity, store.Verification{
		ServiceID: service.ID, Msisdn: "+919876500123", Channel: "SMS",
		CodeHash: verify.HashCode("424242"), MaxAttempts: 3, Currency: "INR",
		ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create verification: %v", err)
	}

	// decide mirrors the handler's rules closely enough to prove the counting.
	decide := func(guess string) func(store.Verification) (string, int, error) {
		return func(current store.Verification) (string, int, error) {
			switch current.Status {
			case "verified", "locked", "expired":
				return current.Status, current.AttemptsUsed, verify.ErrLocked
			}
			attempts := current.AttemptsUsed + 1
			if verify.CodeMatches(current.CodeHash, guess) {
				return "verified", attempts, nil
			}
			if attempts >= current.MaxAttempts {
				return "locked", attempts, verify.ErrLocked
			}
			return "incorrect", attempts, verify.ErrIncorrect
		}
	}

	wantStatus := []string{"incorrect", "incorrect", "locked"}
	for attempt, want := range wantStatus {
		result, err := store.CheckVerification(ctx, pool, identity, created.ID, decide("000000"))
		if err == nil {
			t.Fatalf("guess %d: a wrong code reported success", attempt+1)
		}
		if result.Status != want {
			t.Fatalf("guess %d: status = %q, want %q", attempt+1, result.Status, want)
		}
		// The load-bearing assertion: each wrong guess must be REMEMBERED.
		if result.AttemptsUsed != attempt+1 {
			t.Fatalf("guess %d: attemptsUsed = %d, want %d — a wrong guess was not "+
				"persisted, so the attempt limit can never be reached and the code "+
				"can be brute-forced", attempt+1, result.AttemptsUsed, attempt+1)
		}
	}

	// Locked means locked. If the right code still works after the budget is
	// spent, the limit was decoration.
	result, err := store.CheckVerification(ctx, pool, identity, created.ID, decide("424242"))
	if err == nil {
		t.Fatal("the correct code was accepted after the attempt limit was reached")
	}
	if result.Status != "locked" {
		t.Fatalf("status = %q after lockout, want locked", result.Status)
	}
}

// A wrong guess must not end the verification: the user gets to try again, and
// only the budget running out stops them.
func TestARetryAfterOneWrongGuessCanStillSucceed(t *testing.T) {
	pgURL, adminURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_DATABASE_ADMIN_URL")
	if pgURL == "" || adminURL == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DATABASE_ADMIN_URL not set")
	}
	ctx := context.Background()

	pool, err := store.Open(ctx, pgURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	admin, err := store.Open(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	t.Cleanup(admin.Close)

	tenantID := uuid.New()
	if _, err := admin.Exec(ctx,
		`INSERT INTO tenants (id, name, country) VALUES ($1, 'Retry Co', 'IN')`,
		tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	identity := store.Identity{TenantID: tenantID}
	service, err := store.CreateVerifyService(ctx, pool, identity, store.VerifyService{
		Name: "Login", CodeLength: 6, CodeTTLSeconds: 300, MaxAttempts: 3,
		MaxPerPhone: 5, WindowSeconds: 3600, CooldownSeconds: 60,
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	created, err := store.CreateVerification(ctx, pool, identity, store.Verification{
		ServiceID: service.ID, Msisdn: "+919876500124", Channel: "SMS",
		CodeHash: verify.HashCode("135790"), MaxAttempts: 3, Currency: "INR",
		ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create verification: %v", err)
	}

	decide := func(guess string) func(store.Verification) (string, int, error) {
		return func(current store.Verification) (string, int, error) {
			switch current.Status {
			case "verified", "locked", "expired":
				return current.Status, current.AttemptsUsed, verify.ErrLocked
			}
			attempts := current.AttemptsUsed + 1
			if verify.CodeMatches(current.CodeHash, guess) {
				return "verified", attempts, nil
			}
			if attempts >= current.MaxAttempts {
				return "locked", attempts, verify.ErrLocked
			}
			return "incorrect", attempts, verify.ErrIncorrect
		}
	}

	if _, err := store.CheckVerification(ctx, pool, identity, created.ID, decide("999999")); err == nil {
		t.Fatal("a wrong code reported success")
	}
	result, err := store.CheckVerification(ctx, pool, identity, created.ID, decide("135790"))
	if err != nil {
		t.Fatalf("the correct code was refused on a legitimate retry: %v", err)
	}
	if result.Status != "verified" {
		t.Fatalf("status = %q, want verified", result.Status)
	}
}
