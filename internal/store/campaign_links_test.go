package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/saeedafri/sms-be/internal/store"
)

func countLinks(t *testing.T, ctx context.Context, identity store.Identity, campaignID uuid.UUID) int {
	t.Helper()
	pool := appPool(t, ctx)
	var n int
	if err := store.WithTenant(ctx, pool, identity.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM short_links WHERE campaign_id = $1`,
			campaignID).Scan(&n)
	}); err != nil {
		t.Fatalf("count links: %v", err)
	}
	return n
}

// One code per person per campaign: two people get two codes, and asking again
// — a retry, a resume, the fallback leg — gets the same two back, not two more.
func TestCampaignLinksAreOnePerPersonAndStable(t *testing.T) {
	ctx := context.Background()
	tenant := uuid.New()
	seedTenants(t, ctx, tenant)
	pool := appPool(t, ctx)
	identity := store.Identity{TenantID: tenant}
	campaign := uuid.New()
	spec := &store.LinkSpec{Destination: "https://shop.example.org/sale"}
	people := []string{"+919810000001", "+919810000002"}

	first, err := store.CampaignLinks(ctx, pool, identity, campaign, people, spec)
	if err != nil {
		t.Fatalf("make links: %v", err)
	}
	a, b := first[people[0]], first[people[1]]
	if len(a.Code) != 8 || len(b.Code) != 8 || a.Code == b.Code {
		t.Fatalf("codes = %q and %q, want two different 8-character codes", a.Code, b.Code)
	}
	if a.Destination != spec.Destination {
		t.Errorf("destination = %q", a.Destination)
	}

	again, err := store.CampaignLinks(ctx, pool, identity, campaign,
		[]string{people[1], people[0], people[0]}, spec)
	if err != nil {
		t.Fatalf("ask again: %v", err)
	}
	if again[people[0]].Code != a.Code || again[people[1]].Code != b.Code {
		t.Errorf("second ask = %v, want the first codes back", again)
	}
	if n := countLinks(t, ctx, identity, campaign); n != 2 {
		t.Errorf("%d links for two people, want 2", n)
	}

	// Without a spec nothing is made: what exists is all there is.
	read, err := store.CampaignLinks(ctx, pool, identity, campaign,
		[]string{people[0], "+919810000003"}, nil)
	if err != nil {
		t.Fatalf("read only: %v", err)
	}
	if _, made := read["+919810000003"]; made || read[people[0]].Code != a.Code {
		t.Errorf("read-only ask = %v, want only the existing link", read)
	}
}

// A link the customer made through bulk for this campaign and this person is
// the one they get, with ITS destination: the campaign's is the default for
// people who have none (the founder's decision, 22 October).
func TestAPreMadeLinkWinsWithItsOwnDestination(t *testing.T) {
	ctx := context.Background()
	tenant := uuid.New()
	seedTenants(t, ctx, tenant)
	pool := appPool(t, ctx)
	identity := store.Identity{TenantID: tenant}
	campaign := uuid.New()
	person := "+919810000011"

	made, err := store.CreateShortLinks(ctx, pool, identity, []store.ShortLink{{
		Destination: "https://shop.example.org/vip", CampaignID: &campaign, Recipient: &person}})
	if err != nil {
		t.Fatalf("pre-make: %v", err)
	}
	links, err := store.CampaignLinks(ctx, pool, identity, campaign,
		[]string{person, "+919810000012"},
		&store.LinkSpec{Destination: "https://shop.example.org/everyone"})
	if err != nil {
		t.Fatalf("campaign links: %v", err)
	}
	if got := links[person]; got.Code != made[0].Code || got.Destination != "https://shop.example.org/vip" {
		t.Errorf("pre-made person got %+v, want the pre-made link and its own destination", got)
	}
	if got := links["+919810000012"]; got.Destination != "https://shop.example.org/everyone" {
		t.Errorf("person with none got %+v, want the campaign's destination", got)
	}
	if n := countLinks(t, ctx, identity, campaign); n != 2 {
		t.Errorf("%d links, want the pre-made one plus one new", n)
	}
}

// Bulk is untouched: calling it twice for the same campaign and person still
// makes two sets, exactly as before the sender started making links.
func TestBulkStillMakesASecondSet(t *testing.T) {
	ctx := context.Background()
	tenant := uuid.New()
	seedTenants(t, ctx, tenant)
	pool := appPool(t, ctx)
	identity := store.Identity{TenantID: tenant}
	campaign := uuid.New()
	person := "+919810000021"
	link := store.ShortLink{Destination: "https://shop.example.org/", CampaignID: &campaign, Recipient: &person}
	for range 2 {
		if _, err := store.CreateShortLinks(ctx, pool, identity, []store.ShortLink{link}); err != nil {
			t.Fatalf("bulk: %v", err)
		}
	}
	if n := countLinks(t, ctx, identity, campaign); n != 2 {
		t.Errorf("%d links after two bulk calls, want 2", n)
	}
}

// Two senders asking for the same people at once — the primary leg and an
// after-failure fallback, two API instances — still make one link each. The
// read-then-insert alone cannot promise that; the unique index on sender-made
// links is what does.
func TestConcurrentAsksStillMakeOneLinkPerPerson(t *testing.T) {
	ctx := context.Background()
	tenant := uuid.New()
	seedTenants(t, ctx, tenant)
	pool := appPool(t, ctx)
	identity := store.Identity{TenantID: tenant}
	campaign := uuid.New()
	people := make([]string, 50)
	for i := range people {
		people[i] = "+9198100" + fmt.Sprintf("%05d", i)
	}
	spec := &store.LinkSpec{Destination: "https://shop.example.org/"}
	results := make(chan map[string]store.ShortLink, 8)
	errs := make(chan error, 8)
	for range 8 {
		go func() {
			links, err := store.CampaignLinks(ctx, pool, identity, campaign, people, spec)
			errs <- err
			results <- links
		}()
	}
	var first map[string]store.ShortLink
	for range 8 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent ask: %v", err)
		}
		links := <-results
		if first == nil {
			first = links
		}
		for _, person := range people {
			if links[person].Code != first[person].Code {
				t.Fatalf("%s got %s and %s from two concurrent asks", person,
					links[person].Code, first[person].Code)
			}
		}
	}
	if n := countLinks(t, ctx, identity, campaign); n != len(people) {
		t.Errorf("%d links for %d people after 8 concurrent asks", n, len(people))
	}
}

// Once a person has been sent a link, a later bulk call for them does not
// change what a retry sends: the oldest link is theirs.
func TestALinkAlreadySentIsNotDisplacedByALaterOne(t *testing.T) {
	ctx := context.Background()
	tenant := uuid.New()
	seedTenants(t, ctx, tenant)
	pool := appPool(t, ctx)
	identity := store.Identity{TenantID: tenant}
	campaign := uuid.New()
	person := "+919810000041"
	spec := &store.LinkSpec{Destination: "https://shop.example.org/"}
	sent, err := store.CampaignLinks(ctx, pool, identity, campaign, []string{person}, spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateShortLinks(ctx, pool, identity, []store.ShortLink{{
		Destination: "https://shop.example.org/later", CampaignID: &campaign, Recipient: &person}}); err != nil {
		t.Fatal(err)
	}
	retry, err := store.CampaignLinks(ctx, pool, identity, campaign, []string{person}, spec)
	if err != nil {
		t.Fatal(err)
	}
	if retry[person].Code != sent[person].Code {
		t.Errorf("retry got %s, want the link already sent, %s", retry[person].Code, sent[person].Code)
	}
}
