package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// A tenant with no ceiling is uncapped, and that is the default. The whole
// design rests on the absence of a cap meaning "send everything" — if a missing
// column ever read as zero, every customer we have not touched would silently
// stop sending.
func TestATenantWithNoCeilingMaySendEverything(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tenant := uuid.New()
	seedTenants(t, ctx, tenant)
	pool := appPool(t, ctx)

	allowance, err := store.ReadSendAllowance(ctx, pool,
		store.Identity{TenantID: tenant, Country: "IN"}, time.Now())
	if err != nil {
		t.Fatalf("read allowance: %v", err)
	}
	if allowance.Capped() {
		t.Fatalf("a tenant nobody capped reads as capped: %+v", allowance)
	}
	if room := allowance.Room(100_000); room != 100_000 {
		t.Fatalf("uncapped tenant got room %d for 100000", room)
	}
}

// The ceiling clips the ask rather than refusing it, and it clips by what is
// left rather than by the whole cap — a tenant who has already sent 50,000 of
// 70,000 gets 20,000, not 70,000 again.
func TestACeilingAdmitsWhatIsLeftOfTheDay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tenant := uuid.New()
	admin := seedTenants(t, ctx, tenant)
	pool := appPool(t, ctx)
	identity := store.Identity{TenantID: tenant, Country: "IN"}

	if _, err := admin.Exec(ctx,
		`UPDATE tenants SET send_cap_per_day = 70000 WHERE id = $1`, tenant); err != nil {
		t.Fatalf("set cap: %v", err)
	}

	roomNow := func() int {
		t.Helper()
		allowance, err := store.ReadSendAllowance(ctx, pool, identity, time.Now())
		if err != nil {
			t.Fatalf("read allowance: %v", err)
		}
		if !allowance.Capped() {
			t.Fatalf("a capped tenant reads as uncapped: %+v", allowance)
		}
		return allowance.Room(100_000)
	}

	if room := roomNow(); room != 70_000 {
		t.Fatalf("fresh day: room %d, want 70000", room)
	}

	day := store.SendDay("IN", time.Now())
	if err := store.RecordSendUsage(ctx, pool, identity, day, 50_000, 0); err != nil {
		t.Fatalf("record usage: %v", err)
	}
	if room := roomNow(); room != 20_000 {
		t.Fatalf("after 50000 sent: room %d, want 20000", room)
	}

	// Two writes ADD. A record that replaced would let a campaign's second page
	// erase the first page's usage, and the ceiling would never arrive.
	if err := store.RecordSendUsage(ctx, pool, identity, day, 20_000, 0); err != nil {
		t.Fatalf("record usage: %v", err)
	}
	if room := roomNow(); room != 0 {
		t.Fatalf("after the cap is spent: room %d, want 0", room)
	}
}

// What the ceiling took off is recorded, because a withheld recipient has no
// message row by design — so if this number is not kept here it cannot be
// recovered from anywhere, and "what did the cap cost this customer" becomes
// unanswerable on the day somebody asks.
func TestWhatTheCeilingWithheldIsWrittenDown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tenant := uuid.New()
	admin := seedTenants(t, ctx, tenant)
	pool := appPool(t, ctx)
	identity := store.Identity{TenantID: tenant, Country: "IN"}

	day := store.SendDay("IN", time.Now())
	if err := store.RecordSendUsage(ctx, pool, identity, day, 70_000, 30_000); err != nil {
		t.Fatalf("record usage: %v", err)
	}
	accepted, withheld, err := store.ReadSendUsage(ctx, admin, tenant, day)
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if accepted != 70_000 || withheld != 30_000 {
		t.Fatalf("usage reads %d accepted / %d withheld, want 70000 / 30000", accepted, withheld)
	}
}

// The day a tenant's allowance resets is THEIR midnight, not UTC's.
//
// 20:00 UTC is 01:30 the next morning in Kolkata. A UTC day would hand an
// Indian tenant a fresh allowance at 05:30 in the morning and count their
// evening against the following day — neither of which is a boundary any
// customer would recognise, and both of which make the operator console
// disagree with the customer about what happened on Tuesday.
func TestADayEndsAtTheTenantsOwnMidnight(t *testing.T) {
	t.Parallel()
	evening := time.Date(2026, 9, 22, 20, 0, 0, 0, time.UTC)

	india := store.SendDay("IN", evening)
	if got := india.Format("2006-01-02"); got != "2026-09-23" {
		t.Fatalf("20:00 UTC on 22 Sep is 01:30 IST on the 23rd; SendDay said %s", got)
	}
	if utc := store.SendDay("US", evening); utc.Format("2006-01-02") != "2026-09-22" {
		t.Fatalf("the same instant is still the 22nd in New York, got %s",
			utc.Format("2006-01-02"))
	}
}

// Zero is a real ceiling and means "send nothing". It has to be told apart from
// "no ceiling", which is the same absence expressed as NULL — collapsing the
// two is how an operator stopping one tenant's bulk traffic would instead
// uncap them.
func TestAZeroCeilingIsNotTheSameAsNoCeiling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tenant := uuid.New()
	admin := seedTenants(t, ctx, tenant)
	pool := appPool(t, ctx)

	if _, err := admin.Exec(ctx,
		`UPDATE tenants SET send_cap_per_day = 0 WHERE id = $1`, tenant); err != nil {
		t.Fatalf("set cap: %v", err)
	}
	allowance, err := store.ReadSendAllowance(ctx, pool,
		store.Identity{TenantID: tenant, Country: "IN"}, time.Now())
	if err != nil {
		t.Fatalf("read allowance: %v", err)
	}
	if !allowance.Capped() {
		t.Fatalf("a zero ceiling read as no ceiling at all: %+v", allowance)
	}
	if room := allowance.Room(1); room != 0 {
		t.Fatalf("a zero ceiling admitted %d", room)
	}
}
