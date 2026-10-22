package store

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
)

// A fresh code that happens to be taken by another link is retried with a new
// one, not dropped and not an error. 55^8 makes it rare; a batch of a million
// makes rare happen.
func TestACodeCollisionIsRetriedNotLost(t *testing.T) {
	adminURL, appURL := os.Getenv("TEST_DATABASE_ADMIN_URL"), os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DATABASE_ADMIN_URL not set")
	}
	ctx := context.Background()
	admin, err := Open(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	pool, err := Open(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tenant := uuid.New()
	if _, err := admin.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'collide')`, tenant); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant)
	identity := Identity{TenantID: tenant}

	taken, err := CreateShortLinks(ctx, pool, identity, []ShortLink{{Destination: "https://a.example/"}})
	if err != nil {
		t.Fatal(err)
	}
	// The first two codes drawn are the taken one; after that, random.
	drawn := 0
	linkCode = func() string {
		drawn++
		if drawn <= 2 {
			return taken[0].Code
		}
		return NewLinkCode()
	}
	defer func() { linkCode = NewLinkCode }()

	campaign := uuid.New()
	people := []string{"+919810000031", "+919810000032"}
	links, err := CampaignLinks(ctx, pool, identity, campaign, people, &LinkSpec{Destination: "https://b.example/"})
	if err != nil {
		t.Fatalf("campaign links: %v", err)
	}
	for _, person := range people {
		got := links[person]
		if got.Code == "" || got.Code == taken[0].Code || got.Destination != "https://b.example/" {
			t.Errorf("%s got %+v, want a fresh link of its own", person, got)
		}
	}
}
