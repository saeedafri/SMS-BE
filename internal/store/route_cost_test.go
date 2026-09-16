package store_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// Repricing a route in place. The endpoint that will call this is waiting on
// the contract; the rule it has to keep is the same either way — the price
// moves and the path does not.
func TestRepricingARouteChangesTheCostAndNothingElse(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool := appPool(t, ctx)

	created, err := store.CreateRoute(ctx, pool, store.Route{
		Country: "IN", Channel: "SMS", Carrier: "VIDEOCON",
		Label:              "Repricing test " + uuid.NewString()[:8],
		ComplianceStanding: "registered", CostPerSegmentMinor: 12, Currency: "INR",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM routes WHERE id = $1`, created.ID)
	})

	updated, err := store.UpdateRouteCost(ctx, pool, created.ID, 9)
	if err != nil {
		t.Fatalf("reprice: %v", err)
	}
	if updated.CostPerSegmentMinor != 9 {
		t.Errorf("cost = %d, want 9", updated.CostPerSegmentMinor)
	}
	// The path a message takes must be untouched: same corridor, same carrier,
	// same place in the ladder, same status. A reprice that moved any of these
	// would reroute live traffic.
	if updated.Country != created.Country || updated.Channel != created.Channel ||
		updated.Carrier != created.Carrier || updated.Priority != created.Priority ||
		updated.Status != created.Status || updated.Label != created.Label ||
		updated.Currency != created.Currency {
		t.Errorf("repricing moved the route:\n before %+v\n after  %+v", created, updated)
	}

	if _, err := store.UpdateRouteCost(ctx, pool, uuid.New(), 5); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("repricing a route that does not exist = %v, want ErrNotFound", err)
	}
}
