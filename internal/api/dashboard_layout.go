package api

import (
	"context"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// The dashboard layout fetches these three on every render and throws on any
// non-2xx, so no screen renders until all three answer. They belong to later
// stages — Wallet is Stage 3, Alerts Stage 8, Inbox Stage 11 — but a new tenant
// genuinely has no wallet, no alert history and no conversations, so returning
// that empty state now is the truth rather than a placeholder. Each is replaced
// by its real implementation when its stage lands.

func (s *Server) ListWalletBalances(ctx context.Context, _ gen.ListWalletBalancesRequestObject) (gen.ListWalletBalancesResponseObject, error) {
	if _, ok := identityFrom(ctx); !ok {
		return gen.ListWalletBalances401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	// A tenant with no wallet holds no balances. Not zero INR — no currencies
	// at all, which is what drives the dashboard's "add funds" empty state.
	return gen.ListWalletBalances200JSONResponse([]gen.WalletBalance{}), nil
}

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

func (s *Server) ListConversations(ctx context.Context, _ gen.ListConversationsRequestObject) (gen.ListConversationsResponseObject, error) {
	if _, ok := identityFrom(ctx); !ok {
		return gen.ListConversations401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	return gen.ListConversations200JSONResponse(gen.ConversationPage{
		Conversations: []gen.Conversation{},
		Total:         0,
		NextCursor:    nil,
	}), nil
}
