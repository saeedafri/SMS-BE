package sending_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/saeedafri/sms-be/internal/store"
)

// redisFor gives the fixture's service a real Redis, or skips: a cap that is
// only ever tested against a fake is a cap nobody has seen work.
func redisFor(t *testing.T, f *fixture) *redis.Client {
	t.Helper()
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Skip("REDIS_URL not set")
	}
	client, err := store.OpenRedis(context.Background(), url)
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	f.service.Redis = client
	return client
}

func setFrequencyCap(t *testing.T, f *fixture, daily, weekly, monthly *int, excluded ...string) {
	t.Helper()
	if excluded == nil {
		excluded = []string{}
	}
	if _, err := sendAdmin.Exec(context.Background(), `
		INSERT INTO frequency_caps (tenant_id, channel, daily_limit, weekly_limit, monthly_limit, excluded_numbers)
		VALUES ($1, 'SMS', $2, $3, $4, $5)`,
		f.identity.TenantID, daily, weekly, monthly, excluded); err != nil {
		t.Fatalf("set frequency cap: %v", err)
	}
}

func intp(n int) *int { return &n }

// The Nth+1 message to the same handset on the same day is refused with its own
// code, costs nothing, and does not touch anyone else's allowance.
func TestADailyCapRefusesTheNextMessageToTheSameHandsetOnly(t *testing.T) {
	f := newFixture(t)
	redisFor(t, f)
	setFrequencyCap(t, f, intp(2), nil, nil)

	number, other := "+919810000101", "+919810000102"
	for i := 1; i <= 2; i++ {
		if result, err := f.send(number, "hello"); err != nil || result.Status == "rejected" {
			t.Fatalf("message %d under the cap: status=%s err=%v", i, result.Status, err)
		}
	}
	before := f.balance()
	result, err := f.send(number, "hello")
	if err == nil || result.Status != "rejected" || result.FailureCode != "frequency_cap" {
		t.Fatalf("3rd message = %s/%s err=%v, want rejected/frequency_cap", result.Status,
			result.FailureCode, err)
	}
	if f.balance() != before {
		t.Errorf("a capped message moved the wallet: %d -> %d", before, f.balance())
	}
	if result, err := f.send(other, "hello"); err != nil || result.Status == "rejected" {
		t.Errorf("a different handset was capped: %s %v", result.Status, err)
	}
}

func TestAnExcludedNumberIsNeverCapped(t *testing.T) {
	f := newFixture(t)
	redisFor(t, f)
	setFrequencyCap(t, f, intp(1), nil, nil, "+919810000103")

	for i := 1; i <= 4; i++ {
		if result, err := f.send("+919810000103", "hello"); err != nil || result.Status == "rejected" {
			t.Fatalf("excluded number message %d: %s %v", i, result.Status, err)
		}
	}
}

// The weekly and monthly windows bind independently of the day: a handset can be
// under today's limit and over this week's.
func TestAWeeklyCapBindsEvenWhenTheDayIsFree(t *testing.T) {
	f := newFixture(t)
	redisFor(t, f)
	setFrequencyCap(t, f, intp(5), intp(2), intp(9))

	number := "+919810000104"
	for i := 1; i <= 2; i++ {
		if result, err := f.send(number, "hello"); err != nil || result.Status == "rejected" {
			t.Fatalf("message %d: %s %v", i, result.Status, err)
		}
	}
	if result, _ := f.send(number, "hello"); result.FailureCode != "frequency_cap" {
		t.Errorf("3rd message this week = %s/%s, want frequency_cap", result.Status, result.FailureCode)
	}
}

// Tomorrow is a new day. The clock moves; the counter for the old day stays put.
func TestTheDailyCapResetsAtTheTenantsMidnight(t *testing.T) {
	f := newFixture(t)
	redisFor(t, f)
	setFrequencyCap(t, f, intp(1), nil, nil)
	today := time.Now()
	f.service.Now = func() time.Time { return today }

	number := "+919810000105"
	if result, err := f.send(number, "hello"); err != nil || result.Status == "rejected" {
		t.Fatalf("first: %s %v", result.Status, err)
	}
	if result, _ := f.send(number, "hello"); result.FailureCode != "frequency_cap" {
		t.Fatalf("second same day = %s, want frequency_cap", result.FailureCode)
	}
	today = today.Add(24 * time.Hour)
	if result, err := f.send(number, "hello"); err != nil || result.Status == "rejected" {
		t.Errorf("next day still capped: %s %v", result.Status, err)
	}
}

func TestNoCapMeansNoLimit(t *testing.T) {
	f := newFixture(t)
	redisFor(t, f)
	for i := 0; i < 5; i++ {
		if result, err := f.send("+919810000106", "hello"); err != nil || result.Status == "rejected" {
			t.Fatalf("uncapped message %d: %s %v", i, result.Status, err)
		}
	}
}

// Redis is hot state: with it gone the cap steps aside rather than stopping the
// tenant's traffic.
func TestAnUnreachableRedisLetsMessagesThrough(t *testing.T) {
	f := newFixture(t)
	f.service.Redis = redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond,
		MaxRetries: -1})
	setFrequencyCap(t, f, intp(1), nil, nil)

	for i := 0; i < 3; i++ {
		if result, err := f.send("+919810000107", "hello"); err != nil || result.Status == "rejected" {
			t.Fatalf("message %d with Redis down: %s %v", i, result.Status, err)
		}
	}
}
