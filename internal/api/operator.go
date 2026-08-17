package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/saeedafri/sms-be/internal/domain/auth"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// The operator console: platform staff working across every tenant.
//
// Two rules hold everywhere in this file.
//
// First, an operator identity is resolved from operator_sessions and never from
// a tenant session. A tenant user must not be able to reach these endpoints by
// any path, including presenting a perfectly valid tenant token — so a tenant
// token resolves to no operator at all rather than to a low-privileged one.
//
// Second, every action that changes what a customer experiences writes an audit
// entry naming the operator who did it. The audit table is append-only at the
// database level, so that record cannot be quietly revised afterwards.

type operatorKey struct{}

// operatorPool is the cross-tenant pool, falling back to the ordinary one when
// no separate pool was configured. The fallback keeps tests and single-pool
// deployments working; it simply sees less, which fails visibly as empty
// results rather than silently as wrong ones.
func (s *Server) operatorPool() *pgxpool.Pool {
	if s.OperatorDB != nil {
		return s.OperatorDB
	}
	return s.DB
}

func operatorFrom(ctx context.Context) (store.OperatorIdentity, bool) {
	identity, ok := ctx.Value(operatorKey{}).(store.OperatorIdentity)
	return identity, ok
}

// requireOperator is the guard every operator handler starts with.
func (s *Server) requireOperator(ctx context.Context) (store.OperatorIdentity, error) {
	identity, ok := operatorFrom(ctx)
	if !ok {
		return store.OperatorIdentity{}, errUnauthenticated
	}
	return identity, nil
}

func (s *Server) OperatorLogin(ctx context.Context, request gen.OperatorLoginRequestObject) (
	gen.OperatorLoginResponseObject, error) {

	email := strings.ToLower(strings.TrimSpace(string(request.Body.Email)))
	unauthorized := gen.OperatorLogin401JSONResponse(
		errorBody(codeUnauthenticated, "Incorrect email address or password."))

	operator, hash, err := store.FindOperatorByEmail(ctx, s.DB, email)
	if errors.Is(err, store.ErrNotFound) {
		// Same constant-time shape as tenant login: an unknown operator address
		// must cost the same as a known one with the wrong password, or the
		// timing difference enumerates staff accounts.
		auth.VerifyPassword(auth.DummyHash, request.Body.Password)
		return unauthorized, nil
	}
	if err != nil {
		return nil, err
	}
	if !auth.VerifyPassword(hash, request.Body.Password) {
		return unauthorized, nil
	}

	raw, tokenHash, err := auth.NewToken()
	if err != nil {
		return nil, err
	}
	expiresAt, err := store.CreateOperatorSession(ctx, s.DB, operator.OperatorID,
		tokenHash, sessionLifetime)
	if err != nil {
		return nil, err
	}
	return gen.OperatorLogin200JSONResponse{Token: raw, ExpiresAt: expiresAt}, nil
}

func (s *Server) GetOperatorMe(ctx context.Context, _ gen.GetOperatorMeRequestObject) (
	gen.GetOperatorMeResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.GetOperatorMe401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	return gen.GetOperatorMe200JSONResponse{
		OperatorId: operator.OperatorID,
		Name:       operator.Name,
		Email:      openapi_types.Email(operator.Email),
		Role:       gen.OperatorMeRole(operator.Role),
	}, nil
}

// tenantStanding collapses the tenant's status columns into the single value
// the console renders. Suspended outranks throttled: a suspended tenant is not
// sending at all, so showing "throttled" would understate what was done to it.
func tenantStanding(tenant store.OperatorTenant) string {
	switch {
	case tenant.Status == "suspended":
		return "suspended"
	case tenant.ThrottledAt != nil:
		return "throttled"
	default:
		return "active"
	}
}

func toOperatorTenant(tenant store.OperatorTenant) gen.Tenant {
	return gen.Tenant{
		Id:        tenant.ID,
		Name:      tenant.Name,
		Country:   gen.CountryCode(tenant.Country),
		CreatedAt: tenant.CreatedAt,
		// Plan is not modelled yet; the console shows it as a column, and an
		// empty string reads as "unknown" rather than inventing a tier.
		Plan:   "standard",
		Status: gen.TenantStatus(tenantStanding(tenant)),
	}
}

func (s *Server) GetTenants(ctx context.Context, request gen.GetTenantsRequestObject) (
	gen.GetTenantsResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetTenants401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	// The request object used to be discarded entirely, so every filter the
	// console sent was ignored and `?status=pending&country=US` answered with
	// all eight tenants — including active Indian ones. A filter that silently
	// does nothing is worse than one that errors: the operator reads the
	// result as "these are the pending US tenants" and acts on it.
	var status, country *string
	if request.Params.Status != nil {
		value := string(*request.Params.Status)
		status = &value
	}
	if request.Params.Country != nil {
		value := string(*request.Params.Country)
		country = &value
	}
	tenants, err := store.ListTenants(ctx, s.operatorPool(), status, country)
	if err != nil {
		return nil, err
	}
	out := make([]gen.Tenant, 0, len(tenants))
	for _, tenant := range tenants {
		out = append(out, toOperatorTenant(tenant))
	}
	return gen.GetTenants200JSONResponse{Tenants: out}, nil
}

// operatorAction applies a state change to a tenant and records who did it.
// Every mutating tenant endpoint routes through here so no path can change a
// customer's standing without leaving a trace.
func (s *Server) operatorAction(ctx context.Context, id string, action string,
	apply func(tenantID uuid.UUID) error) error {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return err
	}
	tenantID, valid := parsePathID(id)
	if !valid {
		return store.ErrNotFound
	}
	tenant, err := store.GetTenant(ctx, s.operatorPool(), tenantID)
	if err != nil {
		return err
	}
	if err := apply(tenantID); err != nil {
		return err
	}
	return store.RecordOperatorAction(ctx, s.DB, operator.Email, action,
		&tenantID, tenant.Name, tenant.Name, "")
}

func toTenantDetail(tenant store.OperatorTenant) gen.TenantDetail {
	return gen.TenantDetail{
		Id: tenant.ID, Name: tenant.Name, Country: gen.CountryCode(tenant.Country),
		CreatedAt: tenant.CreatedAt, Plan: "standard",
		Status:     gen.TenantStatus(tenantStanding(tenant)),
		FlaggedAt:  tenant.FlaggedAt,
		FlagReason: tenant.FlagReason,
		// Compliance standing per country is derived from registrations; an
		// empty list means "nothing submitted", which is the honest answer for a
		// tenant that has not started onboarding.
		Compliance: []gen.TenantComplianceSummary{},
		Usage: gen.TenantUsageSnapshot{
			MessagesSent30d: tenant.MessagesSent30d,
			LastActivityAt:  tenant.LastActivityAt,
		},
	}
}

func (s *Server) GetTenantDetail(ctx context.Context, request gen.GetTenantDetailRequestObject) (
	gen.GetTenantDetailResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetTenantDetail401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	id, valid := parsePathID(request.Id)
	if !valid {
		return gen.GetTenantDetail404JSONResponse(errorBody("not_found", "No such tenant.")), nil
	}
	tenant, err := store.GetTenant(ctx, s.operatorPool(), id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.GetTenantDetail404JSONResponse(errorBody("not_found", "No such tenant.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetTenantDetail200JSONResponse(toTenantDetail(tenant)), nil
}

func (s *Server) SuspendTenant(ctx context.Context, request gen.SuspendTenantRequestObject) (
	gen.SuspendTenantResponseObject, error) {

	tenant, err := s.applyTenantChange(ctx, request.Id, "tenant.suspend",
		func(id uuid.UUID) error {
			// Suspending clears any abuse flag: the flag exists to mark a tenant
			// as needing a decision, and suspension IS the decision. Leaving it
			// set would keep the tenant in the review queue forever.
			if err := store.SetTenantFlag(ctx, s.operatorPool(), id, ""); err != nil {
				return err
			}
			return store.SetTenantStatus(ctx, s.operatorPool(), id, "suspended")
		})
	if err != nil {
		return tenantActionError(err, func(body gen.Error) gen.SuspendTenantResponseObject {
			return gen.SuspendTenant404JSONResponse(body)
		})
	}
	return gen.SuspendTenant200JSONResponse(toTenantDetail(tenant)), nil
}

func (s *Server) ReinstateTenant(ctx context.Context, request gen.ReinstateTenantRequestObject) (
	gen.ReinstateTenantResponseObject, error) {

	tenant, err := s.applyTenantChange(ctx, request.Id, "tenant.reinstate",
		func(id uuid.UUID) error {
			if err := store.SetTenantThrottled(ctx, s.operatorPool(), id, false); err != nil {
				return err
			}
			return store.SetTenantStatus(ctx, s.operatorPool(), id, "active")
		})
	if err != nil {
		return tenantActionError(err, func(body gen.Error) gen.ReinstateTenantResponseObject {
			return gen.ReinstateTenant404JSONResponse(body)
		})
	}
	return gen.ReinstateTenant200JSONResponse(toTenantDetail(tenant)), nil
}

func (s *Server) ThrottleTenant(ctx context.Context, request gen.ThrottleTenantRequestObject) (
	gen.ThrottleTenantResponseObject, error) {

	tenant, err := s.applyTenantChange(ctx, request.Id, "tenant.throttle",
		func(id uuid.UUID) error {
			if err := store.SetTenantFlag(ctx, s.operatorPool(), id, ""); err != nil {
				return err
			}
			return store.SetTenantThrottled(ctx, s.operatorPool(), id, true)
		})
	if err != nil {
		return tenantActionError(err, func(body gen.Error) gen.ThrottleTenantResponseObject {
			return gen.ThrottleTenant404JSONResponse(body)
		})
	}
	return gen.ThrottleTenant200JSONResponse(toTenantDetail(tenant)), nil
}

func (s *Server) FlagTenantForAbuse(ctx context.Context,
	request gen.FlagTenantForAbuseRequestObject) (gen.FlagTenantForAbuseResponseObject, error) {

	reason := "Flagged for abuse review."
	if request.Body != nil && request.Body.Reason != "" {
		reason = request.Body.Reason
	}
	tenant, err := s.applyTenantChange(ctx, request.Id, "tenant.flag_abuse",
		func(id uuid.UUID) error {
			// Flagging twice must not move the timestamp: the contract says
			// flagging is idempotent, and an operator re-flagging would
			// otherwise reset how long the case has been open.
			existing, err := store.GetTenant(ctx, s.operatorPool(), id)
			if err != nil {
				return err
			}
			if existing.FlaggedAt != nil {
				return nil
			}
			return store.SetTenantFlag(ctx, s.operatorPool(), id, reason)
		})
	if err != nil {
		return tenantActionError(err, func(body gen.Error) gen.FlagTenantForAbuseResponseObject {
			return gen.FlagTenantForAbuse404JSONResponse(body)
		})
	}
	return gen.FlagTenantForAbuse200JSONResponse(toTenantDetail(tenant)), nil
}

func (s *Server) DismissFlag(ctx context.Context, request gen.DismissFlagRequestObject) (
	gen.DismissFlagResponseObject, error) {

	tenant, err := s.applyTenantChange(ctx, request.Id, "tenant.dismiss_flag",
		func(id uuid.UUID) error { return store.SetTenantFlag(ctx, s.operatorPool(), id, "") })
	if err != nil {
		return tenantActionError(err, func(body gen.Error) gen.DismissFlagResponseObject {
			return gen.DismissFlag404JSONResponse(body)
		})
	}
	return gen.DismissFlag200JSONResponse(toTenantDetail(tenant)), nil
}

// applyTenantChange runs a state change, records it, and returns the tenant as
// it now stands. Reading the tenant back rather than patching the copy in
// memory means the response cannot disagree with the database.
func (s *Server) applyTenantChange(ctx context.Context, id string, action string,
	apply func(tenantID uuid.UUID) error) (store.OperatorTenant, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return store.OperatorTenant{}, err
	}
	tenantID, valid := parsePathID(id)
	if !valid {
		return store.OperatorTenant{}, store.ErrNotFound
	}
	before, err := store.GetTenant(ctx, s.operatorPool(), tenantID)
	if err != nil {
		return store.OperatorTenant{}, err
	}
	if err := apply(tenantID); err != nil {
		return store.OperatorTenant{}, err
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, action,
		&tenantID, before.Name, before.Name, auditDetail(action, before.Name)); err != nil {
		return store.OperatorTenant{}, err
	}
	return store.GetTenant(ctx, s.operatorPool(), tenantID)
}

// tenantActionError maps the two failures every tenant action shares. A free
// function rather than a method because Go methods cannot take type parameters.
func tenantActionError[T any](err error, notFound func(gen.Error) T) (T, error) {
	var zero T
	if errors.Is(err, store.ErrNotFound) {
		return notFound(errorBody("not_found", "No such tenant.")), nil
	}
	return zero, err
}

// routeCostReference is the cheapest ACTIVE route for a corridor — the number
// an operator prices against. Disabled routes are excluded deliberately: a
// cheap route nobody is allowed to use is not a cost floor, and including it
// would make a margin look healthier than it is.
func routeCostReference(routes []store.Route, country, channel string) *int {
	var lowest *int
	for _, route := range routes {
		if route.Country != country || route.Channel != channel || route.Status != "enabled" {
			continue
		}
		cost := int(route.CostPerSegmentMinor)
		if lowest == nil || cost < *lowest {
			lowest = &cost
		}
	}
	return lowest
}

func (s *Server) GetRateCard(ctx context.Context, _ gen.GetRateCardRequestObject) (
	gen.GetRateCardResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetRateCard401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	defaults, err := store.ListPricingRates(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	// The OPERATOR pool, not s.DB.
	//
	// rate_overrides itself has no row-level security, so this looked safe — but
	// the query JOINs tenants to get the customer's name, and tenants is
	// ENABLE + FORCE ROW LEVEL SECURITY. On the tenant pool, with no
	// app.tenant_id set, tenants matches nothing and the join silently returns
	// zero rows.
	//
	// The effect was that every tenant rate override was invisible: creating one
	// answered 201 and stored the row, the rate card then reported
	// "overrides": [], and the console showed the customer still on the default
	// price. An operator could set a negotiated rate, see no error, and have it
	// never appear — while the estimator quietly billed the default.
	//
	// ListPricingRates above is fine on s.DB precisely because pricing_rates has
	// no RLS AND no join to a table that does.
	overrides, err := store.ListRateOverrides(ctx, s.operatorPool())
	if err != nil {
		return nil, err
	}
	// The whole routing table: this response is a rate card, not a filtered view.
	routes, err := store.ListRoutes(ctx, s.DB, nil, nil)
	if err != nil {
		return nil, err
	}

	card := gen.RateCard{
		Defaults:  make([]gen.RateCardRow, 0, len(defaults)),
		Overrides: make([]gen.RateOverrideRow, 0, len(overrides)),
	}
	for _, rate := range defaults {
		row := gen.RateCardRow{
			Country: gen.CountryCode(rate.Country), Channel: gen.ChannelId(rate.Channel),
			PerSegmentMinor:    int(rate.PerSegmentMinor),
			Currency:           gen.CurrencyCode(rate.Currency),
			CostReferenceMinor: routeCostReference(routes, rate.Country, rate.Channel),
		}
		// WhatsApp, Email and Voice price per category; SMS and RCS price per
		// channel and carry an empty category, which stays absent rather than
		// being sent as "".
		//
		// This was dropped entirely before, so the card showed two "IN EMAIL"
		// rows at 8 and 12 with nothing to distinguish them — the reader could
		// see two different prices for what looked like the same corridor and
		// had no way to tell which applied to their message.
		if rate.Category != "" {
			category := gen.TemplateCategory(rate.Category)
			// Guarded for the same reason the sender quality rating is: the
			// frontend resolves this against a fixed registry and throws on an
			// unknown value, taking the whole rate card with it. A row that
			// somehow holds a bad category loses its label, not the page.
			if category.Valid() {
				var wrapper gen.RateCardRow_Category
				_ = wrapper.FromTemplateCategory(category)
				row.Category = &wrapper
			}
		}
		card.Defaults = append(card.Defaults, row)
	}
	for _, override := range overrides {
		card.Overrides = append(card.Overrides, gen.RateOverrideRow{
			Id: override.ID.String(), TenantId: override.TenantID,
			TenantName:         override.TenantName,
			Country:            gen.CountryCode(override.Country),
			Channel:            gen.ChannelId(override.Channel),
			PerSegmentMinor:    int(override.PerSegmentMinor),
			Currency:           gen.CurrencyCode(override.Currency),
			UpdatedAt:          override.UpdatedAt,
			CostReferenceMinor: routeCostReference(routes, override.Country, override.Channel),
		})
	}
	return gen.GetRateCard200JSONResponse(card), nil
}

func (s *Server) GetRoutes(ctx context.Context, request gen.GetRoutesRequestObject) (
	gen.GetRoutesResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetRoutes401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	var routeCountry, routeChannel *string
	if request.Params.Country != nil {
		value := string(*request.Params.Country)
		routeCountry = &value
	}
	if request.Params.Channel != nil {
		value := string(*request.Params.Channel)
		routeChannel = &value
	}
	routes, err := store.ListRoutes(ctx, s.DB, routeCountry, routeChannel)
	if err != nil {
		return nil, err
	}
	out := make([]gen.Route, 0, len(routes))
	for _, route := range routes {
		out = append(out, gen.Route{
			Id: route.ID, Country: gen.CountryCode(route.Country),
			Channel: gen.ChannelId(route.Channel), Carrier: gen.CarrierId(route.Carrier),
			Label: route.Label, Priority: route.Priority,
			ComplianceStanding:  gen.ComplianceStanding(route.ComplianceStanding),
			CostPerSegmentMinor: int(route.CostPerSegmentMinor),
			Currency:            gen.CurrencyCode(route.Currency),
			Status:              gen.RouteStatus(route.Status),
		})
	}
	return gen.GetRoutes200JSONResponse{Routes: out}, nil
}

func (s *Server) GetAuditLog(ctx context.Context, request gen.GetAuditLogRequestObject) (
	gen.GetAuditLogResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetAuditLog401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	var auditAction *string
	if request.Params.Action != nil {
		value := string(*request.Params.Action)
		auditAction = &value
	}
	entries, err := store.ListAuditLog(ctx, s.DB, 100, request.Params.TenantId, auditAction)
	if err != nil {
		return nil, err
	}
	out := make([]gen.AuditLogEntry, 0, len(entries))
	for _, entry := range entries {
		row := gen.AuditLogEntry{
			Id: entry.ID.String(), OccurredAt: entry.OccurredAt,
			Actor: entry.Actor, Action: gen.AuditAction(entry.Action),
		}
		if entry.TenantID != nil {
			row.TenantId = entry.TenantID
		}
		if entry.TenantName != nil {
			row.TenantName = entry.TenantName
		}
		if entry.TargetLabel != nil {
			row.TargetLabel = *entry.TargetLabel
		}
		if entry.Detail != nil {
			row.Detail = *entry.Detail
		}
		out = append(out, row)
	}
	return gen.GetAuditLog200JSONResponse{Entries: out}, nil
}

// routeAction changes a route and records who did it. Route order decides which
// carrier a tenant's traffic actually takes, so every change is auditable for
// the same reason tenant suspensions are.
func (s *Server) routeAction(ctx context.Context, id string, action string,
	apply func(routeID uuid.UUID) error) error {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return err
	}
	routeID, valid := parsePathID(id)
	if !valid {
		return store.ErrNotFound
	}
	if err := apply(routeID); err != nil {
		return err
	}
	return store.RecordOperatorAction(ctx, s.DB, operator.Email, action,
		nil, "", routeID.String(), "")
}

// routeAfterChange re-reads the route so the response reflects the database
// rather than what the handler believes it just wrote — priorities in
// particular are rewritten by a neighbour swap.
func (s *Server) routeAfterChange(ctx context.Context, id string) (gen.Route, error) {
	routeID, valid := parsePathID(id)
	if !valid {
		return gen.Route{}, store.ErrNotFound
	}
	route, err := store.GetRoute(ctx, s.DB, routeID)
	if err != nil {
		return gen.Route{}, err
	}
	return toGenRoute(route), nil
}

func toGenRoute(route store.Route) gen.Route {
	return gen.Route{
		Id: route.ID, Country: gen.CountryCode(route.Country),
		Channel: gen.ChannelId(route.Channel), Carrier: gen.CarrierId(route.Carrier),
		Label: route.Label, Priority: route.Priority,
		ComplianceStanding:  gen.ComplianceStanding(route.ComplianceStanding),
		CostPerSegmentMinor: int(route.CostPerSegmentMinor),
		Currency:            gen.CurrencyCode(route.Currency),
		Status:              gen.RouteStatus(route.Status),
	}
}

func (s *Server) EnableRoute(ctx context.Context, request gen.EnableRouteRequestObject) (
	gen.EnableRouteResponseObject, error) {

	if err := s.routeAction(ctx, request.Id, "route.enable", func(id uuid.UUID) error {
		// "active", not "enabled". The contract's RouteStatus enum is
		// active/disabled; "enabled" was an invented value the console could
		// not resolve, so an enabled route showed a blank status cell.
		return store.SetRouteStatus(ctx, s.DB, id, string(gen.RouteStatusActive))
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return gen.EnableRoute404JSONResponse(errorBody("not_found", "No such route.")), nil
		}
		return nil, err
	}
	route, err := s.routeAfterChange(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	return gen.EnableRoute200JSONResponse(route), nil
}

func (s *Server) DisableRoute(ctx context.Context, request gen.DisableRouteRequestObject) (
	gen.DisableRouteResponseObject, error) {

	if err := s.routeAction(ctx, request.Id, "route.disable", func(id uuid.UUID) error {
		return store.SetRouteStatus(ctx, s.DB, id, string(gen.RouteStatusDisabled))
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return gen.DisableRoute404JSONResponse(errorBody("not_found", "No such route.")), nil
		}
		return nil, err
	}
	route, err := s.routeAfterChange(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	return gen.DisableRoute200JSONResponse(route), nil
}

func (s *Server) MoveRouteUp(ctx context.Context, request gen.MoveRouteUpRequestObject) (
	gen.MoveRouteUpResponseObject, error) {

	if err := s.routeAction(ctx, request.Id, "route.move_up", func(id uuid.UUID) error {
		return store.MoveRoute(ctx, s.DB, id, true)
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return gen.MoveRouteUp404JSONResponse(errorBody("not_found", "No such route.")), nil
		}
		return nil, err
	}
	route, err := s.routeAfterChange(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	return gen.MoveRouteUp200JSONResponse(route), nil
}

func (s *Server) MoveRouteDown(ctx context.Context, request gen.MoveRouteDownRequestObject) (
	gen.MoveRouteDownResponseObject, error) {

	if err := s.routeAction(ctx, request.Id, "route.move_down", func(id uuid.UUID) error {
		return store.MoveRoute(ctx, s.DB, id, false)
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return gen.MoveRouteDown404JSONResponse(errorBody("not_found", "No such route.")), nil
		}
		return nil, err
	}
	route, err := s.routeAfterChange(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	return gen.MoveRouteDown200JSONResponse(route), nil
}

func (s *Server) UpdateDefaultRate(ctx context.Context, request gen.UpdateDefaultRateRequestObject) (
	gen.UpdateDefaultRateResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.UpdateDefaultRate401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	// A negative price would pay customers to send. The database carries the
	// same constraint; this exists so the caller gets a validation error rather
	// than a 500 from a constraint violation.
	if request.Body.PerSegmentMinor < 0 {
		return gen.UpdateDefaultRate422JSONResponse(errorBody(codeValidation,
			"A rate cannot be negative.")), nil
	}
	// Category is a nullable union in the contract. An absent or unreadable one
	// means "applies to every category", which is stored as the empty string —
	// the same value the default rate rows already use.
	category := ""
	if request.Body.Category != nil {
		if parsed, err := request.Body.Category.AsTemplateCategory(); err == nil {
			category = string(parsed)
		}
	}
	rate, err := store.UpsertPricingRate(ctx, s.DB, string(request.Body.Country),
		string(request.Body.Channel), category, int64(request.Body.PerSegmentMinor))
	if err != nil {
		return nil, err
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, "rate.default_update",
		nil, "", string(request.Body.Country)+" "+string(request.Body.Channel),
		""); err != nil {
		return nil, err
	}

	// The whole routing table: this response is a rate card, not a filtered view.
	routes, err := store.ListRoutes(ctx, s.DB, nil, nil)
	if err != nil {
		return nil, err
	}
	return gen.UpdateDefaultRate200JSONResponse{
		Country: gen.CountryCode(rate.Country), Channel: gen.ChannelId(rate.Channel),
		PerSegmentMinor:    int(rate.PerSegmentMinor),
		Currency:           gen.CurrencyCode(rate.Currency),
		CostReferenceMinor: routeCostReference(routes, rate.Country, rate.Channel),
	}, nil
}

func toTenantRateOverride(override store.RateOverride) gen.TenantRateOverride {
	return gen.TenantRateOverride{
		Id: override.ID.String(), TenantId: override.TenantID,
		TenantName:      override.TenantName,
		Country:         gen.CountryCode(override.Country),
		Channel:         gen.ChannelId(override.Channel),
		PerSegmentMinor: int(override.PerSegmentMinor),
		Currency:        gen.CurrencyCode(override.Currency),
		UpdatedAt:       override.UpdatedAt,
	}
}

// findOverride re-reads an override by id. Both mutating endpoints answer with
// the tenant NAME, which the overrides table does not carry — it comes from the
// join, so the row has to be read back rather than assembled in memory.
func (s *Server) findOverride(ctx context.Context, id uuid.UUID) (store.RateOverride, error) {
	// Operator pool, for the same reason GetRateCard uses it: the join to
	// tenants is invisible to the tenant pool, so this returned ErrNotFound for
	// an override that had just been written successfully.
	overrides, err := store.ListRateOverrides(ctx, s.operatorPool())
	if err != nil {
		return store.RateOverride{}, err
	}
	for _, override := range overrides {
		if override.ID == id {
			return override, nil
		}
	}
	return store.RateOverride{}, store.ErrNotFound
}

func (s *Server) CreateRateOverride(ctx context.Context,
	request gen.CreateRateOverrideRequestObject) (gen.CreateRateOverrideResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.CreateRateOverride401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	if request.Body.PerSegmentMinor < 0 {
		return gen.CreateRateOverride422JSONResponse(errorBody(codeValidation,
			"A rate cannot be negative.")), nil
	}
	tenant, err := store.GetTenant(ctx, s.operatorPool(), request.Body.TenantId)
	if errors.Is(err, store.ErrNotFound) {
		return gen.CreateRateOverride422JSONResponse(errorBody(codeValidation,
			"No such tenant.")), nil
	}
	if err != nil {
		return nil, err
	}

	var category *string
	if request.Body.Category != nil {
		if parsed, parseErr := request.Body.Category.AsTemplateCategory(); parseErr == nil {
			value := string(parsed)
			category = &value
		}
	}
	// The override's currency follows the tenant's default rate corridor rather
	// than being chosen here: an override priced in a different currency from
	// the wallet it debits cannot be charged.
	created, err := store.CreateRateOverride(ctx, s.DB, store.RateOverride{
		TenantID: request.Body.TenantId, TenantName: tenant.Name,
		Country: string(request.Body.Country), Channel: string(request.Body.Channel),
		Category: category, PerSegmentMinor: int64(request.Body.PerSegmentMinor),
		Currency: "INR",
	})
	if err != nil {
		return nil, err
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, "rate.override_create",
		&request.Body.TenantId, tenant.Name,
		string(request.Body.Country)+" "+string(request.Body.Channel), ""); err != nil {
		return nil, err
	}
	created.TenantName = tenant.Name
	return gen.CreateRateOverride201JSONResponse(toTenantRateOverride(created)), nil
}

func (s *Server) EditRateOverride(ctx context.Context,
	request gen.EditRateOverrideRequestObject) (gen.EditRateOverrideResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.EditRateOverride401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	id, valid := parsePathID(request.Id)
	if !valid {
		return gen.EditRateOverride404JSONResponse(
			errorBody("not_found", "No such override.")), nil
	}
	if request.Body.PerSegmentMinor < 0 {
		return gen.EditRateOverride422JSONResponse(errorBody(codeValidation,
			"A rate cannot be negative.")), nil
	}
	if err := store.UpdateRateOverride(ctx, s.DB, id,
		int64(request.Body.PerSegmentMinor)); errors.Is(err, store.ErrNotFound) {
		return gen.EditRateOverride404JSONResponse(
			errorBody("not_found", "No such override.")), nil
	} else if err != nil {
		return nil, err
	}
	override, err := s.findOverride(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, "rate.override_update",
		&override.TenantID, override.TenantName,
		override.Country+" "+override.Channel, ""); err != nil {
		return nil, err
	}
	return gen.EditRateOverride200JSONResponse(toTenantRateOverride(override)), nil
}

func (s *Server) RemoveRateOverride(ctx context.Context,
	request gen.RemoveRateOverrideRequestObject) (gen.RemoveRateOverrideResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.RemoveRateOverride401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	id, valid := parsePathID(request.Id)
	if !valid {
		return gen.RemoveRateOverride404JSONResponse(
			errorBody("not_found", "No such override.")), nil
	}
	// Read before deleting: the audit entry needs the tenant and corridor, and
	// after the DELETE there is nothing left to name them with.
	override, err := s.findOverride(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.RemoveRateOverride404JSONResponse(
			errorBody("not_found", "No such override.")), nil
	}
	if err != nil {
		return nil, err
	}
	if err := store.DeleteRateOverride(ctx, s.DB, id); err != nil {
		return nil, err
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, "rate.override_delete",
		&override.TenantID, override.TenantName,
		override.Country+" "+override.Channel, ""); err != nil {
		return nil, err
	}
	return gen.RemoveRateOverride204Response{}, nil
}

func (s *Server) GetApprovalQueue(ctx context.Context, request gen.GetApprovalQueueRequestObject) (
	gen.GetApprovalQueueResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetApprovalQueue401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	// The status filter is applied in SQL rather than in the in-memory pass
	// below, because it decides WHICH rows to load: a queue defaulting to
	// pending must not read every approved sender ever just to discard them.
	var queueStatus *string
	if request.Params.Status != nil {
		value := string(*request.Params.Status)
		queueStatus = &value
	}
	senders, err := store.ListPendingSenders(ctx, s.operatorPool(), queueStatus)
	if err != nil {
		return nil, err
	}
	templates, err := store.ListPendingTemplates(ctx, s.operatorPool(), queueStatus)
	if err != nil {
		return nil, err
	}

	// Senders and templates share one queue because an operator works through
	// "what needs a decision", not "what kind of thing needs a decision".
	//
	// The filters are applied here rather than in SQL, which is the one place in
	// this file that happens. The queue is a merge of two different tables with
	// different shapes, so filtering in SQL would mean the same three conditions
	// written twice and kept in step by hand. This list is bounded by what staff
	// have not yet decided — tens of rows, not millions.
	// ponytail: in-memory filter over the merged queue; push into both queries
	// if the pending backlog ever grows past a few thousand.
	wantType, wantCountry, wantStatus := "", "", ""
	if request.Params.Type != nil {
		wantType = string(*request.Params.Type)
	}
	if request.Params.Country != nil {
		wantCountry = string(*request.Params.Country)
	}
	if request.Params.Status != nil {
		wantStatus = string(*request.Params.Status)
	}
	// Status is deliberately absent here — it was applied in SQL above. Checking
	// it twice would be harmless today and wrong the moment the two notions of
	// "matching status" drift apart.
	_ = wantStatus
	keep := func(itemType, country, status string) bool {
		return (wantType == "" || wantType == itemType) &&
			(wantCountry == "" || wantCountry == country)
	}

	items := make([]gen.ApprovalQueueItem, 0, len(senders)+len(templates))
	for _, sender := range senders {
		if !keep("sender", sender.Country, sender.Status) {
			continue
		}
		var item gen.ApprovalQueueItem
		if err := item.FromApprovalQueueSenderItem(gen.ApprovalQueueSenderItem{
			Id: sender.ID, ItemType: gen.ApprovalQueueSenderItemItemTypeSender,
			TenantId: sender.TenantID, TenantName: sender.TenantName,
			Header: sender.Header, Channel: gen.ChannelId(sender.Channel),
			Country: gen.CountryCode(sender.Country),
			Status:  gen.ApprovalStatus(sender.Status), CreatedAt: sender.CreatedAt,
			// Why it was refused, and the registry id it earned if approved.
			// The review dialog shows both; without them a rejected item gave
			// the operator no way to explain the decision to the customer.
			RejectionReason: sender.RejectionReason,
			RegistrationId:  sender.RegistrationID,
			// The channel-specific evidence the operator is being asked to
			// judge. Declared on the contract and never sent, so the dialog
			// asked "approve this?" while showing nothing to approve against.
			CallerIdNumber: sender.CallerIDNumber,
			EmailDomain:    sender.EmailDomain,
			FromAddress:    sender.FromAddress,
			DisplayName:    sender.DisplayName,
			DnsRecords:     dnsRecordsResponse(sender.DNSRecords),
		}); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	for _, template := range templates {
		if !keep("template", template.Country, template.Status) {
			continue
		}
		entry := gen.ApprovalQueueTemplateItem{
			Id: template.ID, ItemType: gen.ApprovalQueueTemplateItemItemTypeTemplate,
			TenantId: template.TenantID, TenantName: template.TenantName,
			Name: template.Name, Channel: gen.ChannelId(template.Channel),
			Country: gen.CountryCode(template.Country), Body: template.Body,
			Status: gen.ApprovalStatus(template.Status), CreatedAt: template.CreatedAt,
			RejectionReason: template.RejectionReason,
		}
		var item gen.ApprovalQueueItem
		if err := item.FromApprovalQueueTemplateItem(entry); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return gen.GetApprovalQueue200JSONResponse{Items: items}, nil
}

func (s *Server) GetAbuseQueue(ctx context.Context, _ gen.GetAbuseQueueRequestObject) (
	gen.GetAbuseQueueResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetAbuseQueue401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	tenants, err := store.ListFlaggedTenants(ctx, s.operatorPool())
	if err != nil {
		return nil, err
	}
	items := make([]gen.TenantDetail, 0, len(tenants))
	for _, tenant := range tenants {
		items = append(items, toTenantDetail(tenant))
	}
	return gen.GetAbuseQueue200JSONResponse{Items: items}, nil
}

// decideSender is the shared body of approve and reject: they differ only in
// the status they write and whether a reason is required.
func (s *Server) decideSender(ctx context.Context, id string, status, reason string) (
	store.PendingSender, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return store.PendingSender{}, err
	}
	senderID, valid := parsePathID(id)
	if !valid {
		return store.PendingSender{}, store.ErrNotFound
	}
	// A sender may only be approved once the thing that proves it is really
	// theirs has actually happened. Both checks live here, in the one place
	// approve and reject share, so no future approval path can skip them.
	//
	// Approving an unverified email domain would let a tenant send as a domain
	// they do not control, which is the entire problem SPF, DKIM and DMARC
	// exist to prevent — and every message sent that way damages the sending
	// reputation of every other tenant on the same infrastructure. The
	// caller-ID check is the same argument for voice: an unverified number is
	// someone else's phone number.
	if status == "approved" {
		if err := s.senderReadyForApproval(ctx, senderID); err != nil {
			return store.PendingSender{}, err
		}
	}

	sender, err := store.DecideSender(ctx, s.operatorPool(), senderID, status, reason)
	if err != nil {
		return store.PendingSender{}, err
	}
	action := "sender.approve"
	if status == "rejected" {
		action = "sender.reject"
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, action,
		&sender.TenantID, sender.TenantName, sender.Header, reason); err != nil {
		return store.PendingSender{}, err
	}
	return sender, nil
}

func toGenSender(sender store.PendingSender) gen.SenderId {
	return gen.SenderId{
		Id: sender.ID, Header: sender.Header,
		Channel:   gen.ChannelId(sender.Channel),
		Country:   gen.CountryCode(sender.Country),
		Status:    gen.ApprovalStatus(sender.Status),
		CreatedAt: sender.CreatedAt,
	}
}

func (s *Server) ApproveSenderItem(ctx context.Context,
	request gen.ApproveSenderItemRequestObject) (gen.ApproveSenderItemResponseObject, error) {

	sender, err := s.decideSender(ctx, request.Id, "approved", "")
	if errors.Is(err, store.ErrNotFound) {
		return gen.ApproveSenderItem404JSONResponse(
			errorBody("not_found", "No such sender.")), nil
	}
	if errors.Is(err, errUnauthenticated) {
		return gen.ApproveSenderItem401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.ApproveSenderItem200JSONResponse(toGenSender(sender)), nil
}

func (s *Server) RejectSenderItem(ctx context.Context,
	request gen.RejectSenderItemRequestObject) (gen.RejectSenderItemResponseObject, error) {

	reason := ""
	if request.Body != nil {
		reason = request.Body.Reason
	}
	// A rejection with no reason is useless to the customer receiving it: they
	// cannot fix what they are not told about.
	if strings.TrimSpace(reason) == "" {
		return gen.RejectSenderItem422JSONResponse(errorBody(codeValidation,
			"A rejection reason is required.")), nil
	}
	sender, err := s.decideSender(ctx, request.Id, "rejected", reason)
	if errors.Is(err, store.ErrNotFound) {
		return gen.RejectSenderItem404JSONResponse(
			errorBody("not_found", "No such sender.")), nil
	}
	if errors.Is(err, errUnauthenticated) {
		return gen.RejectSenderItem401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.RejectSenderItem200JSONResponse(toGenSender(sender)), nil
}

func toGenTemplate(template store.PendingTemplate) gen.Template {
	out := gen.Template{
		Id: template.ID, Name: template.Name,
		Channel:   gen.ChannelId(template.Channel),
		Country:   gen.CountryCode(template.Country),
		Body:      template.Body,
		Status:    gen.ApprovalStatus(template.Status),
		CreatedAt: template.CreatedAt,
	}
	return out
}

func (s *Server) decideTemplate(ctx context.Context, id string, status, reason string) (
	store.PendingTemplate, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return store.PendingTemplate{}, err
	}
	templateID, valid := parsePathID(id)
	if !valid {
		return store.PendingTemplate{}, store.ErrNotFound
	}
	template, err := store.DecideTemplate(ctx, s.operatorPool(), templateID, status, reason)
	if err != nil {
		return store.PendingTemplate{}, err
	}
	action := "template.approve"
	if status == "rejected" {
		action = "template.reject"
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, action,
		&template.TenantID, template.TenantName, template.Name, reason); err != nil {
		return store.PendingTemplate{}, err
	}
	return template, nil
}

func (s *Server) ApproveTemplateItem(ctx context.Context,
	request gen.ApproveTemplateItemRequestObject) (gen.ApproveTemplateItemResponseObject, error) {

	template, err := s.decideTemplate(ctx, request.Id, "approved", "")
	if errors.Is(err, store.ErrNotFound) {
		return gen.ApproveTemplateItem404JSONResponse(
			errorBody("not_found", "No such template.")), nil
	}
	if errors.Is(err, errUnauthenticated) {
		return gen.ApproveTemplateItem401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.ApproveTemplateItem200JSONResponse(toGenTemplate(template)), nil
}

func (s *Server) RejectTemplateItem(ctx context.Context,
	request gen.RejectTemplateItemRequestObject) (gen.RejectTemplateItemResponseObject, error) {

	reason := ""
	if request.Body != nil {
		reason = request.Body.Reason
	}
	if strings.TrimSpace(reason) == "" {
		return gen.RejectTemplateItem422JSONResponse(errorBody(codeValidation,
			"A rejection reason is required.")), nil
	}
	template, err := s.decideTemplate(ctx, request.Id, "rejected", reason)
	if errors.Is(err, store.ErrNotFound) {
		return gen.RejectTemplateItem404JSONResponse(
			errorBody("not_found", "No such template.")), nil
	}
	if errors.Is(err, errUnauthenticated) {
		return gen.RejectTemplateItem401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.RejectTemplateItem200JSONResponse(toGenTemplate(template)), nil
}

func toGenTicket(ticket store.SupportTicket) gen.SupportTicket {
	return gen.SupportTicket{
		Id: ticket.ID, TenantId: ticket.TenantID, TenantName: ticket.TenantName,
		Subject: ticket.Subject, Category: gen.TicketCategory(ticket.Category),
		Status:    gen.TicketStatus(ticket.Status),
		CreatedAt: ticket.CreatedAt, UpdatedAt: ticket.UpdatedAt,
	}
}

func toGenTicketDetail(ticket store.SupportTicket,
	messages []store.SupportMessage) gen.SupportTicketDetail {

	out := gen.SupportTicketDetail{
		Id: ticket.ID, Subject: ticket.Subject,
		Category:  gen.TicketCategory(ticket.Category),
		Status:    gen.TicketStatus(ticket.Status),
		CreatedAt: ticket.CreatedAt,
		Messages:  make([]gen.SupportMessage, 0, len(messages)),
	}
	for _, message := range messages {
		out.Messages = append(out.Messages, gen.SupportMessage{
			Id: message.ID.String(), Author: gen.SupportMessageAuthor(message.Author),
			AuthorName: message.AuthorName, Body: message.Body,
			CreatedAt: message.CreatedAt,
		})
	}
	return out
}

func (s *Server) GetOperatorSupportTickets(ctx context.Context,
	request gen.GetOperatorSupportTicketsRequestObject) (
	gen.GetOperatorSupportTicketsResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetOperatorSupportTickets401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	tickets, err := store.ListAllSupportTickets(ctx, s.operatorPool(), request.Params.TenantId)
	if err != nil {
		return nil, err
	}
	out := make([]gen.SupportTicket, 0, len(tickets))
	for _, ticket := range tickets {
		// Status and category filter in memory: the operator queue is small
		// enough that a second index earns nothing, and keeping one query means
		// one place to change when pagination arrives.
		if request.Params.Status != nil && ticket.Status != string(*request.Params.Status) {
			continue
		}
		if request.Params.Category != nil &&
			ticket.Category != string(*request.Params.Category) {
			continue
		}
		out = append(out, toGenTicket(ticket))
	}
	return gen.GetOperatorSupportTickets200JSONResponse{Tickets: out}, nil
}

func (s *Server) GetOperatorSupportTicket(ctx context.Context,
	request gen.GetOperatorSupportTicketRequestObject) (
	gen.GetOperatorSupportTicketResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetOperatorSupportTicket401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	id, valid := parsePathID(request.Id)
	if !valid {
		return gen.GetOperatorSupportTicket404JSONResponse(
			errorBody("not_found", "No such ticket.")), nil
	}
	ticket, messages, err := store.GetSupportTicketAnyTenant(ctx, s.operatorPool(), id)
	if errors.Is(err, store.ErrNotFound) {
		return gen.GetOperatorSupportTicket404JSONResponse(
			errorBody("not_found", "No such ticket.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetOperatorSupportTicket200JSONResponse(
		toGenTicketDetail(ticket, messages)), nil
}

func (s *Server) AddOperatorSupportMessage(ctx context.Context,
	request gen.AddOperatorSupportMessageRequestObject) (
	gen.AddOperatorSupportMessageResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.AddOperatorSupportMessage401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	id, valid := parsePathID(request.Id)
	if !valid {
		return gen.AddOperatorSupportMessage404JSONResponse(
			errorBody("not_found", "No such ticket.")), nil
	}
	if request.Body == nil || strings.TrimSpace(request.Body.Body) == "" {
		return gen.AddOperatorSupportMessage422JSONResponse(errorBody(codeValidation,
			"A message body is required.")), nil
	}
	if err := store.AddOperatorTicketMessage(ctx, s.operatorPool(), id, operator.Name,
		request.Body.Body); errors.Is(err, store.ErrNotFound) {
		return gen.AddOperatorSupportMessage404JSONResponse(
			errorBody("not_found", "No such ticket.")), nil
	} else if err != nil {
		return nil, err
	}
	ticket, messages, err := store.GetSupportTicketAnyTenant(ctx, s.operatorPool(), id)
	if err != nil {
		return nil, err
	}
	return gen.AddOperatorSupportMessage200JSONResponse(
		toGenTicketDetail(ticket, messages)), nil
}

// ticketDecision is the shared body of resolve and reopen.
func (s *Server) ticketDecision(ctx context.Context, id string, status, action string) (
	store.SupportTicket, []store.SupportMessage, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return store.SupportTicket{}, nil, err
	}
	ticketID, valid := parsePathID(id)
	if !valid {
		return store.SupportTicket{}, nil, store.ErrNotFound
	}
	if err := store.SetTicketStatusAnyTenant(ctx, s.operatorPool(), ticketID, status); err != nil {
		return store.SupportTicket{}, nil, err
	}
	ticket, messages, err := store.GetSupportTicketAnyTenant(ctx, s.operatorPool(), ticketID)
	if err != nil {
		return store.SupportTicket{}, nil, err
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, action,
		&ticket.TenantID, ticket.TenantName, ticket.Subject, ""); err != nil {
		return store.SupportTicket{}, nil, err
	}
	return ticket, messages, nil
}

func (s *Server) ResolveSupportTicket(ctx context.Context,
	request gen.ResolveSupportTicketRequestObject) (
	gen.ResolveSupportTicketResponseObject, error) {

	ticket, messages, err := s.ticketDecision(ctx, request.Id, "resolved", "ticket.resolve")
	if errors.Is(err, store.ErrNotFound) {
		return gen.ResolveSupportTicket404JSONResponse(
			errorBody("not_found", "No such ticket.")), nil
	}
	if errors.Is(err, errUnauthenticated) {
		return gen.ResolveSupportTicket401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.ResolveSupportTicket200JSONResponse(
		toGenTicketDetail(ticket, messages)), nil
}

func (s *Server) ReopenSupportTicket(ctx context.Context,
	request gen.ReopenSupportTicketRequestObject) (
	gen.ReopenSupportTicketResponseObject, error) {

	ticket, messages, err := s.ticketDecision(ctx, request.Id, "open", "ticket.reopen")
	if errors.Is(err, store.ErrNotFound) {
		return gen.ReopenSupportTicket404JSONResponse(
			errorBody("not_found", "No such ticket.")), nil
	}
	if errors.Is(err, errUnauthenticated) {
		return gen.ReopenSupportTicket401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.ReopenSupportTicket200JSONResponse(
		toGenTicketDetail(ticket, messages)), nil
}

func (s *Server) GetOperatorUsage(ctx context.Context,
	request gen.GetOperatorUsageRequestObject) (gen.GetOperatorUsageResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetOperatorUsage401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	clickhouse, err := s.clickhouse(ctx)
	if err != nil {
		return nil, err
	}
	window := gen.AnalyticsRange("30d")
	if request.Params.Range != nil {
		window = gen.AnalyticsRange(*request.Params.Range)
	}
	usage, err := store.QueryPlatformUsage(ctx, clickhouse, rangeSince(string(window)))
	if err != nil {
		return nil, s.clickhouseFailed(err)
	}
	names, err := store.TenantNames(ctx, s.operatorPool())
	if err != nil {
		return nil, err
	}

	report := gen.OperatorUsageReport{
		Range: window, TotalMessages: int(usage.Total),
		ByDay:     make([]gen.OperatorUsageByDay, 0, len(usage.ByDay)),
		ByChannel: make([]gen.OperatorUsageByChannel, 0, len(usage.ByChannel)),
		ByCountry: make([]gen.OperatorUsageByCountry, 0, len(usage.ByCountry)),
		ByTenant:  make([]gen.OperatorUsageByTenant, 0, len(usage.ByTenant)),
	}
	for _, row := range usage.ByDay {
		day, parseErr := time.Parse("2006-01-02", row.Key)
		if parseErr != nil {
			continue
		}
		report.ByDay = append(report.ByDay, gen.OperatorUsageByDay{
			Date: openapi_types.Date{Time: day}, MessageCount: int(row.Count)})
	}
	for _, row := range usage.ByChannel {
		report.ByChannel = append(report.ByChannel, gen.OperatorUsageByChannel{
			Channel: gen.ChannelId(row.Key), MessageCount: int(row.Count)})
	}
	for _, row := range usage.ByCountry {
		report.ByCountry = append(report.ByCountry, gen.OperatorUsageByCountry{
			Country: gen.CountryCode(row.Key), MessageCount: int(row.Count)})
	}
	for _, row := range usage.ByTenant {
		tenantID, parseErr := uuid.Parse(row.Key)
		if parseErr != nil {
			continue
		}
		// A tenant deleted since the messages were sent still has traffic in the
		// warehouse. Naming it "(deleted tenant)" is honest; dropping the row
		// would make the per-tenant breakdown disagree with the total.
		name := names[row.Key]
		if name == "" {
			name = "(deleted tenant)"
		}
		report.ByTenant = append(report.ByTenant, gen.OperatorUsageByTenant{
			TenantId: tenantID, TenantName: name, MessageCount: int(row.Count)})
	}
	return gen.GetOperatorUsage200JSONResponse(report), nil
}

// marginRows turns revenue rows into margin rows using route cost as the cost
// basis. Cost is per SEGMENT, so it multiplies by segments rather than message
// count — a two-segment message costs twice as much to carry.
func marginRows(revenue []store.PlatformRevenueRow, costFor func(key string) int64,
	label func(key string) string) []gen.OperatorMarginRow {

	out := make([]gen.OperatorMarginRow, 0, len(revenue))
	for _, row := range revenue {
		cost := costFor(row.Key) * int64(row.Segments)
		margin := row.Revenue - cost
		percentage := float32(0)
		if row.Revenue > 0 {
			percentage = float32(margin) / float32(row.Revenue)
		}
		out = append(out, gen.OperatorMarginRow{
			Key: row.Key, Label: label(row.Key),
			RevenueMinor: int(row.Revenue), CostMinor: int(cost),
			MarginMinor: int(margin), MarginPct: percentage,
		})
	}
	return out
}

func (s *Server) GetOperatorMargin(ctx context.Context,
	request gen.GetOperatorMarginRequestObject) (gen.GetOperatorMarginResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetOperatorMargin401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	clickhouse, err := s.clickhouse(ctx)
	if err != nil {
		return nil, err
	}
	window := gen.AnalyticsRange("30d")
	if request.Params.Range != nil {
		window = gen.AnalyticsRange(*request.Params.Range)
	}
	since := rangeSince(string(window))

	// The whole routing table: this response is a rate card, not a filtered view.
	routes, err := store.ListRoutes(ctx, s.DB, nil, nil)
	if err != nil {
		return nil, err
	}
	names, err := store.TenantNames(ctx, s.operatorPool())
	if err != nil {
		return nil, err
	}

	// Cheapest enabled route per channel and per country: the best case cost,
	// which is the honest floor to measure margin against.
	channelCost := map[string]int64{}
	countryCost := map[string]int64{}
	for _, route := range routes {
		if route.Status != "enabled" {
			continue
		}
		if existing, ok := channelCost[route.Channel]; !ok || route.CostPerSegmentMinor < existing {
			channelCost[route.Channel] = route.CostPerSegmentMinor
		}
		if existing, ok := countryCost[route.Country]; !ok || route.CostPerSegmentMinor < existing {
			countryCost[route.Country] = route.CostPerSegmentMinor
		}
	}
	// A blended figure for per-tenant rows, which span corridors.
	var blended int64
	for _, cost := range channelCost {
		if blended == 0 || cost < blended {
			blended = cost
		}
	}

	byChannel, err := store.QueryPlatformRevenue(ctx, clickhouse, since, "channel")
	if err != nil {
		return nil, s.clickhouseFailed(err)
	}
	byCountry, err := store.QueryPlatformRevenue(ctx, clickhouse, since, "country")
	if err != nil {
		return nil, s.clickhouseFailed(err)
	}
	byTenant, err := store.QueryPlatformRevenue(ctx, clickhouse, since, "tenant_id")
	if err != nil {
		return nil, s.clickhouseFailed(err)
	}

	// Grouped by currency, never summed across it: adding rupees to dollars
	// would produce a margin figure that means nothing.
	totals := map[string]struct{ revenue, cost int64 }{}
	for _, row := range byChannel {
		entry := totals[row.Currency]
		entry.revenue += row.Revenue
		entry.cost += channelCost[row.Key] * int64(row.Segments)
		totals[row.Currency] = entry
	}

	report := gen.OperatorMarginReport{Range: window, Groups: []gen.OperatorMarginGroup{}}
	for currency, total := range totals {
		percentage := float32(0)
		if total.revenue > 0 {
			percentage = float32(total.revenue-total.cost) / float32(total.revenue)
		}
		report.Groups = append(report.Groups, gen.OperatorMarginGroup{
			Currency:     gen.CurrencyCode(currency),
			RevenueMinor: int(total.revenue), CostMinor: int(total.cost),
			MarginMinor: int(total.revenue - total.cost), MarginPct: percentage,
			ByChannel: marginRows(byChannel,
				func(key string) int64 { return channelCost[key] },
				func(key string) string { return key }),
			ByCountry: marginRows(byCountry,
				func(key string) int64 { return countryCost[key] },
				func(key string) string { return key }),
			ByTenant: marginRows(byTenant,
				func(string) int64 { return blended },
				func(key string) string {
					if name := names[key]; name != "" {
						return name
					}
					return "(deleted tenant)"
				}),
		})
	}
	return gen.GetOperatorMargin200JSONResponse(report), nil
}

func (s *Server) GetUserActivity(ctx context.Context,
	request gen.GetUserActivityRequestObject) (gen.GetUserActivityResponseObject, error) {

	if _, err := s.requireOperator(ctx); err != nil {
		return gen.GetUserActivity401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}

	// Every filter the contract declares is applied. All three used to be
	// ignored, so the console's tenant dropdown, event-type dropdown and range
	// chips each rendered, accepted a choice, and changed nothing — the list
	// looked filtered because the controls looked engaged.
	filter := store.UserActivityFilter{
		TenantID: request.Params.TenantId,
		Since:    rangeStart(request.Params.Range),
	}
	if request.Params.EventType != nil {
		eventType := string(*request.Params.EventType)
		filter.EventType = &eventType
	}
	if request.Params.Limit != nil {
		filter.Limit = *request.Params.Limit
	}

	activity, err := store.ListUserActivity(ctx, s.operatorPool(), filter)
	if err != nil {
		return nil, err
	}
	entries := make([]gen.UserActivityEntry, 0, len(activity))
	for _, event := range activity {
		entries = append(entries, gen.UserActivityEntry{
			Id: event.ID.String(), EventType: gen.UserActivityEventType(event.EventType),
			OccurredAt: event.OccurredAt,
			TenantId:   event.TenantID, TenantName: event.TenantName,
			UserName: event.UserName, UserEmail: openapi_types.Email(event.UserEmail),
			Detail: event.Detail,
		})
	}
	return gen.GetUserActivity200JSONResponse{Entries: entries}, nil
}

// rangeStart turns the contract's analytics range into the timestamp to look
// back to. A missing or unrecognised range means no lower bound rather than an
// error: this parameter narrows a list, and the widest possible answer is the
// safe reading of "not specified".
func rangeStart(analyticsRange *gen.AnalyticsRange) time.Time {
	if analyticsRange == nil {
		return time.Time{}
	}
	var days int
	switch *analyticsRange {
	case gen.AnalyticsRangeN7d:
		days = 7
	case gen.AnalyticsRangeN30d:
		days = 30
	case gen.AnalyticsRangeN90d:
		days = 90
	default:
		return time.Time{}
	}
	return time.Now().AddDate(0, 0, -days)
}

// senderReadyForApproval refuses approval when a channel's proof-of-ownership
// step has not completed. Returns nil for channels that have no such step.
func (s *Server) senderReadyForApproval(ctx context.Context, senderID uuid.UUID) error {
	var channel string
	var voiceVerified bool
	if err := s.operatorPool().QueryRow(ctx,
		`SELECT channel, voice_verified FROM sender_ids WHERE id = $1`,
		senderID).Scan(&channel, &voiceVerified); err != nil {
		return err
	}

	switch channel {
	case "EMAIL":
		var total, verified int
		if err := s.operatorPool().QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE status = 'verified')
			FROM sender_dns_records WHERE sender_id = $1`, senderID,
		).Scan(&total, &verified); err != nil {
			return err
		}
		// No records at all is also unverified. A sender with nothing to
		// publish has proved nothing, and treating an empty set as "all
		// verified" is the classic vacuous-truth bug in a permission check.
		if total == 0 || verified < total {
			return errDependencyUnmet(fmt.Sprintf(
				"All 3 DNS records must be verified before approval — %d of %d are.",
				verified, total))
		}
	case "VOICE":
		if !voiceVerified {
			return errDependencyUnmet(
				"The caller-ID number must be verified before this sender can be approved.")
		}
	}
	return nil
}

// recordActivity appends one user-activity event for the caller.
//
// It never fails the request. A login refused because its audit row could not
// be written would turn a bookkeeping problem into an outage, and the thing
// being recorded has already happened — dropping the record is bad, undoing the
// action is worse. The failure is logged at warn so it is visible without
// paging anyone.
func (s *Server) recordActivity(ctx context.Context, identity store.Identity,
	eventType, detail string) {

	if err := store.RecordUserActivity(ctx, s.DB, identity,
		identity.UserID, identity.Name, identity.Email, eventType, detail); err != nil {
		s.Logger.Warn("user activity not recorded",
			"event", eventType, "tenant", identity.TenantID, "error", err)
	}
}

// dnsRecordsResponse maps stored DNS rows onto the contract shape, returning
// nil for a sender that has none — the field is optional and an empty array
// would tell the dialog "checked, found nothing" rather than "not applicable".
func dnsRecordsResponse(records []store.SenderDNSRecord) *[]gen.EmailDnsRecord {
	if len(records) == 0 {
		return nil
	}
	out := make([]gen.EmailDnsRecord, 0, len(records))
	for _, record := range records {
		out = append(out, gen.EmailDnsRecord{
			Type:   gen.EmailDnsRecordType(record.Type),
			Host:   record.Host,
			Value:  record.Value,
			Status: gen.EmailDnsRecordStatus(record.Status),
		})
	}
	return &out
}

// auditDetail turns an action code into the sentence the audit log shows.
//
// The detail column was written empty, so every row read as a bare code —
// "tenant.suspend" against a tenant name — and the person scanning the log
// during an incident had to translate each one. The code stays the machine
// key and remains what the filter matches on; this is the human half.
func auditDetail(action, tenantName string) string {
	verbs := map[string]string{
		"tenant.suspend":      "Suspended",
		"tenant.reinstate":    "Reinstated",
		"tenant.throttle":     "Throttled",
		"tenant.flag_abuse":   "Flagged for abuse review",
		"tenant.dismiss_flag": "Dismissed the abuse flag for",
	}
	verb, known := verbs[action]
	if !known {
		return ""
	}
	return verb + " " + tenantName
}
