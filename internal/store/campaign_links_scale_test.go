package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// A13: link creation for a 50,000-recipient send, the way the sender does it —
// one CampaignLinks call per 500-recipient page — and then the whole send again,
// which is what a retry or a resume costs. Off unless LINK_SCALE=1: it writes
// 50,000 rows, which the ordinary suite has no reason to.
func TestLinkCreationAtScale(t *testing.T) {
	if os.Getenv("LINK_SCALE") != "1" {
		t.Skip("LINK_SCALE=1 to run")
	}
	ctx := context.Background()
	tenant := uuid.New()
	seedTenants(t, ctx, tenant)
	pool := appPool(t, ctx)
	identity := store.Identity{TenantID: tenant}
	campaign := uuid.New()
	spec := &store.LinkSpec{Destination: "https://shop.example.org/sale"}

	const total, page = 50_000, 500
	people := make([]string, total)
	for i := range people {
		people[i] = fmt.Sprintf("+9198%08d", i)
	}
	run := func() time.Duration {
		start := time.Now()
		for from := 0; from < total; from += page {
			links, err := store.CampaignLinks(ctx, pool, identity, campaign, people[from:from+page], spec)
			if err != nil {
				t.Fatalf("page at %d: %v", from, err)
			}
			if len(links) != page {
				t.Fatalf("page at %d made %d links, want %d", from, len(links), page)
			}
		}
		return time.Since(start)
	}
	made := run()
	again := run()
	if n := countLinks(t, ctx, identity, campaign); n != total {
		t.Fatalf("%d links, want %d", n, total)
	}
	t.Logf("A13: %d links in %d pages of %d: made in %s (%.0f/s); the same send again in %s",
		total, total/page, page, made.Round(time.Millisecond), float64(total)/made.Seconds(),
		again.Round(time.Millisecond))
}
