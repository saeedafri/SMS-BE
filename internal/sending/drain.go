package sending

import (
	"context"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/store"
)

// ReportSource is a connector that queues its own delivery reports rather than
// posting them to us. Only the sandbox does this: a real carrier calls our
// ingest endpoint, so there is nothing to drain.
type ReportSource interface {
	DrainReports() []connector.DeliveryReport
}

// DrainSandboxReports applies whatever the sandbox has queued.
//
// This exists so the whole lifecycle is observable locally: without it a
// message stops at "sent" forever, no wallet ever settles, and the delivered /
// undelivered distinction the product is built on cannot be seen in the UI at
// all. In production the same settlement runs from the carrier's DLR webhook
// instead — this is the identical code path, driven by a different trigger.
func (s *Service) DrainSandboxReports(ctx context.Context) (int, error) {
	source, ok := s.Connector.(ReportSource)
	if !ok {
		return 0, nil
	}

	applied := 0
	for _, report := range source.DrainReports() {
		messageID, err := uuid.Parse(report.MessageID)
		if err != nil {
			continue
		}
		// The report carries no tenant — carriers have no concept of one — so
		// it is resolved from the message before anything is scoped.
		tenantID, err := store.FindMessageTenant(ctx, s.ClickHouse, messageID)
		if err != nil {
			continue
		}
		if err := s.ApplyDeliveryReport(ctx, store.Identity{TenantID: tenantID}, report); err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}
