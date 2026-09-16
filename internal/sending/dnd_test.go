package sending_test

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/sending"
	"github.com/saeedafri/sms-be/internal/store"
)

// Ask 45: a promotional SMS to an Indian number is checked against the DND
// register first, and refused when the register blocks it or cannot be reached.

// fakeRegister counts lookups and answers what the test set.
type fakeRegister struct {
	lookups    atomic.Int32
	preference sending.DNDPreference
	err        error
}

func (f *fakeRegister) Lookup(context.Context, string) (sending.DNDPreference, error) {
	f.lookups.Add(1)
	return f.preference, f.err
}

// dndTemplate gives the fixture a template in one DLT category and sends with it.
func (f *fixture) sendWithCategory(t *testing.T, category, msisdn string) sending.SendResult {
	t.Helper()
	admin, err := store.Open(context.Background(), os.Getenv("TEST_DATABASE_ADMIN_URL"))
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	t.Cleanup(admin.Close)
	templateID := uuid.New()
	if _, err := admin.Exec(context.Background(), `
		INSERT INTO templates (id, tenant_id, sender_id, name, channel, country, body, status, dlt_category)
		VALUES ($1, $2, $3, $4, 'SMS', 'IN', '{{message}}', 'approved', $5)`,
		templateID, f.identity.TenantID, f.senderID, "DND "+templateID.String()[:8], category); err != nil {
		t.Fatalf("seed %s template: %v", category, err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DELETE FROM templates WHERE id = $1`, templateID)
	})
	result, err := f.service.Send(context.Background(), f.identity, sending.SendRequest{
		SenderID: f.senderID, TemplateID: &templateID, Msisdn: msisdn, Body: "Big sale today."})
	if err != nil && result.Status == "" {
		t.Fatalf("send: %v", err)
	}
	return result
}

// middayIST puts the fixture's clock inside India's promotional window, so
// these tests are about the register and not about the hour they run at.
func (f *fixture) middayIST() {
	ist := time.FixedZone("IST", 5*3600+1800)
	noon := time.Date(2026, 9, 16, 12, 0, 0, 0, ist)
	f.service.Now = func() time.Time { return noon }
}

func refused(t *testing.T, result sending.SendResult, code string) {
	t.Helper()
	if result.Status != "rejected" || result.FailureCode != code || result.CostMinor != 0 {
		t.Fatalf("result = %+v, want rejected %s at cost 0", result, code)
	}
}

// 1 and 9: with no register, an Indian promotional SMS is refused and no money moves.
func TestAPromotionalSMSToAnIndianNumberWithNoRegisterIsRefused(t *testing.T) {
	f := newFixture(t)
	f.middayIST()
	before := f.balance()
	refused(t, f.sendWithCategory(t, "PROMOTIONAL", "9876543210"), "dnd_check_unavailable")
	if after := f.balance(); after != before {
		t.Fatalf("balance moved from %d to %d on a refusal", before, after)
	}
}

// 2: transactional and service traffic never looks up.
func TestNonPromotionalTrafficNeverLooksUp(t *testing.T) {
	f := newFixture(t)
	f.middayIST()
	register := &fakeRegister{preference: sending.DNDPreference{FullyBlocked: true}}
	f.service.DND = register
	for _, category := range []string{"TRANSACTIONAL", "SERVICE_IMPLICIT", "SERVICE_EXPLICIT"} {
		if result := f.sendWithCategory(t, category, "9876543210"); result.Status != "sent" {
			t.Fatalf("%s = %+v, want sent", category, result)
		}
	}
	if n := register.lookups.Load(); n != 0 {
		t.Fatalf("%d DND lookups for non-promotional traffic, want 0", n)
	}
}

// 3: a non-Indian number never looks up.
func TestAPromotionalSMSToANonIndianNumberNeverLooksUp(t *testing.T) {
	f := newFixture(t)
	f.middayIST()
	register := &fakeRegister{preference: sending.DNDPreference{FullyBlocked: true}}
	f.service.DND = register
	f.sendWithCategory(t, "PROMOTIONAL", "+14155550123")
	if n := register.lookups.Load(); n != 0 {
		t.Fatalf("%d DND lookups for a US number, want 0", n)
	}
}

// 4 and 5: fully blocked, and a single blocked category, both refuse dnd_blocked.
func TestABlockedNumberIsRefusedDndBlocked(t *testing.T) {
	f := newFixture(t)
	f.middayIST()
	f.service.DND = &fakeRegister{preference: sending.DNDPreference{FullyBlocked: true}}
	refused(t, f.sendWithCategory(t, "PROMOTIONAL", "9876543211"), "dnd_blocked")
}

func TestAPartialCategoryBlockCountsAsBlocked(t *testing.T) {
	f := newFixture(t)
	f.middayIST()
	f.service.DND = &fakeRegister{preference: sending.DNDPreference{BlockedCategories: []string{"3"}}}
	refused(t, f.sendWithCategory(t, "PROMOTIONAL", "9876543212"), "dnd_blocked")
}

// 6: an unblocked number sends as before.
func TestAnUnblockedNumberIsSent(t *testing.T) {
	f := newFixture(t)
	f.middayIST()
	f.service.DND = &fakeRegister{}
	if result := f.sendWithCategory(t, "PROMOTIONAL", "9876543213"); result.Status != "sent" {
		t.Fatalf("result = %+v, want sent", result)
	}
}

// 7 and 9: a lookup error fails closed, holds no money, and is not cached.
func TestALookupErrorFailsClosedAndIsNotCached(t *testing.T) {
	f := newFixture(t)
	f.middayIST()
	register := &fakeRegister{err: errors.New("register unreachable")}
	f.service.DND = sending.CacheDNDLookups(register, time.Now)
	before := f.balance()
	refused(t, f.sendWithCategory(t, "PROMOTIONAL", "9876543214"), "dnd_check_unavailable")
	refused(t, f.sendWithCategory(t, "PROMOTIONAL", "9876543214"), "dnd_check_unavailable")
	if n := register.lookups.Load(); n != 2 {
		t.Fatalf("%d lookups after two sends, want 2 — an error must not be cached", n)
	}
	if after := f.balance(); after != before {
		t.Fatalf("balance moved from %d to %d on a refusal", before, after)
	}
}

// 8: an answer is cached for a day, and looked up again after it.
func TestAResultIsCachedForADay(t *testing.T) {
	f := newFixture(t)
	f.middayIST()
	register := &fakeRegister{}
	now := time.Now()
	f.service.DND = sending.CacheDNDLookups(register, func() time.Time { return now })
	f.sendWithCategory(t, "PROMOTIONAL", "9876543215")
	f.sendWithCategory(t, "PROMOTIONAL", "9876543215")
	if n := register.lookups.Load(); n != 1 {
		t.Fatalf("%d lookups for two sends inside a day, want 1", n)
	}
	now = now.Add(25 * time.Hour)
	f.sendWithCategory(t, "PROMOTIONAL", "9876543215")
	if n := register.lookups.Load(); n != 2 {
		t.Fatalf("%d lookups after the cache expired, want 2", n)
	}
}
