package store

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// OpenRedis dials Redis and verifies it answers before returning. Redis holds
// rate-limit buckets, idempotency keys, live counters and abuse throttles —
// hot state, never the source of truth. Losing it degrades the system; it does
// not lose messages.
func OpenRedis(ctx context.Context, raw string) (*redis.Client, error) {
	opts, err := redis.ParseURL(raw)
	if err != nil {
		return nil, fmt.Errorf("store: parse redis url: %w", err)
	}
	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("store: ping redis: %w", err)
	}
	return client, nil
}
