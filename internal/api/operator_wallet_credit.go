package api

import (
	"context"
	"errors"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/store"
)

// maxOperatorCreditMinor is the most one credit can add: 1,000,000,000 minor
// units, ₹1 crore. A mistyped extra zero on a bank transfer is refused rather
// than credited, and the money path has no undo.
const maxOperatorCreditMinor = 1_000_000_000

// GetTenantWallet is the balance an operator sees before deciding to credit,
// and after. A tenant that has never held money has no balance rows, which is
// an empty list rather than a 404: the tenant exists and holds nothing.
func (s *Server) GetTenantWallet(ctx context.Context, request gen.GetTenantWalletRequestObject) (
	gen.GetTenantWalletResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetTenantWallet401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	tenantID, valid := parsePathID(request.Id)
	if !valid {
		return gen.GetTenantWallet404JSONResponse(errorBody(codeNotFound, "No such tenant.")), nil
	}
	if _, err := store.GetTenant(ctx, s.operatorPool(), tenantID); errors.Is(err, store.ErrNotFound) {
		return gen.GetTenantWallet404JSONResponse(errorBody(codeNotFound, "No such tenant.")), nil
	} else if err != nil {
		return nil, err
	}
	balances, err := store.ListWalletBalances(ctx, s.DB, store.Identity{TenantID: tenantID})
	if err != nil {
		return nil, err
	}
	// Wrapped in an object, not a bare array: the contract's TenantWallet says
	// so, and a room to grow beside balances later without breaking callers.
	wallet := gen.TenantWallet{Balances: make([]gen.WalletBalance, 0, len(balances))}
	for _, balance := range balances {
		wallet.Balances = append(wallet.Balances, gen.WalletBalance{
			Currency: gen.CurrencyCode(balance.Currency), BalanceMinor: int(balance.BalanceMinor)})
	}
	return gen.GetTenantWallet200JSONResponse(wallet), nil
}
