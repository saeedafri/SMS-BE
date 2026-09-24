package sending

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

func contactAt(always bool, lastSent *time.Time) store.Contact {
	return store.Contact{ID: uuid.New(), AlwaysSend: always, LastCappedSendAt: lastSent}
}

func plain(n int) []store.Contact {
	page := make([]store.Contact, n)
	for i := range page {
		page[i] = contactAt(false, nil)
	}
	return page
}

// The headline number: a hundred recipients at 70% is seventy.
func TestSeventyPercentOfAHundredIsSeventy(t *testing.T) {
	t.Parallel()
	send, withheld := chooseUnderCap(plain(100), 70)
	if len(send) != 70 || len(withheld) != 30 {
		t.Fatalf("sent %d and withheld %d, want 70 and 30", len(send), len(withheld))
	}
}

// The exemption is the whole reason the feature was rebuilt: fifteen contacts
// marked always_send go out inside a 70% cap, and they come out of the seventy
// rather than being added on top — the operator set "seventy", and a cap that
// quietly sends seventy-four is not the cap they set.
func TestTheExemptAreCarriedAndCountTowardTheCap(t *testing.T) {
	t.Parallel()
	page := plain(85)
	for range 15 {
		page = append(page, contactAt(true, nil))
	}

	send, withheld := chooseUnderCap(page, 70)
	if len(send) != 70 {
		t.Fatalf("sent %d of 100 at 70%%, want 70", len(send))
	}
	if len(withheld) != 30 {
		t.Fatalf("withheld %d, want 30", len(withheld))
	}
	exempt := 0
	for _, contact := range send {
		if contact.AlwaysSend {
			exempt++
		}
	}
	if exempt != 15 {
		t.Fatalf("%d of the 15 exempt contacts were carried, want all 15", exempt)
	}
	for _, contact := range withheld {
		if contact.AlwaysSend {
			t.Fatalf("an always_send contact was withheld; the exemption is not a preference")
		}
	}
}

// A guarantee a cap can override is not a guarantee. Twenty exempt contacts
// inside a 10% cap all go, even though that is double what the cap allows.
func TestTheExemptGoEvenWhenTheyExceedTheCap(t *testing.T) {
	t.Parallel()
	page := plain(80)
	for range 20 {
		page = append(page, contactAt(true, nil))
	}

	send, withheld := chooseUnderCap(page, 10)
	if len(send) != 20 {
		t.Fatalf("sent %d, want all 20 exempt — 10%% of 100 is 10, and the exemption wins",
			len(send))
	}
	for _, contact := range send {
		if !contact.AlwaysSend {
			t.Fatalf("a non-exempt contact was carried while the cap was already over")
		}
	}
	if len(withheld) != 80 {
		t.Fatalf("withheld %d, want the 80 non-exempt", len(withheld))
	}
}

// Whoever was carried last time goes to the back of the queue this time.
// Without this the same tail of the list is cut on every send and never
// receives anything at all — invisible on every report, and the thing a
// customer eventually notices.
func TestTheLeastRecentlyCarriedGoFirst(t *testing.T) {
	t.Parallel()
	recent := time.Now().Add(-time.Hour)
	older := time.Now().Add(-72 * time.Hour)

	page := []store.Contact{
		contactAt(false, &recent),
		contactAt(false, &older),
		contactAt(false, nil), // never carried
		contactAt(false, &recent),
	}
	send, withheld := chooseUnderCap(page, 50)
	if len(send) != 2 {
		t.Fatalf("sent %d of 4 at 50%%, want 2", len(send))
	}
	if send[0].LastCappedSendAt != nil {
		t.Errorf("the contact never carried before was not chosen first")
	}
	if send[1].LastCappedSendAt == nil || !send[1].LastCappedSendAt.Equal(older) {
		t.Errorf("the longest-waiting contact was not chosen second")
	}
	for _, contact := range withheld {
		if contact.LastCappedSendAt == nil || !contact.LastCappedSendAt.Equal(recent) {
			t.Errorf("a contact was cut ahead of one carried more recently")
		}
	}
}

// 100% is not a cap, and an empty page is not a crash.
func TestAFullShareCarriesEveryone(t *testing.T) {
	t.Parallel()
	send, withheld := chooseUnderCap(plain(7), 100)
	if len(send) != 7 || len(withheld) != 0 {
		t.Fatalf("sent %d withheld %d at 100%%, want 7 and 0", len(send), len(withheld))
	}
	if send, withheld := chooseUnderCap(nil, 70); len(send) != 0 || len(withheld) != 0 {
		t.Fatalf("an empty page produced %d and %d", len(send), len(withheld))
	}
}

// Zero means send nothing — except to the exempt, who are exempt from zero too.
func TestAZeroShareStillCarriesTheExempt(t *testing.T) {
	t.Parallel()
	page := append(plain(9), contactAt(true, nil))
	send, withheld := chooseUnderCap(page, 0)
	if len(send) != 1 || !send[0].AlwaysSend {
		t.Fatalf("sent %d at 0%%, want only the one exempt contact", len(send))
	}
	if len(withheld) != 9 {
		t.Fatalf("withheld %d, want 9", len(withheld))
	}
}

// Rounded up, so a long list does not drift materially below the share the
// operator actually set.
func TestTheShareRoundsUp(t *testing.T) {
	t.Parallel()
	send, _ := chooseUnderCap(plain(3), 70)
	if len(send) != 3 {
		t.Fatalf("70%% of 3 carried %d, want 3 — 2.1 rounds up", len(send))
	}
}

// The exemption belongs to the contact, not to the path that reached them.
// A journey step is one contact at a time, so there is no share to take — but a
// contact no cap may withhold must not be held by the DAY's ceiling either.
// Harder to notice than a campaign skipping them, because nobody watches a
// journey's recipient count.
func TestTheExemptionIsAboutTheContactNotThePath(t *testing.T) {
	t.Parallel()
	exempt := store.Contact{ID: uuid.New(), AlwaysSend: true}
	ordinary := store.Contact{ID: uuid.New()}

	// The share path: a page of one exempt contact under a 0% cap still carries.
	if send, _ := chooseUnderCap([]store.Contact{exempt}, 0); len(send) != 1 {
		t.Fatalf("a single exempt contact was withheld at 0%%")
	}
	if send, _ := chooseUnderCap([]store.Contact{ordinary}, 0); len(send) != 0 {
		t.Fatalf("an ordinary contact was carried at 0%%")
	}
}
