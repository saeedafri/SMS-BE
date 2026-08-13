package api

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/billing"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// maxTopUpMinor caps a single top-up. A fat-fingered extra zero on a card
// capture is a support incident and a chargeback; refusing it costs nothing.
const maxTopUpMinor int64 = 100_000_000 // 10 lakh rupees / $1M equivalent

func (s *Server) gateway() billing.PaymentGateway {
	if s.Gateway != nil {
		return s.Gateway
	}
	return billing.ManualGateway{}
}

func ledgerEntryResponse(e store.LedgerEntry) gen.LedgerEntry {
	entry := gen.LedgerEntry{
		Id:                e.ID.String(),
		Type:              gen.LedgerEntryType(e.Type),
		AmountMinor:       int(e.AmountMinor),
		Currency:          gen.CurrencyCode(e.Currency),
		CreatedAt:         e.CreatedAt,
		Description:       e.Description,
		BalanceAfterMinor: int(e.BalanceAfterMinor),
		CampaignName:      e.CampaignName,
		JourneyName:       e.JourneyName,
	}
	// These four are required-but-nullable in the contract, so they must be
	// present as null rather than omitted.
	if e.CampaignID != nil {
		value := e.CampaignID.String()
		entry.CampaignId = &value
	}
	if e.JourneyID != nil {
		value := e.JourneyID.String()
		entry.JourneyId = &value
	}
	return entry
}

func (s *Server) ListWalletBalances(ctx context.Context, _ gen.ListWalletBalancesRequestObject) (gen.ListWalletBalancesResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.ListWalletBalances401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	balances, err := store.ListWalletBalances(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.WalletBalance, 0, len(balances))
	for _, balance := range balances {
		out = append(out, gen.WalletBalance{
			BalanceMinor: int(balance.BalanceMinor),
			Currency:     gen.CurrencyCode(balance.Currency),
		})
	}
	return gen.ListWalletBalances200JSONResponse(out), nil
}

func (s *Server) ListLedger(ctx context.Context, request gen.ListLedgerRequestObject) (gen.ListLedgerResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.ListLedger401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	currency, cursor, limit := "", "", 50
	if request.Params.Currency != nil {
		currency = string(*request.Params.Currency)
	}
	if request.Params.Cursor != nil {
		cursor = *request.Params.Cursor
	}
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}

	entries, next, err := store.LedgerPage(ctx, s.DB, identity, currency, cursor, limit)
	if errors.Is(err, store.ErrInvalidCursor) {
		// A cursor we never issued is the client's error, not ours.
		return gen.ListLedger401JSONResponse(
			errorBody(codeValidation, "That page cursor is not valid.")), nil
	}
	if err != nil {
		return nil, err
	}

	out := make([]gen.LedgerEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, ledgerEntryResponse(entry))
	}
	page := gen.LedgerPage{Entries: out}
	if next != "" {
		page.NextCursor = &next
	}
	return gen.ListLedger200JSONResponse(page), nil
}

func (s *Server) TopUpWallet(ctx context.Context, request gen.TopUpWalletRequestObject) (gen.TopUpWalletResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.TopUpWallet401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if !canManageSettings(identity.Role) {
		return gen.TopUpWallet403JSONResponse(
			errorBody(codeForbidden, "Member role cannot add funds.")), nil
	}

	amount := int64(request.Body.AmountMinor)
	switch {
	case amount <= 0:
		return gen.TopUpWallet422JSONResponse(
			errorBody(codeValidation, "Enter an amount greater than zero.")), nil
	case amount > maxTopUpMinor:
		return gen.TopUpWallet422JSONResponse(
			errorBody(codeValidation, "That amount is larger than the single top-up limit.")), nil
	}

	methodID, err := uuid.Parse(request.Body.PaymentMethodId)
	if err != nil {
		return gen.TopUpWallet422JSONResponse(
			errorBody(codeValidation, "That payment method does not exist.")), nil
	}
	exists, err := store.PaymentMethodExists(ctx, s.DB, identity, methodID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return gen.TopUpWallet422JSONResponse(
			errorBody(codeValidation, "That payment method does not exist.")), nil
	}

	// Capture first, then move the balance. The other order would credit a
	// wallet for money that was never taken.
	receipt, err := s.gateway().Capture(ctx, billing.Capture{
		TenantID:        identity.TenantID.String(),
		PaymentMethodID: methodID.String(),
		Currency:        string(request.Body.Currency),
		AmountMinor:     amount,
		Reference:       methodID.String(),
	})
	if errors.Is(err, billing.ErrCaptureDeclined) {
		return gen.TopUpWallet422JSONResponse(
			errorBody(codeValidation, "That payment was declined.")), nil
	}
	if err != nil {
		return nil, err
	}

	entry, err := store.AppendLedgerEntry(ctx, s.DB, identity, store.LedgerEntry{
		Currency:    string(request.Body.Currency),
		Type:        "topup",
		AmountMinor: receipt.CapturedMinor,
		Description: "Wallet top-up (" + receipt.Provider + ")",
	})
	if err != nil {
		return nil, err
	}
	return gen.TopUpWallet201JSONResponse(ledgerEntryResponse(entry)), nil
}

func paymentMethodResponse(m store.PaymentMethod) gen.PaymentMethod {
	return gen.PaymentMethod{
		Id:        m.ID.String(),
		Brand:     gen.PaymentMethodBrand(m.Brand),
		Last4:     m.Last4,
		IsDefault: m.IsDefault,
	}
}

func (s *Server) ListPaymentMethods(ctx context.Context, _ gen.ListPaymentMethodsRequestObject) (gen.ListPaymentMethodsResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.ListPaymentMethods401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	methods, err := store.ListPaymentMethods(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.PaymentMethod, 0, len(methods))
	for _, method := range methods {
		out = append(out, paymentMethodResponse(method))
	}
	return gen.ListPaymentMethods200JSONResponse(out), nil
}

func (s *Server) AddPaymentMethod(ctx context.Context, request gen.AddPaymentMethodRequestObject) (gen.AddPaymentMethodResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.AddPaymentMethod401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if !canManageSettings(identity.Role) {
		return gen.AddPaymentMethod403JSONResponse(
			errorBody(codeForbidden, "Member role cannot manage payment methods.")), nil
	}
	if len(request.Body.Last4) != 4 {
		return gen.AddPaymentMethod422JSONResponse(
			errorBody(codeValidation, "Enter the last four digits of the card.")), nil
	}
	for _, digit := range request.Body.Last4 {
		if digit < '0' || digit > '9' {
			return gen.AddPaymentMethod422JSONResponse(
				errorBody(codeValidation, "Enter the last four digits of the card.")), nil
		}
	}

	created, err := store.AddPaymentMethod(ctx, s.DB, identity,
		string(request.Body.Brand), request.Body.Last4)
	if err != nil {
		return nil, err
	}
	return gen.AddPaymentMethod201JSONResponse(paymentMethodResponse(created)), nil
}

func (s *Server) RemovePaymentMethod(ctx context.Context, request gen.RemovePaymentMethodRequestObject) (gen.RemovePaymentMethodResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.RemovePaymentMethod401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if !canManageSettings(identity.Role) {
		return gen.RemovePaymentMethod403JSONResponse(
			errorBody(codeForbidden, "Member role cannot manage payment methods.")), nil
	}
	methodID, err := uuid.Parse(request.Id)
	if err != nil {
		return gen.RemovePaymentMethod404JSONResponse(
			errorBody(codeNotFound, "No such payment method.")), nil
	}
	err = store.RemovePaymentMethod(ctx, s.DB, identity, methodID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.RemovePaymentMethod404JSONResponse(
			errorBody(codeNotFound, "No such payment method.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.RemovePaymentMethod204Response{}, nil
}

func (s *Server) SetDefaultPaymentMethod(ctx context.Context, request gen.SetDefaultPaymentMethodRequestObject) (gen.SetDefaultPaymentMethodResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.SetDefaultPaymentMethod401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if !canManageSettings(identity.Role) {
		return gen.SetDefaultPaymentMethod403JSONResponse(
			errorBody(codeForbidden, "Member role cannot manage payment methods.")), nil
	}
	methodID, err := uuid.Parse(request.Id)
	if err != nil {
		return gen.SetDefaultPaymentMethod404JSONResponse(
			errorBody(codeNotFound, "No such payment method.")), nil
	}
	updated, err := store.SetDefaultPaymentMethod(ctx, s.DB, identity, methodID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.SetDefaultPaymentMethod404JSONResponse(
			errorBody(codeNotFound, "No such payment method.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.SetDefaultPaymentMethod200JSONResponse(paymentMethodResponse(updated)), nil
}

func autoRechargeResponse(c store.AutoRecharge) gen.AutoRechargeConfig {
	config := gen.AutoRechargeConfig{
		Currency:          gen.CurrencyCode(c.Currency),
		Enabled:           c.Enabled,
		ThresholdMinor:    int(c.ThresholdMinor),
		TopUpMinor:        int(c.TopUpMinor),
		LastFailureAt:     c.LastFailureAt,
		LastFailureReason: c.LastFailureReason,
	}
	if c.PaymentMethodID != nil {
		value := c.PaymentMethodID.String()
		config.PaymentMethodId = &value
	}
	return config
}

func (s *Server) ListAutoRecharge(ctx context.Context, _ gen.ListAutoRechargeRequestObject) (gen.ListAutoRechargeResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.ListAutoRecharge401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	configs, err := store.ListAutoRecharge(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	out := make([]gen.AutoRechargeConfig, 0, len(configs))
	for _, config := range configs {
		out = append(out, autoRechargeResponse(config))
	}
	return gen.ListAutoRecharge200JSONResponse(out), nil
}

func (s *Server) UpdateAutoRecharge(ctx context.Context, request gen.UpdateAutoRechargeRequestObject) (gen.UpdateAutoRechargeResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.UpdateAutoRecharge401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	if !canManageSettings(identity.Role) {
		return gen.UpdateAutoRecharge403JSONResponse(
			errorBody(codeForbidden, "Member role cannot change auto-recharge.")), nil
	}

	config := store.AutoRecharge{
		Currency:       string(request.Body.Currency),
		Enabled:        request.Body.Enabled,
		ThresholdMinor: int64(request.Body.ThresholdMinor),
		TopUpMinor:     int64(request.Body.TopUpMinor),
	}
	if request.Body.PaymentMethodId != nil && *request.Body.PaymentMethodId != "" {
		methodID, err := uuid.Parse(*request.Body.PaymentMethodId)
		if err != nil {
			return gen.UpdateAutoRecharge422JSONResponse(
				errorBody(codeValidation, "That payment method does not exist.")), nil
		}
		exists, err := store.PaymentMethodExists(ctx, s.DB, identity, methodID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return gen.UpdateAutoRecharge422JSONResponse(
				errorBody(codeValidation, "That payment method does not exist.")), nil
		}
		config.PaymentMethodID = &methodID
	}

	// Enabling auto-recharge with nothing to charge would fail silently at the
	// worst possible moment — when the wallet has just run dry mid-campaign.
	if config.Enabled {
		if config.PaymentMethodID == nil {
			return gen.UpdateAutoRecharge422JSONResponse(errorBody(codeValidation,
				"Choose a payment method before enabling auto-recharge.")), nil
		}
		if config.TopUpMinor <= 0 {
			return gen.UpdateAutoRecharge422JSONResponse(errorBody(codeValidation,
				"Set a top-up amount greater than zero.")), nil
		}
	}

	saved, err := store.UpsertAutoRecharge(ctx, s.DB, identity, config)
	if err != nil {
		return nil, err
	}
	return gen.UpdateAutoRecharge200JSONResponse(autoRechargeResponse(saved)), nil
}
