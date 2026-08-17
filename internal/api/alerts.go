package api

import (
	"context"
	"encoding/json"

	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// Alert rules: the thresholds that tell a customer their balance is low, their
// delivery rate has dropped, or their spend or volume has run past a ceiling.
//
// These used to have no storage. GetAlerts returned four hardcoded disabled
// groups and UpdateAlerts answered 501, so the screen accepted input, showed no
// error, and forgot it — the worst shape a settings page can have, because the
// customer believes they are now being warned when they are not.

// defaultLowBalanceThresholdMinor is 1,000 of the major unit — ₹1,000, $1,000.
// It is a placeholder in a disabled rule, so it only ever needs to be a
// plausible starting figure someone edits, never a recommendation.
const defaultLowBalanceThresholdMinor = 100_000

// defaultAlertRules is what a tenant who has configured nothing sees.
//
// Every group is present and disabled, rather than absent. The contract marks
// all four as required, and "off" is the honest state of a new account —
// omitting them would make the screen guess.
func defaultAlertRules() gen.AlertRules {
	return gen.AlertRules{
		// Overwritten by loadAlertRules with one row per wallet the tenant
		// holds. Empty here because a tenant holds no wallet until they top up.
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
	}
}

// loadAlertRules returns the tenant's stored rules, falling back to the
// defaults. A stored document that cannot be decoded also falls back rather
// than failing the request: the alerts screen is where someone goes to fix
// their alerts, and refusing to render it would lock them out of the repair.
func (s *Server) loadAlertRules(ctx context.Context, identity store.Identity) (gen.AlertRules, error) {
	stored, err := store.LoadAlertRules(ctx, s.DB, identity)
	if err != nil {
		return gen.AlertRules{}, err
	}
	rules := defaultAlertRules()
	if len(stored) > 0 {
		if err := json.Unmarshal(stored, &rules); err != nil {
			s.Logger.Warn("stored alert rules could not be decoded; showing defaults",
				"tenant", identity.TenantID, "error", err)
			return defaultAlertRules(), nil
		}
	}
	// One low-balance row per wallet the tenant actually holds.
	//
	// The screen renders a row per currency and has nothing to render without
	// one — it says "No active wallets" instead, so a tenant with a funded INR
	// wallet could never configure the alert that guards it. The rows are
	// derived rather than stored because the set of currencies is the wallet's
	// to decide, not the alert config's: opening a second wallet must surface a
	// second row without anyone editing this document.
	configured := make(map[gen.CurrencyCode]gen.LowBalanceRule, len(rules.LowBalance))
	for _, rule := range rules.LowBalance {
		configured[rule.Currency] = rule
	}
	balances, err := store.ListWalletBalances(ctx, s.DB, identity)
	if err != nil {
		return gen.AlertRules{}, err
	}
	rules.LowBalance = make([]gen.LowBalanceRule, 0, len(balances))
	for _, balance := range balances {
		currency := gen.CurrencyCode(balance.Currency)
		rule, ok := configured[currency]
		if !ok {
			// Matches the other three groups: present and off, with a threshold
			// that is a starting point rather than a promise.
			rule = gen.LowBalanceRule{
				Currency: currency, Enabled: false,
				ThresholdMinor: defaultLowBalanceThresholdMinor, Recipients: []string{},
			}
		}
		if rule.Recipients == nil {
			rule.Recipients = []string{}
		}
		rules.LowBalance = append(rules.LowBalance, rule)
	}
	if rules.DeliveryFloor.Recipients == nil {
		rules.DeliveryFloor.Recipients = []string{}
	}
	if rules.SpendCeiling.Recipients == nil {
		rules.SpendCeiling.Recipients = []string{}
	}
	if rules.VolumeCeiling.Recipients == nil {
		rules.VolumeCeiling.Recipients = []string{}
	}
	return rules, nil
}

func (s *Server) GetAlerts(ctx context.Context, _ gen.GetAlertsRequestObject) (gen.GetAlertsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.GetAlerts401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	rules, err := s.loadAlertRules(ctx, identity)
	if err != nil {
		return nil, err
	}
	return gen.GetAlerts200JSONResponse(rules), nil
}

// UpdateAlerts applies a partial update: only the top-level groups present in
// the body change, and the rest are left exactly as they were.
//
// That is what the contract specifies, and it is also what the screen needs —
// each rule group has its own Save button, so a request carrying only the spend
// ceiling must not silently reset the other three to their defaults.
func (s *Server) UpdateAlerts(ctx context.Context, request gen.UpdateAlertsRequestObject) (gen.UpdateAlertsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.UpdateAlerts401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if !canManageSettings(identity.Role) {
		return gen.UpdateAlerts403JSONResponse(
			errorBody(codeForbidden, "Member role cannot change alert settings.")), nil
	}

	rules, err := s.loadAlertRules(ctx, identity)
	if err != nil {
		return nil, err
	}
	if request.Body != nil {
		if request.Body.LowBalance != nil {
			// Merged by currency, not replaced. The screen has one Save button
			// per card and the low-balance card submits only the currencies it
			// rendered, so a wholesale replace would delete the threshold for
			// any wallet that was not on screen.
			byCurrency := make(map[gen.CurrencyCode]int, len(rules.LowBalance))
			for i, rule := range rules.LowBalance {
				byCurrency[rule.Currency] = i
			}
			for _, incoming := range *request.Body.LowBalance {
				if i, ok := byCurrency[incoming.Currency]; ok {
					rules.LowBalance[i] = incoming
					continue
				}
				rules.LowBalance = append(rules.LowBalance, incoming)
			}
		}
		if request.Body.DeliveryFloor != nil {
			rules.DeliveryFloor = *request.Body.DeliveryFloor
		}
		if request.Body.SpendCeiling != nil {
			rules.SpendCeiling = *request.Body.SpendCeiling
		}
		if request.Body.VolumeCeiling != nil {
			rules.VolumeCeiling = *request.Body.VolumeCeiling
		}
	}

	encoded, err := json.Marshal(rules)
	if err != nil {
		return nil, err
	}
	if err := store.SaveAlertRules(ctx, s.DB, identity, encoded); err != nil {
		return nil, err
	}
	// The merged document is returned, not the patch. The screen re-renders from
	// this response, and echoing only what was sent would blank the groups the
	// caller did not touch.
	return gen.UpdateAlerts200JSONResponse(rules), nil
}
