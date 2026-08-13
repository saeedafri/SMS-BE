package sending

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/domain/messaging"
	"github.com/saeedafri/sms-be/internal/store"
)

// DefaultValidityWindow is how long a message may sit accepted-but-unreported
// before we give up on the carrier. Real SMSC validity periods are typically
// 24-48h; anything still silent past that is never arriving.
const DefaultValidityWindow = 48 * time.Hour

// Reconcile expires messages a carrier accepted but never reported on, and
// releases the money held against them.
//
// This exists because delivery reports are best-effort: carriers drop them,
// networks partition, and our own ingest can be down during a deploy. Without
// this, a lost report means a tenant is charged forever for a message nobody
// can prove arrived — which is exactly the billing behaviour this product is
// built to replace. Silence is treated as failure, and failure is free.
func (s *Service) Reconcile(ctx context.Context, window time.Duration, limit int) (int, error) {
	if window <= 0 {
		window = DefaultValidityWindow
	}
	if limit <= 0 {
		limit = 1000
	}

	stale, err := store.FindStaleMessages(ctx, s.ClickHouse, time.Now().UTC().Add(-window), limit)
	if err != nil {
		return 0, err
	}

	expired, failures := 0, []error(nil)
	for _, message := range stale {
		// Routed through the normal settlement path rather than a bespoke one,
		// so the transition legality check and the refund rules stay identical
		// to a real carrier report arriving late.
		err := s.settle(ctx, store.Identity{TenantID: message.TenantID},
			connector.DeliveryReport{
				MessageID: message.ID.String(), Delivered: false,
				ErrorCode: "EXPIRED", OccurredAt: time.Now().UTC(),
			}, messaging.StateExpired)
		if err != nil {
			// One tenant must never stall the sweep for everybody else. This is
			// not hypothetical: ClickHouse retains messages for 90 days while a
			// tenant row can be deleted long before that, so a closed account's
			// stale messages will fail to settle forever. Abandoning the whole
			// run there would leave every other tenant's held money stuck.
			failures = append(failures, fmt.Errorf("message %s (tenant %s): %w",
				message.ID, message.TenantID, err))
			continue
		}
		expired++
	}
	return expired, errors.Join(failures...)
}
