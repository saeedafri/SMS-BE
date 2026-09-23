package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/saeedafri/sms-be/internal/store"
)

// EnforceMessageRetention deletes each tenant's message log older than the
// retention it chose. The ClickHouse TTL is only the 365-day ceiling; this is
// what makes 30, 90 and 180 true, and it reads the setting fresh every cycle,
// so a change applies to the whole table on the next run rather than only to
// messages sent afterwards.
//
// The hourly rollups are left alone: they hold counts, not messages, and
// deleting them would rewrite a tenant's billing history.
func (s *Server) EnforceMessageRetention(ctx context.Context) error {
	if s.OperatorDB == nil || !s.ClickHouse.Configured() {
		return nil
	}
	groups, err := store.TenantsByRetention(ctx, s.OperatorDB)
	if err != nil {
		return err
	}
	conn, err := s.clickhouse(ctx)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	var failures []error
	for days, tenants := range groups {
		cutoff := now.AddDate(0, 0, -days)
		removed, err := store.DeleteMessagesBefore(ctx, conn, tenants, cutoff)
		if err != nil {
			failures = append(failures, fmt.Errorf("%d-day retention: %w", days, err))
			continue
		}
		if removed > 0 {
			s.Logger.Info("message log retention enforced", "retention_days", days,
				"tenants", len(tenants), "messages_removed", removed)
		}
	}
	return errors.Join(failures...)
}
