package api

import (
	"context"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// The dashboard layout fetches these on every render and throws on any non-2xx,
// so no screen renders until they answer. Wallet moved to its real
// implementation in Stage 3; Alerts (Stage 8) and Inbox (Stage 11) still return
// the genuine empty state of a new tenant, which is the truth rather than a
// placeholder.

func (s *Server) GetAlerts(ctx context.Context, _ gen.GetAlertsRequestObject) (gen.GetAlertsResponseObject, error) {
	if _, ok := identityFrom(ctx); !ok {
		return gen.GetAlerts401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	// All four rule groups are required by the schema. They exist but are off,
	// which is the correct state for an account that has configured nothing.
	// LowBalance is one row per currency the tenant holds — none yet.
	return gen.GetAlerts200JSONResponse(gen.AlertRules{
		LowBalance: []gen.LowBalanceRule{},
		DeliveryFloor: gen.DeliveryFloorRule{
			Enabled: false, Range: gen.AnalyticsRangeN7d,
			ThresholdPercent: 90, Recipients: []string{},
		},
		SpendCeiling: gen.SpendCeilingRule{
			Enabled: false, Currency: gen.CurrencyCode("INR"),
			ThresholdMinor: 0, Recipients: []string{},
		},
		VolumeCeiling: gen.VolumeCeilingRule{
			Enabled: false, ThresholdCount: 0, Recipients: []string{},
		},
	}), nil
}
