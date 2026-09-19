package api

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saeedafri/sms-be/internal/domain/billing"
	"github.com/saeedafri/sms-be/internal/domain/compliance"
	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/store"
)

// Postpaid credit on account.
//
// Every figure these handlers serve is derived, on this request, from the two
// fact tables — what was issued and what arrived. None of them stores a status,
// a received amount or an allocation, and none of them recomputes a total of
// its own: the arithmetic lives in internal/domain/billing/postpaid.go so that
// the operator's view, the customer's view and both exports cannot drift.
//
// The customer's route and the operator's route are separate handlers rather
// than one with a flag. The one thing that must never be a parameter a caller
// can flip is whether a tenant reads the operator's notes.

// maxPaymentMinor bounds one recorded transfer exactly as a credit is bounded,
// so a mistyped extra zero is refused rather than banked.
const maxPaymentMinor = 1_000_000_000

// invoiceStatusFilter reads the status parameter, refusing anything the
// contract does not declare.
//
// Refused rather than ignored, and refused rather than quietly treated as the
// default. An undeclared status used to reach MatchesFilter's default branch
// and be answered as `due` — so `?status=ovedue`, one letter out, returned the
// late invoices PLUS every invoice comfortably inside its terms, with no error
// and nothing on screen to say so. An operator asking "who is late" would have
// chased customers who were not.
//
// Returning everything would have been wrong too, but wrong in a way that looks
// wrong. This looked right, which is why it is worth a 422.
func invoiceStatusFilter(status *string) (string, bool) {
	if status == nil {
		return "", true
	}
	switch *status {
	case billing.StatusDue, billing.StatusOverdue, billing.StatusPaid:
		return *status, true
	}
	// Case included: OVERDUE is refused, not folded to overdue. A contract enum
	// is a set of exact strings, and guessing at a near miss is how the silent
	// wrong answer above got there in the first place.
	return "", false
}

// invoiceStatusRefusal is the one sentence all four reads give, so the console
// can render it without knowing which route it came from.
//
// The two exports used to have no 422 in the contract and the refusal was
// served through a response type of our own; the contract declares it now, and
// a test on their side fails if a route ever offers the status enum without
// declaring the refusal for it.
const invoiceStatusRefusal = "Status must be one of due, overdue or paid."

// ---------------------------------------------------------------- projection

func accountInvoiceResponse(row billing.Row, operatorView bool) gen.AccountInvoice {
	invoice := gen.AccountInvoice{
		Id:               uuid.MustParse(row.Invoice.ID),
		Number:           row.Invoice.Number,
		Currency:         gen.CurrencyCode(row.Invoice.Currency),
		TaxableMinor:     int(row.Invoice.TaxableMinor),
		TaxRatePercent:   row.Invoice.TaxRatePercent,
		TaxMinor:         int(row.Invoice.TaxMinor),
		TotalMinor:       int(row.Invoice.TotalMinor),
		ReceivedMinor:    int(row.ReceivedMinor),
		OutstandingMinor: int(row.OutstandingMinor),
		IssuedAt:         row.Invoice.IssuedAt,
		DueAt:            row.Invoice.DueAt,
		Status:           gen.AccountInvoiceStatus(row.Status),
		Reference:        row.Invoice.Reference,
	}
	// Withheld HERE, at the projection, rather than trusted to each caller.
	// A tenant must never read an internal note about their own account, and
	// the way to guarantee that is for the customer's path to have no branch
	// that could ever produce one.
	if operatorView {
		if row.Invoice.Note != "" {
			note := row.Invoice.Note
			invoice.Note = &note
		}
		if row.Invoice.IssuedBy != "" {
			by := row.Invoice.IssuedBy
			invoice.IssuedBy = &by
		}
	}
	return invoice
}

func currencyTotalsResponse(totals []billing.CurrencyTotals) []gen.CurrencyTotals {
	out := make([]gen.CurrencyTotals, 0, len(totals))
	for _, row := range totals {
		out = append(out, gen.CurrencyTotals{
			Currency:         gen.CurrencyCode(row.Currency),
			InvoicedMinor:    int(row.InvoicedMinor),
			ReceivedMinor:    int(row.ReceivedMinor),
			OutstandingMinor: int(row.OutstandingMinor),
			OverdueMinor:     int(row.OverdueMinor),
		})
	}
	return out
}

func tenantPaymentResponse(payment billing.Payment, settlement billing.Settlement) gen.TenantPayment {
	out := gen.TenantPayment{
		Id:          uuid.MustParse(payment.ID),
		Currency:    gen.CurrencyCode(payment.Currency),
		AmountMinor: int(payment.AmountMinor),
		Reference:   payment.Reference,
		ReceivedAt:  payment.ReceivedAt,
		RecordedAt:  payment.RecordedAt,
		// Required and nullable in the contract, so always present.
		Allocations:    []gen.PaymentAllocation{},
		UnappliedMinor: int(settlement.UnappliedByPayment[payment.ID]),
		VoidedAt:       payment.VoidedAt,
	}
	if payment.RecordedBy != "" {
		by := payment.RecordedBy
		out.RecordedBy = &by
	}
	if payment.VoidReason != "" {
		reason := payment.VoidReason
		out.VoidReason = &reason
	}
	if payment.VoidedBy != "" {
		by := payment.VoidedBy
		out.VoidedBy = &by
	}
	// A voided payment settled nothing, so it holds nothing back either. The
	// settlement pass already excluded it, which is why both of these are
	// naturally empty rather than cleared by hand.
	if payment.VoidedAt != nil {
		out.UnappliedMinor = 0
		return out
	}
	for _, allocation := range settlement.Allocations {
		if allocation.PaymentID != payment.ID {
			continue
		}
		out.Allocations = append(out.Allocations, gen.PaymentAllocation{
			InvoiceId:     uuid.MustParse(allocation.InvoiceID),
			InvoiceNumber: allocation.InvoiceNumber,
			AmountMinor:   int(allocation.AmountMinor),
		})
	}
	return out
}

// accountInvoiceRows loads a tenant's ledger and derives every invoice row,
// newest first, narrowed to the filter.
//
// Newest first because a statement is read from the top for what just happened,
// while the settlement pass works oldest first — the two orders answer
// different questions and are deliberately not the same.
func (s *Server) accountInvoiceRows(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID,
	filter string) ([]billing.Row, error) {

	ledger, err := store.LoadAccountLedger(ctx, pool, tenantID)
	if err != nil {
		return nil, err
	}
	settlement := billing.Settle(ledger.Invoices, ledger.Payments)
	rows := billing.Rows(ledger.Invoices, settlement, s.now().UTC())

	kept := rows[:0]
	for _, row := range rows {
		if billing.MatchesFilter(row.Status, filter) {
			kept = append(kept, row)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if !kept[i].Invoice.IssuedAt.Equal(kept[j].Invoice.IssuedAt) {
			return kept[i].Invoice.IssuedAt.After(kept[j].Invoice.IssuedAt)
		}
		return kept[i].Invoice.Number > kept[j].Invoice.Number
	})
	return kept, nil
}

// ------------------------------------------------------------ issuing credit

// CreditTenantWallet loads a tenant's wallet on account and raises the invoice
// they owe for it.
//
// The two are one act and share one transaction: credit with no invoice is
// money given away, an invoice with no credit is a bill for nothing.
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
			"A credit needs a currency, an amount and a reference.")), nil
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
			"reference must be your own reference for this credit, 6 to 64 characters.")), nil
	case len(note) > 200:
		return gen.CreditTenantWallet422JSONResponse(errorBody(codeValidation,
			"note must be at most 200 characters.")), nil
	case body.TaxRatePercent != nil && (*body.TaxRatePercent < 0 || *body.TaxRatePercent > 100):
		return gen.CreditTenantWallet422JSONResponse(errorBody(codeValidation,
			"taxRatePercent must be a whole percent from 0 to 100.")), nil
	}

	// An absent rate means "use the tenant country's own"; a supplied 0 means
	// "this credit carries no tax". They are different answers and conflating
	// them would either invent a charge or drop one.
	rate, _ := billing.TaxRateFor(tenant.Country)
	if body.TaxRatePercent != nil {
		rate = *body.TaxRatePercent
	}
	amount := int64(body.AmountMinor)
	taxMinor := billing.TaxOn(amount, rate)
	totalMinor := amount + taxMinor

	location := billing.BillingLocationFor(tenant.Country)
	dueAt := billing.MonthEnd(s.now(), location)
	if body.DueAt != nil {
		dueAt = *body.DueAt
	}

	// The limit check runs before any write: a refused credit must leave both
	// the wallet and the invoice list exactly as they were.
	//
	// ponytail: read-then-write, so two operators crediting the same tenant in
	// the same instant could both pass and take them one credit past the limit.
	// Deliberately not locked, unlike the duplicate-reference guard: booking one
	// transfer twice is money moved that nobody asked for, while a credit limit
	// is a business control an operator can lower again. Move this inside
	// IssueCreditOnAccount's advisory lock if that ever stops being true.
	if tenant.CreditLimitMinor != nil && tenant.CreditLimitCurrency != nil &&
		*tenant.CreditLimitCurrency == currency {

		ledger, err := store.LoadAccountLedger(ctx, s.operatorPool(), tenantID)
		if err != nil {
			return nil, err
		}
		settlement := billing.Settle(ledger.Invoices, ledger.Payments)
		outstanding := billing.OutstandingFor(ledger.Invoices, settlement, currency)
		unapplied := billing.UnappliedFor(ledger.Payments, settlement, currency)
		over := billing.CreditHeadroom(outstanding, unapplied, totalMinor, *tenant.CreditLimitMinor)
		if over > 0 {
			// Naming the shortfall rather than only refusing: an operator who
			// has to work out how much to reduce it by will guess, and guessing
			// at a credit limit is how one gets raised for the wrong reason.
			owed := outstanding - unapplied
			if owed < 0 {
				owed = 0
			}
			return gen.CreditTenantWallet422JSONResponse(errorBody(codeValidation,
				fmt.Sprintf("This would take %s %s past their %s credit limit. They already owe %s.",
					tenant.Name, money(over, currency),
					money(*tenant.CreditLimitMinor, currency), money(owed, currency)))), nil
		}
	}

	entry, invoice, err := store.IssueCreditOnAccount(ctx, s.operatorPool(),
		store.Identity{TenantID: tenantID}, store.IssueCredit{
			Currency: currency, AmountMinor: amount, TaxRatePercent: rate,
			TaxMinor: taxMinor, TotalMinor: totalMinor, DueAt: dueAt,
			Reference: reference, Note: note, IssuedBy: operator.Email,
		})
	if errors.Is(err, store.ErrConflict) {
		return gen.CreditTenantWallet409JSONResponse(errorBody(codeConflict,
			fmt.Sprintf("Reference %s has already been credited to %s.",
				reference, tenant.Name))), nil
	}
	if err != nil {
		return nil, err
	}

	detail := fmt.Sprintf("Credited %s to %s on invoice %s",
		money(amount, currency), tenant.Name, invoice.Number)
	if note != "" {
		detail += " — " + note
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, "wallet.credit",
		&tenantID, tenant.Name, reference, detail); err != nil {
		return nil, err
	}

	// The invoice is brand new, so nothing has been allocated against it yet —
	// except any money the tenant had already overpaid, which the settlement
	// pass claims the moment there is an invoice to claim it for. Deriving the
	// response rather than describing the row we just wrote is what makes that
	// visible immediately instead of on the next refresh.
	rows, err := s.accountInvoiceRows(ctx, s.operatorPool(), tenantID, "")
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Invoice.ID != invoice.ID {
			continue
		}
		return gen.CreditTenantWallet201JSONResponse(gen.IssueCreditResult{
			Entry:   ledgerEntryResponse(entry),
			Invoice: accountInvoiceResponse(row, true),
		}), nil
	}
	return nil, fmt.Errorf("api: invoice %s vanished between write and read", invoice.ID)
}

// money renders minor units for a sentence a human reads. Deliberately plain:
// the console formats for display, and this is for an error message and an
// audit line where being unambiguous beats being pretty.
func money(minor int64, currency string) string {
	whole := minor / 100
	part := minor % 100
	if part < 0 {
		part = -part
	}
	return fmt.Sprintf("%s %d.%02d", currency, whole, part)
}

// -------------------------------------------------------- operator: invoices

func (s *Server) ListTenantInvoices(ctx context.Context,
	request gen.ListTenantInvoicesRequestObject) (
	gen.ListTenantInvoicesResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.ListTenantInvoices401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	tenantID, found, err := s.operatorTenantID(ctx, request.Id.String())
	if err != nil {
		return nil, err
	}
	if !found {
		return gen.ListTenantInvoices404JSONResponse(
			errorBody(codeNotFound, "No such tenant.")), nil
	}
	page, ok := pageNumber(request.Params.Page)
	if !ok {
		return gen.ListTenantInvoices422JSONResponse(
			errorBody(codeValidation, pageTooLow)), nil
	}
	limit, limitOK := pageSize(request.Params.Limit)
	if !limitOK {
		return gen.ListTenantInvoices422JSONResponse(
			errorBody(codeValidation, limitOutOfRange)), nil
	}
	if limit == 0 {
		limit = 20
	}
	filter, statusOK := invoiceStatusFilter((*string)(request.Params.Status))
	if !statusOK {
		return gen.ListTenantInvoices422JSONResponse(
			errorBody(codeValidation, invoiceStatusRefusal)), nil
	}

	rows, err := s.accountInvoiceRows(ctx, s.operatorPool(), tenantID, filter)
	if err != nil {
		return nil, err
	}
	return gen.ListTenantInvoices200JSONResponse(
		invoicePage(rows, page, limit, true)), nil
}

// invoicePage slices one page out and totals the WHOLE set.
//
// The totals are computed over every row matching the filter, never over the
// slice: a total summed from the page on screen is wrong the moment a second
// page exists, and it looks authoritative while being wrong.
func invoicePage(rows []billing.Row, page, limit int, operatorView bool) gen.AccountInvoicePage {
	totals := billing.TotalsFor(rows)
	// Clamped at both ends: a page number large enough to overflow the
	// multiplication would otherwise come back negative and slice from the
	// wrong end of the set.
	offset := (page - 1) * limit
	if offset < 0 || offset > len(rows) {
		offset = len(rows)
	}
	end := offset + limit
	if end > len(rows) {
		end = len(rows)
	}
	entries := make([]gen.AccountInvoice, 0, end-offset)
	for _, row := range rows[offset:end] {
		entries = append(entries, accountInvoiceResponse(row, operatorView))
	}
	return gen.AccountInvoicePage{
		Entries: entries,
		Total:   len(rows),
		Totals:  currencyTotalsResponse(totals),
	}
}

// ---------------------------------------------------------- operator: export

type invoicesCSV struct{ csvDownload }

func (r invoicesCSV) VisitExportTenantInvoicesResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type accountInvoicesCSV struct{ csvDownload }

func (r accountInvoicesCSV) VisitExportAccountInvoicesResponse(w http.ResponseWriter) error {
	return r.write(w)
}

// invoiceCSVColumns is the customer's file. The operator's is these columns
// plus issuedBy and note, APPENDED rather than interleaved, so the customer's
// file is a strict prefix of the operator's and the two cannot drift apart
// column by column.
//
// Money goes out as minor units, never formatted: a spreadsheet must be able to
// sum the column, and "₹ 59,000.00" sums to nothing.
var invoiceCSVColumns = []string{
	"number", "issuedAt", "dueAt", "status", "currency", "taxableMinor",
	"taxRatePercent", "taxMinor", "totalMinor", "receivedMinor",
	"outstandingMinor", "reference",
}

var invoiceCSVOperatorColumns = []string{"issuedBy", "note"}

func invoiceCSVRow(row billing.Row, operatorView bool) []string {
	cells := []string{
		row.Invoice.Number,
		row.Invoice.IssuedAt.UTC().Format(time.RFC3339),
		row.Invoice.DueAt.UTC().Format(time.RFC3339),
		row.Status,
		row.Invoice.Currency,
		strconv.FormatInt(row.Invoice.TaxableMinor, 10),
		strconv.Itoa(row.Invoice.TaxRatePercent),
		strconv.FormatInt(row.Invoice.TaxMinor, 10),
		strconv.FormatInt(row.Invoice.TotalMinor, 10),
		strconv.FormatInt(row.ReceivedMinor, 10),
		strconv.FormatInt(row.OutstandingMinor, 10),
		row.Invoice.Reference,
	}
	if operatorView {
		cells = append(cells, row.Invoice.IssuedBy, row.Invoice.Note)
	}
	return cells
}

// streamInvoicesCSV writes the header and every row through a pipe, so the file
// costs one row of memory rather than all of them.
func streamInvoicesCSV(rows []billing.Row, operatorView bool) io.Reader {
	reader, writer := io.Pipe()
	go func() {
		out := csv.NewWriter(writer)
		header := invoiceCSVColumns
		if operatorView {
			header = append(append([]string{}, invoiceCSVColumns...), invoiceCSVOperatorColumns...)
		}
		err := out.Write(header)
		for _, row := range rows {
			if err != nil {
				break
			}
			err = out.Write(invoiceCSVRow(row, operatorView))
		}
		out.Flush()
		if err == nil {
			err = out.Error()
		}
		// A half-written file must not look complete. Closing with an error
		// breaks the response body rather than truncating it silently.
		_ = writer.CloseWithError(err)
	}()
	return reader
}

func (s *Server) ExportTenantInvoices(ctx context.Context,
	request gen.ExportTenantInvoicesRequestObject) (
	gen.ExportTenantInvoicesResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.ExportTenantInvoices401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	tenantID, found, err := s.operatorTenantID(ctx, request.Id.String())
	if err != nil {
		return nil, err
	}
	if !found {
		return gen.ExportTenantInvoices404JSONResponse(
			errorBody(codeNotFound, "No such tenant.")), nil
	}
	filter, statusOK := invoiceStatusFilter((*string)(request.Params.Status))
	if !statusOK {
		return gen.ExportTenantInvoices422JSONResponse(
			errorBody(codeValidation, invoiceStatusRefusal)), nil
	}
	// Built from the same rows the paged route serves, under the identical
	// filter, so the file and the screen that offered it can never disagree.
	rows, err := s.accountInvoiceRows(ctx, s.operatorPool(), tenantID, filter)
	if err != nil {
		return nil, err
	}
	name := filter
	if name == "" {
		name = "all"
	}
	return invoicesCSV{csvDownload{
		filename: fmt.Sprintf("invoices-%s-%s.csv", tenantID, name),
		body:     streamInvoicesCSV(rows, true),
	}}, nil
}

// -------------------------------------------------------- operator: payments

func (s *Server) ListTenantPayments(ctx context.Context,
	request gen.ListTenantPaymentsRequestObject) (
	gen.ListTenantPaymentsResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.ListTenantPayments401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	tenantID, found, err := s.operatorTenantID(ctx, request.Id.String())
	if err != nil {
		return nil, err
	}
	if !found {
		return gen.ListTenantPayments404JSONResponse(
			errorBody(codeNotFound, "No such tenant.")), nil
	}
	page, ok := pageNumber(request.Params.Page)
	if !ok {
		return gen.ListTenantPayments422JSONResponse(
			errorBody(codeValidation, pageTooLow)), nil
	}
	limit, limitOK := pageSize(request.Params.Limit)
	if !limitOK {
		return gen.ListTenantPayments422JSONResponse(
			errorBody(codeValidation, limitOutOfRange)), nil
	}
	if limit == 0 {
		limit = 20
	}

	payments, settlement, err := s.settledPayments(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	offset := (page - 1) * limit
	if offset < 0 || offset > len(payments) {
		offset = len(payments)
	}
	end := offset + limit
	if end > len(payments) {
		end = len(payments)
	}
	entries := make([]gen.TenantPayment, 0, end-offset)
	for _, payment := range payments[offset:end] {
		entries = append(entries, tenantPaymentResponse(payment, settlement))
	}
	return gen.ListTenantPayments200JSONResponse(gen.TenantPaymentPage{
		Entries: entries, Total: len(payments),
	}), nil
}

// settledPayments loads a tenant's payments with the settlement they produce,
// newest receipt first.
func (s *Server) settledPayments(ctx context.Context, tenantID uuid.UUID) (
	[]billing.Payment, billing.Settlement, error) {

	ledger, err := store.LoadAccountLedger(ctx, s.operatorPool(), tenantID)
	if err != nil {
		return nil, billing.Settlement{}, err
	}
	return ledger.Payments, billing.Settle(ledger.Invoices, ledger.Payments), nil
}

func (s *Server) RecordTenantPayment(ctx context.Context,
	request gen.RecordTenantPaymentRequestObject) (
	gen.RecordTenantPaymentResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.RecordTenantPayment401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	tenantID, found, err := s.operatorTenantID(ctx, request.Id.String())
	if err != nil {
		return nil, err
	}
	if !found {
		return gen.RecordTenantPayment404JSONResponse(
			errorBody(codeNotFound, "No such tenant.")), nil
	}
	tenant, err := store.GetTenant(ctx, s.operatorPool(), tenantID)
	if err != nil {
		return nil, err
	}

	body := request.Body
	if body == nil {
		return gen.RecordTenantPayment422JSONResponse(errorBody(codeValidation,
			"A payment needs a currency, an amount, the bank's reference and when it landed.")), nil
	}
	currency := string(body.Currency)
	reference := strings.TrimSpace(body.Reference)
	switch {
	case !oneOf(currency, validCurrencies):
		return gen.RecordTenantPayment422JSONResponse(errorBody(codeValidation,
			enumMessage("currency", validCurrencies))), nil
	case body.AmountMinor < 1 || body.AmountMinor > maxPaymentMinor:
		return gen.RecordTenantPayment422JSONResponse(errorBody(codeValidation,
			"amountMinor must be between 1 and 1,000,000,000 minor units.")), nil
	case len(reference) < 6 || len(reference) > 64:
		return gen.RecordTenantPayment422JSONResponse(errorBody(codeValidation,
			"reference must be the bank's transfer reference (UTR), 6 to 64 characters.")), nil
	case body.ReceivedAt.IsZero():
		return gen.RecordTenantPayment422JSONResponse(errorBody(codeValidation,
			"receivedAt must say when the money landed, per the bank.")), nil
	}

	id, err := store.RecordPayment(ctx, s.operatorPool(), store.Identity{TenantID: tenantID},
		billing.Payment{
			Currency: currency, AmountMinor: int64(body.AmountMinor),
			Reference: reference, ReceivedAt: body.ReceivedAt, RecordedBy: operator.Email,
		})
	if errors.Is(err, store.ErrConflict) {
		return gen.RecordTenantPayment409JSONResponse(errorBody(codeConflict,
			fmt.Sprintf("Reference %s has already been recorded for %s.",
				reference, tenant.Name))), nil
	}
	if err != nil {
		return nil, err
	}

	payments, settlement, err := s.settledPayments(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for _, payment := range payments {
		if payment.ID != id {
			continue
		}
		settled := tenantPaymentResponse(payment, settlement)
		what := "no invoice"
		if len(settled.Allocations) > 0 {
			numbers := make([]string, 0, len(settled.Allocations))
			for _, allocation := range settled.Allocations {
				numbers = append(numbers, allocation.InvoiceNumber)
			}
			what = strings.Join(numbers, ", ")
		}
		if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, "payment.record",
			&tenantID, tenant.Name, reference,
			fmt.Sprintf("Recorded %s from %s, settling %s",
				money(int64(body.AmountMinor), currency), tenant.Name, what)); err != nil {
			return nil, err
		}
		return gen.RecordTenantPayment201JSONResponse(settled), nil
	}
	return nil, fmt.Errorf("api: payment %s vanished between write and read", id)
}

func (s *Server) VoidTenantPayment(ctx context.Context,
	request gen.VoidTenantPaymentRequestObject) (
	gen.VoidTenantPaymentResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.VoidTenantPayment401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	tenantID, found, err := s.operatorTenantID(ctx, request.Id.String())
	if err != nil {
		return nil, err
	}
	if !found {
		return gen.VoidTenantPayment404JSONResponse(
			errorBody(codeNotFound, "No such tenant.")), nil
	}
	tenant, err := store.GetTenant(ctx, s.operatorPool(), tenantID)
	if err != nil {
		return nil, err
	}

	reason := ""
	if request.Body != nil {
		reason = strings.TrimSpace(request.Body.Reason)
	}
	// A payment that reversed itself with no stated reason is unauditable, and
	// this is money. Checked before the already-voided case only in that both
	// precede any write.
	if len(reason) < 3 || len(reason) > 200 {
		return gen.VoidTenantPayment422JSONResponse(
			errorBody(codeValidation, "A reason of 3 to 200 characters is required.")), nil
	}

	err = store.VoidPayment(ctx, s.operatorPool(), store.Identity{TenantID: tenantID},
		request.PaymentId, reason, operator.Email)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return gen.VoidTenantPayment404JSONResponse(
			errorBody(codeNotFound, "No such payment.")), nil
	case errors.Is(err, store.ErrConflict):
		return gen.VoidTenantPayment409JSONResponse(
			errorBody(codeConflict, "This payment has already been voided.")), nil
	case err != nil:
		return nil, err
	}

	payments, settlement, err := s.settledPayments(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for _, payment := range payments {
		if payment.ID != request.PaymentId.String() {
			continue
		}
		if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, "payment.void",
			&tenantID, tenant.Name, payment.Reference,
			fmt.Sprintf("Voided %s from %s: %s",
				money(payment.AmountMinor, payment.Currency), tenant.Name, reason)); err != nil {
			return nil, err
		}
		return gen.VoidTenantPayment200JSONResponse(
			tenantPaymentResponse(payment, settlement)), nil
	}
	return nil, fmt.Errorf("api: payment %s vanished after voiding", request.PaymentId)
}

// ----------------------------------------------------- operator: credit limit

func (s *Server) SetTenantCreditLimit(ctx context.Context,
	request gen.SetTenantCreditLimitRequestObject) (
	gen.SetTenantCreditLimitResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.SetTenantCreditLimit401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	tenantID, found, err := s.operatorTenantID(ctx, request.Id.String())
	if err != nil {
		return nil, err
	}
	if !found {
		return gen.SetTenantCreditLimit404JSONResponse(
			errorBody(codeNotFound, "No such tenant.")), nil
	}
	tenant, err := store.GetTenant(ctx, s.operatorPool(), tenantID)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return gen.SetTenantCreditLimit422JSONResponse(errorBody(codeValidation,
			"creditLimitMinor is required; null removes the limit.")), nil
	}

	var limit *int64
	if request.Body.CreditLimitMinor != nil {
		value := int64(*request.Body.CreditLimitMinor)
		if value < 0 || value > maxOperatorCreditMinor {
			return gen.SetTenantCreditLimit422JSONResponse(errorBody(codeValidation,
				"creditLimitMinor must be a whole number of minor units from 0 to "+
					"1,000,000,000, or null for no limit.")), nil
		}
		limit = &value
	}

	// The tenant's own country decides the currency: a limit denominated in
	// money they are not billed in would cap nothing. A country whose regime we
	// have not confirmed gets no limit rather than a guessed one.
	currency := ""
	if regime, known := compliance.For(tenant.Country); known {
		currency = regime.Currency()
	}
	if limit != nil && currency == "" {
		return gen.SetTenantCreditLimit422JSONResponse(errorBody(codeValidation,
			fmt.Sprintf("We have no confirmed currency for %s, so a credit limit there would "+
				"cap nothing. Set the tenant's country first.", tenant.Country))), nil
	}

	if err := store.SetCreditLimit(ctx, s.operatorPool(), tenantID, limit, currency); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return gen.SetTenantCreditLimit404JSONResponse(
				errorBody(codeNotFound, "No such tenant.")), nil
		}
		return nil, err
	}

	detail := fmt.Sprintf("Removed %s's credit limit", tenant.Name)
	if limit != nil {
		detail = fmt.Sprintf("Set %s's credit limit to %s", tenant.Name, money(*limit, currency))
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, "tenant.credit_limit",
		&tenantID, tenant.Name, tenant.Name, detail); err != nil {
		return nil, err
	}

	updated, err := store.GetTenant(ctx, s.operatorPool(), tenantID)
	if err != nil {
		return nil, err
	}
	return gen.SetTenantCreditLimit200JSONResponse(toTenantDetail(updated)), nil
}

// ------------------------------------------------------- customer: invoices

func (s *Server) ListAccountInvoices(ctx context.Context,
	request gen.ListAccountInvoicesRequestObject) (
	gen.ListAccountInvoicesResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.ListAccountInvoices401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	page, pageOK := pageNumber(request.Params.Page)
	if !pageOK {
		return gen.ListAccountInvoices422JSONResponse(
			errorBody(codeValidation, pageTooLow)), nil
	}
	limit, limitOK := pageSize(request.Params.Limit)
	if !limitOK {
		return gen.ListAccountInvoices422JSONResponse(
			errorBody(codeValidation, limitOutOfRange)), nil
	}
	if limit == 0 {
		limit = 20
	}
	filter, statusOK := invoiceStatusFilter((*string)(request.Params.Status))
	if !statusOK {
		return gen.ListAccountInvoices422JSONResponse(
			errorBody(codeValidation, invoiceStatusRefusal)), nil
	}

	// The tenant's own pool, so row-level security is the second lock on this
	// door — a bug in the identity check still cannot read another tenant's
	// invoices.
	rows, err := s.accountInvoiceRows(ctx, s.DB, identity.TenantID, filter)
	if err != nil {
		return nil, err
	}
	return gen.ListAccountInvoices200JSONResponse(
		invoicePage(rows, page, limit, false)), nil
}

func (s *Server) ExportAccountInvoices(ctx context.Context,
	request gen.ExportAccountInvoicesRequestObject) (
	gen.ExportAccountInvoicesResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.ExportAccountInvoices401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	filter, statusOK := invoiceStatusFilter((*string)(request.Params.Status))
	if !statusOK {
		return gen.ExportAccountInvoices422JSONResponse(
			errorBody(codeValidation, invoiceStatusRefusal)), nil
	}
	rows, err := s.accountInvoiceRows(ctx, s.DB, identity.TenantID, filter)
	if err != nil {
		return nil, err
	}
	name := filter
	if name == "" {
		name = "all"
	}
	return accountInvoicesCSV{csvDownload{
		filename: fmt.Sprintf("invoices-%s.csv", name),
		body:     streamInvoicesCSV(rows, false),
	}}, nil
}

// operatorTenantID parses a tenant id from the path and confirms the tenant
// exists, which is the same two refusals every operator route here starts with.
func (s *Server) operatorTenantID(ctx context.Context, raw string) (uuid.UUID, bool, error) {
	tenantID, valid := parsePathID(raw)
	if !valid {
		return uuid.UUID{}, false, nil
	}
	if _, err := store.GetTenant(ctx, s.operatorPool(), tenantID); errors.Is(err, store.ErrNotFound) {
		return uuid.UUID{}, false, nil
	} else if err != nil {
		return uuid.UUID{}, false, err
	}
	return tenantID, true, nil
}
