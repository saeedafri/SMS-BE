package demoseed_test

import (
	"context"
	"os"
	"testing"

	"github.com/saeedafri/sms-be/internal/demoseed"
	"github.com/saeedafri/sms-be/internal/store"
)

// The seed must leave the routes table populated.
//
// It clears routes and re-inserts them in two statements. When the INSERT is
// refused, the DELETE has already committed, so the table is not left as it was
// — it is left EMPTY, and an empty routes table means no corridor has a carrier
// path and no tenant can send anything at all.
//
// That is not hypothetical. Migration 00039 replaced the old per-corridor
// uniqueness with UNIQUE (country, channel, priority), and the fixture rows
// still gave three IN/SMS routes priority 1. Every call to the seed — including
// the /v1/dev/reset-mock-state hook the browser suite calls between specs —
// wiped the routes of whatever database it was pointed at and restored nothing.
// The console showed "No routes match these filters" and sending was dead.
//
// This asserts the outcome rather than the constraint, so it still holds if the
// uniqueness rule changes again: whatever the rule is, the seed's data must
// satisfy it.
func TestSeedLeavesEveryCorridorRoutable(t *testing.T) {
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

	if err := demoseed.ApplyFixtureOnly(ctx, admin); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var routes int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM routes`).Scan(&routes); err != nil {
		t.Fatalf("count routes: %v", err)
	}
	if routes == 0 {
		t.Fatal("the seed left no routes at all — every corridor is unroutable " +
			"and no tenant can send")
	}

	// Gap-free as well as distinct. The console's move-up and move-down swap a
	// route with priority-1 or priority+1, so a hole in the sequence makes those
	// controls target a row that is not there.
	rows, err := admin.Query(ctx, `
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
