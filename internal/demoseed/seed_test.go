package demoseed

import (
	"context"
	"os"
	"testing"

	"github.com/saeedafri/sms-be/internal/store"
)

// The seed's route rows must satisfy whatever uniqueness the schema enforces.
//
// The rebuild clears routes and re-inserts them. When the INSERT is refused the
// DELETE has already happened, so the table is not left as it was — it is left
// EMPTY, and an empty routes table means no corridor has a carrier path and no
// tenant can send anything at all.
//
// That is not hypothetical. Migration 00039 replaced the old per-corridor
// uniqueness with UNIQUE (country, channel, priority), and the fixture rows
// still gave three IN/SMS routes priority 1. Every call to the seed — including
// the /v1/dev/reset-mock-state hook the browser suite calls between specs —
// wiped the routes of whatever database it was pointed at and restored nothing.
// The console showed "No routes match these filters" and sending was dead.
//
// Everything runs inside a transaction that is rolled back. routes is a global
// table with no tenant scope, so a test that seeded it for real broke two
// others in other packages that were inserting routes of their own at the same
// time — the suite runs packages concurrently against one database.
func TestSeedRoutesSatisfyTheSchema(t *testing.T) {
	adminURL := os.Getenv("TEST_DATABASE_ADMIN_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_ADMIN_URL not set")
	}
	ctx := context.Background()

	admin, err := store.Open(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	t.Cleanup(admin.Close)

	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	if err := rebuildRoutes(ctx, tx); err != nil {
		t.Fatalf("the seed cannot lay down its own routes, so every call to it "+
			"empties the table: %v", err)
	}

	var routes int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM routes`).Scan(&routes); err != nil {
		t.Fatalf("count routes: %v", err)
	}
	if routes == 0 {
		t.Fatal("the seed left no routes at all — every corridor is unroutable")
	}

	// Gap-free as well as distinct. The console's move-up and move-down swap a
	// route with priority-1 or priority+1, so a hole in the sequence makes those
	// controls target a row that is not there.
	rows, err := tx.Query(ctx, `
		SELECT country, channel, count(*), count(DISTINCT priority),
		       min(priority), max(priority)
		  FROM routes GROUP BY country, channel ORDER BY country, channel`)
	if err != nil {
		t.Fatalf("group routes: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var country, channel string
		var total, distinct, lowest, highest int
		if err := rows.Scan(&country, &channel, &total, &distinct, &lowest, &highest); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if distinct != total {
			t.Errorf("%s/%s has %d routes sharing %d priorities — the unique "+
				"constraint would refuse this", country, channel, total, distinct)
		}
		if lowest != 1 || highest != total {
			t.Errorf("%s/%s priorities run %d..%d across %d routes, want 1..%d",
				country, channel, lowest, highest, total, total)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("group routes: %v", err)
	}
}

// The two console specs that move Jio Direct down expect Jio via Aggregator A
// to be the row directly beneath it. routes-state.ts still carries the
// pre-00039 numbering, so the ORDER is what ports across from the mock, not the
// numbers — and nothing else would catch a renumbering that quietly separated
// those two rows.
func TestJioDirectSitsDirectlyAboveItsAggregatorRoute(t *testing.T) {
	adminURL := os.Getenv("TEST_DATABASE_ADMIN_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_ADMIN_URL not set")
	}
	ctx := context.Background()

	admin, err := store.Open(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	t.Cleanup(admin.Close)

	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	if err := rebuildRoutes(ctx, tx); err != nil {
		t.Fatalf("rebuild routes: %v", err)
	}

	var direct, aggregator int
	if err := tx.QueryRow(ctx, `
		SELECT
			max(priority) FILTER (WHERE label = 'Jio Direct'),
			max(priority) FILTER (WHERE label = 'Jio via Aggregator A')
		FROM routes WHERE country = 'IN' AND channel = 'SMS'`).
		Scan(&direct, &aggregator); err != nil {
		t.Fatalf("read IN/SMS ladder: %v", err)
	}
	if direct != 1 {
		t.Errorf("Jio Direct is priority %d, want 1 — the console spec asserts "+
			"its Move up button is disabled", direct)
	}
	if aggregator != direct+1 {
		t.Errorf("Jio via Aggregator A is priority %d, want %d — moving Jio "+
			"Direct down must swap it with that row", aggregator, direct+1)
	}
}
