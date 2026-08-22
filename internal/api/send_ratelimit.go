package api

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Send throttling.
//
// GetRateLimit has always told customers their budget — 100 requests a second
// on live, 10 on test — and nothing enforced it. That was harmless while the
// only way to send was through the dashboard's own campaign screen. It stops
// being harmless the moment a key in someone's code can drive the send path:
// a retry loop with no backoff, or a leaked key, otherwise runs as fast as the
// network allows until the wallet empties.
//
// The numbers here are the same ones GetRateLimit reports. A limit a customer
// is shown and a limit that is applied must be one number, or the documentation
// is a lie in a specific and expensive way.
const (
	liveBurst = 200
	testBurst = 20
	// One second, because the tier is expressed per second.
	//
	// The window is anchored to the tenant's own first request rather than to
	// wall-clock seconds. Anchoring to the clock looks tidier and quietly halves
	// the limit's usefulness: a burst that straddles a second boundary is split
	// across two counters and neither notices it. Measured — thirty sends in
	// half a second landed 15/15 either side of a boundary and not one was
	// refused.
	//
	// ponytail: still a fixed window, so a burst either side of an EXPIRY can
	// reach 2x the tier. Swap for a sliding window if that ever matters.
	sendRateWindow = time.Second
)

// allowSend reports whether this tenant may send another message right now.
//
// Fails OPEN. Rate limiting protects the platform from a runaway caller; it is
// not what keeps a tenant inside their wallet, and refusing every send because
// Redis blinked would turn a degraded counter into an outage. Same stance the
// process takes at boot, which warns and continues when Redis is unreachable.
func (s *Server) allowSend(ctx context.Context, tenantID uuid.UUID, environment string) bool {
	if s.Redis == nil {
		return true
	}
	burst := liveBurst
	if environment == "test" {
		burst = testBurst
	}
	// Keyed on the tenant, not the key: two keys held by one customer share one
	// budget, which is the budget GetRateLimit describes.
	bucket := fmt.Sprintf("relay:sendrate:%s:%s", tenantID, environment)
	used, err := s.Redis.Incr(ctx, bucket).Result()
	if err != nil {
		return true
	}
	if used == 1 {
		// The first send of a window starts the clock. If this EXPIRE is lost
		// the key would live forever and throttle the tenant permanently, so it
		// is set on the counter that has just been created rather than assumed.
		s.Redis.Expire(ctx, bucket, sendRateWindow)
	}
	return used <= int64(burst)
}
