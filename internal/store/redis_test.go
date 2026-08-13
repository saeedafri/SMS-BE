package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/saeedafri/sms-be/internal/store"
)

func TestRedisRoundTrip(t *testing.T) {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Skip("REDIS_URL not set")
	}
	ctx := context.Background()

	client, err := store.OpenRedis(ctx, url)
	if err != nil {
		t.Fatalf("OpenRedis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	const key = "sms:test:roundtrip"
	t.Cleanup(func() { client.Del(context.Background(), key) })

	if err := client.Set(ctx, key, "value", 0).Err(); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := client.Get(ctx, key).Result()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "value" {
		t.Fatalf("get returned %q, want %q", got, "value")
	}
}

func TestOpenRedisRejectsUnreachableServer(t *testing.T) {
	// Port 1 is reserved and never listening, so this exercises the ping
	// check rather than the URL parser — an unreachable Redis must be an
	// error at startup, not a surprise on the first request.
	if _, err := store.OpenRedis(context.Background(), "redis://localhost:1/0"); err == nil {
		t.Fatal("expected an error for an unreachable Redis, got nil")
	}
}
