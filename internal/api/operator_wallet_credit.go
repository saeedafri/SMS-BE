package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/store"
)

// maxOperatorCreditMinor is the most one credit can add: 1,000,000,000 minor
// units, ₹1 crore. A mistyped extra zero on a bank transfer is refused rather
// than credited, and the money path has no undo.
const maxOperatorCreditMinor = 1_000_000_000

// CreditTenantWallet puts money a tenant paid outside the product into their
// wallet. The same bank reference is credited to a tenant once, so an operator
// who presses the button twice, or two operators booking the same transfer,
// move the balance once.
func (s *Server) CreditTenantWallet(ctx context.Context, request gen.CreditTenantWalletRequestObject) (
	gen.CreditTenantWalletResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.CreditTenantWallet401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	tenantID, valid := parsePathID(request.Id)
	if !valid {
		return gen.CreditTenantWallet404JSONResponse(errorBody(codeNotFound, "No such tenant.")), nil
	}
	tenant, err := store.GetTenant(ctx, s.operatorPool(), tenantID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.CreditTenantWallet404JSONResponse(errorBody(codeNotFound, "No such tenant.")), nil
	}
	if err != nil {
		return nil, err
	}

	body := request.Body
	if body == nil {
		return gen.CreditTenantWallet422JSONResponse(errorBody(codeValidation,
			"A credit needs a currency, an amount and the bank's reference.")), nil
	}
	currency := string(body.Currency)
	reference := strings.TrimSpace(body.Reference)
	note := ""
	if body.Note != nil {
		note = strings.TrimSpace(*body.Note)
	}
	switch {
	case !oneOf(currency, validCurrencies):
		return gen.CreditTenantWallet422JSONResponse(errorBody(codeValidation,
			enumMessage("currency", validCurrencies))), nil
	case body.AmountMinor < 1 || body.AmountMinor > maxOperatorCreditMinor:
		return gen.CreditTenantWallet422JSONResponse(errorBody(codeValidation,
			"amountMinor must be between 1 and 1,000,000,000 minor units.")), nil
	case len(reference) < 6 || len(reference) > 64:
		return gen.CreditTenantWallet422JSONResponse(errorBody(codeValidation,
			"reference must be the bank's transfer reference (UTR) or invoice number, 6 to 64 characters.")), nil
	case len(note) > 200:
		return gen.CreditTenantWallet422JSONResponse(errorBody(codeValidation,
			"note must be at most 200 characters.")), nil
	}

	entry, err := store.CreditWalletByTransfer(ctx, s.DB, store.Identity{TenantID: tenantID},
		currency, int64(body.AmountMinor), reference)
	if errors.Is(err, store.ErrConflict) {
		return gen.CreditTenantWallet409JSONResponse(errorBody(codeConflict,
			fmt.Sprintf("Reference %s has already been credited to %s.", reference, tenant.Name))), nil
	}
	if err != nil {
		return nil, err
	}

	detail := fmt.Sprintf("Credited %s %d.%02d to %s", currency,
		body.AmountMinor/100, body.AmountMinor%100, tenant.Name)
	if note != "" {
		detail += " — " + note
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, "wallet.credit",
		&tenantID, tenant.Name, reference, detail); err != nil {
		return nil, err
	}
	return gen.CreditTenantWallet201JSONResponse(ledgerEntryResponse(entry)), nil
}
