package sending

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saeedafri/sms-be/internal/store"
)

// StuckCampaignWindow is how long a campaign may sit at 'sending' before the
// sweep decides nothing is still working on it.
//
// Generous on purpose: a large list legitimately takes a while to fan out, and
// landing a campaign that is still running would report it finished while
// messages were still leaving. Fifteen minutes is far longer than any fan-out
// this platform does today and far shorter than the days a genuinely stuck one
// would otherwise sit there.
const StuckCampaignWindow = 15 * time.Minute

// ReconcileStuckCampaigns lands campaigns whose fan-out never finished.
//
// Fan-out marks 'sending', sends, then marks a terminal status. Anything that
// stops it in between — a ClickHouse error mid-page, a deploy, a panic — leaves
// the row at 'sending' with no process left to move it, and the customer's
// campaign shows as sending forever.
//
// The verdict comes from the messages actually recorded, not from a guess: a
// campaign that got some of its messages out really did send, and one that got
// none out really did fail. Same rule fan-out itself applies when it completes
// normally, so a reconciled campaign is indistinguishable from one that landed
// on its own.
//
// tenants is the operator pool, used ONLY to enumerate tenant ids — campaigns
// carry no operator policy, so every campaign read and write below still goes
// through the ordinary tenant-scoped pool.
func ReconcileStuckCampaigns(ctx context.Context, tenants, db *pgxpool.Pool,
	clickhouse driver.Conn, window time.Duration, limit int) (int, error) {

	if window <= 0 {
		window = StuckCampaignWindow
	}
	if limit <= 0 {
		limit = 100
	}
	cutoff := time.Now().UTC().Add(-window)

	// ponytail: one query per tenant per tick. Fine at this scale; if the
	// tenant count ever makes this hurt, give campaigns an operator read policy
	// and do it in a single cross-tenant query.
	rows, err := store.ListTenants(ctx, tenants, nil, nil)
	if err != nil {
		return 0, err
	}

	landed, failures := 0, []error(nil)
	for _, tenant := range rows {
		identity := store.Identity{TenantID: tenant.ID}
		stuck, err := store.FindStuckCampaigns(ctx, db, identity, cutoff, limit)
		if err != nil {
			// One tenant must never stall the sweep for everybody else.
			failures = append(failures, fmt.Errorf("tenant %s: %w", tenant.ID, err))
			continue
		}
		for _, campaign := range stuck {
			counts, err := store.CountCampaignMessages(ctx, clickhouse, tenant.ID, campaign.ID)
			if err != nil {
				failures = append(failures, fmt.Errorf("campaign %s: %w", campaign.ID, err))
				continue
			}
			status := "sent"
			if counts.Queued+counts.Sent+counts.Delivered == 0 {
				status = "failed"
			}
			if err := store.SetCampaignStatus(ctx, db, identity, campaign.ID, status); err != nil {
				failures = append(failures, fmt.Errorf("campaign %s: %w", campaign.ID, err))
				continue
			}
			landed++
		}
	}
	return landed, errors.Join(failures...)
}
