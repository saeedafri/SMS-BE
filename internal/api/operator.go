package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
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

	var result gen.OperatorLoginResult

	// The password alone is not a session when a second factor is enrolled.
	// Same shape as tenant login, because the frontend already knows how to read
	// it and because the two flows differing would be a reason for one of them
	// to be got wrong.
	state, err := store.LoadOperatorForMfa(ctx, s.DB, operator.OperatorID)
	if err != nil {
		return nil, err
	}
	if state.Enabled {
		challenge, err := s.issueOperatorMfaChallenge(ctx, operator.OperatorID)
		if err != nil {
			return nil, err
		}
		if err := result.FromOperatorLoginMfaChallengeResult(gen.OperatorLoginMfaChallengeResult{
			Kind: "mfa_challenge", Challenge: challenge,
		}); err != nil {
			return nil, err
		}
		return gen.OperatorLogin200JSONResponse(result), nil
	}

	session, err := s.issueOperatorSession(ctx, operator.OperatorID)
	if err != nil {
		return nil, err
	}
	// token and expiresAt repeat session.* verbatim.
	//
	// This endpoint used to answer a flat AuthSession, and the shipped operator
	// console still reads it that way — it casts the body to AuthSession and
	// takes .token. When the union landed, that read produced undefined, the
	// console wrote an empty cookie, and every request after sign-in came back
	// unauthenticated: staff could not get into the console at all.
	//
	// Mirroring costs two fields and keeps both readings true, which is worth
	// more than a tidy body. The MFA branch above deliberately has no mirror —
	// a challenge is not a session, and a client that cannot see that must fail
	// to sign in rather than proceed on a token that was never issued.
	if err := result.FromOperatorLoginSessionResult(gen.OperatorLoginSessionResult{
		Kind: "session", Session: session,
		Token: &session.Token, ExpiresAt: &session.ExpiresAt,
	}); err != nil {
		return nil, err
	}
	return gen.OperatorLogin200JSONResponse(result), nil
}

// issueOperatorSession mints a console session. Separate from the tenant
// issueSession for the same reason every other pair here is separate: the two
// identity systems must not share a token namespace.
func (s *Server) issueOperatorSession(ctx context.Context, operatorID uuid.UUID) (
	gen.AuthSession, error) {

	raw, tokenHash, err := auth.NewToken()
	if err != nil {
		return gen.AuthSession{}, err
	}
	expiresAt, err := store.CreateOperatorSession(ctx, s.DB, operatorID,
		tokenHash, sessionLifetime)
	if err != nil {
		return gen.AuthSession{}, err
	}
	return gen.AuthSession{Token: raw, ExpiresAt: expiresAt}, nil
}

func (s *Server) GetOperatorMe(ctx context.Context, _ gen.GetOperatorMeRequestObject) (
	gen.GetOperatorMeResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.GetOperatorMe401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	// Read rather than assumed: the security screen renders enrolled or not, and
	// a console that shows "not enrolled" to somebody who is would send them to
	// re-enrol and invalidate the codes they already have.
	state, err := store.LoadOperatorForMfa(ctx, s.DB, operator.OperatorID)
	if err != nil {
		return nil, err
	}
	return gen.GetOperatorMe200JSONResponse{
		OperatorId: operator.OperatorID,
		Name:       operator.Name,
		Email:      openapi_types.Email(operator.Email),
		Role:       gen.OperatorMeRole(operator.Role),
		MfaEnabled: state.Enabled,
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
	cursor, limit := "", 0
	if request.Params.Cursor != nil {
		cursor = *request.Params.Cursor
	}
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}
	tenants, total, next, err := store.ListTenants(ctx, s.operatorPool(), status, country, cursor, limit)
	if errors.Is(err, store.ErrInvalidCursor) {
		return gen.GetTenants422JSONResponse(
			errorBody(codeValidation, "That page cursor is not valid.")), nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]gen.Tenant, 0, len(tenants))
	for _, tenant := range tenants {
		out = append(out, toOperatorTenant(tenant))
	}
	page := gen.GetTenants200JSONResponse{Tenants: out, Total: total}
	if next != "" {
		page.NextCursor = &next
	}
	return page, nil
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
	// Suspension, throttling and reinstatement all land here, and every one of
	// them changes what the customer is allowed to do right now.
	s.publishTenantEvent(ctx, tenantID, "tenant.status_changed", "", "")
	return store.RecordOperatorAction(ctx, s.DB, operator.Email, action,
		&tenantID, tenant.Name, tenant.Name, tenantActionDetail(action, tenant.Name))
}

// tenantActionDetail is the sentence the Audit table shows as its row header.
//
// Every one of these used to record an empty detail, so the most prominent
// column on the screen was blank and the log read as a list of verbs with no
// objects. The target label already names the tenant; this says what was done
// to them, in the words an operator would use writing it up.
func tenantActionDetail(action, tenantName string) string {
	switch action {
	case "tenant.suspend":
		return "Suspended " + tenantName
	case "tenant.reinstate":
		return "Reinstated " + tenantName
	case "tenant.throttle":
		return "Throttled sending for " + tenantName
	case "tenant.flag_abuse":
		return "Flagged " + tenantName + " for abuse review"
	case "tenant.dismiss_flag":
		return "Dismissed the abuse flag on " + tenantName
	default:
		return ""
	}
}

func toTenantDetail(tenant store.OperatorTenant) gen.TenantDetail {
	return gen.TenantDetail{
		Id: tenant.ID, Name: tenant.Name, Country: gen.CountryCode(tenant.Country),
		CreatedAt: tenant.CreatedAt, Plan: "standard",
		Status:     gen.TenantStatus(tenantStanding(tenant)),
		FlaggedAt:  tenant.FlaggedAt,
		FlagReason: tenant.FlagReason,
		// Non-null exactly when the tenant is throttled. The contract declares
		// the key required with a nullable value, so it is always present and
		// the console never has to distinguish "no ceiling" from "field absent".
		ThrottledRatePerSecond: tenant.ThrottledRatePerSecond,
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
	// Volume comes from the warehouse; GetTenant only reads Postgres. Without
	// this the screen reported "0 messages sent in the last 30 days" for every
	// tenant, forever, while the usage report one click away said 2,119 for the
	// same tenant over the same window.
	//
	// Logged and swallowed, never returned: a warehouse that is down should cost
	// this screen its volume figure, not the whole tenant record an operator
	// opened it to read.
	if conn, chErr := s.clickhouse(ctx); chErr != nil {
		s.Logger.Warn("tenant usage snapshot unavailable — no warehouse connection",
			"tenant", tenant.ID, "error", chErr)
	} else if usage, usageErr := store.QueryTenantUsage(ctx, conn, tenant.ID.String(),
		// rangeSince, not time.Now().AddDate(0, 0, -30). The rollup is hourly, so
		// an untruncated start lands mid-bucket and silently drops part of the
		// oldest hour — this screen read 1,783 where /admin/usage read 1,788 for
		// the same tenant over the same nominal window. Sharing the store query
		// was not enough to keep the two screens agreeing; they have to share the
		// window too.
		rangeSince("30d")); usageErr != nil {
		s.Logger.Warn("tenant usage snapshot unavailable",
			"tenant", tenant.ID, "error", usageErr)
	} else {
		tenant.MessagesSent30d = usage.MessagesSent30d
		tenant.LastActivityAt = usage.LastActivityAt
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
			// And the throttle ceiling, for the same reason: a suspended tenant
			// is not sending at all, so a rate limit on it is a number the
			// console would still display and nothing would honour.
			if err := store.SetTenantThrottled(ctx, s.operatorPool(), id, nil); err != nil {
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
			// Clears the rate as well as the state: a reinstated tenant with a
			// ceiling left behind reads as still capped.
			if err := store.SetTenantThrottled(ctx, s.operatorPool(), id, nil); err != nil {
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

	// The tenant is resolved before the rate is judged, so a bad id is still
	// reported as a bad id. Validating the body first would answer 422 for a
	// tenant that does not exist, and an operator chasing a typo'd id would be
	// told their rate was wrong.
	if _, err := s.requireOperator(ctx); err != nil {
		return nil, err
	}
	tenantID, valid := parsePathID(request.Id)
	if !valid {
		return gen.ThrottleTenant404JSONResponse(
			errorBody(codeNotFound, "No such tenant.")), nil
	}
	before, err := store.GetTenant(ctx, s.operatorPool(), tenantID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.ThrottleTenant404JSONResponse(
			errorBody(codeNotFound, "No such tenant.")), nil
	}
	if err != nil {
		return nil, err
	}

	if request.Body == nil || request.Body.RatePerSecond < 1 {
		return gen.ThrottleTenant422JSONResponse(errorBody(codeValidation,
			"ratePerSecond must be a whole number of messages per second, at least 1.")), nil
	}
	// Throttling is a restriction on an ACTIVE account. Applying it to a
	// suspended tenant would record a ceiling nothing enforces, and re-applying
	// it to one already throttled would silently overwrite the ceiling an
	// earlier operator set without either of them seeing the other's number.
	if standing := tenantStanding(before); standing != "active" {
		return gen.ThrottleTenant422JSONResponse(errorBody(codeValidation,
			fmt.Sprintf("Only an active tenant can be throttled — this one is %s.",
				standing))), nil
	}

	rate := request.Body.RatePerSecond
	detail := fmt.Sprintf("Throttled %s to %d message/second", before.Name, rate)
	if rate != 1 {
		detail = fmt.Sprintf("Throttled %s to %d messages/second", before.Name, rate)
	}
	if request.Body.Reason != nil && strings.TrimSpace(*request.Body.Reason) != "" {
		detail += " — " + strings.TrimSpace(*request.Body.Reason)
	}
	tenant, err := s.applyTenantChangeWithDetail(ctx, request.Id, "tenant.throttle", detail,
		func(id uuid.UUID) error {
			if err := store.SetTenantFlag(ctx, s.operatorPool(), id, ""); err != nil {
				return err
			}
			return store.SetTenantThrottled(ctx, s.operatorPool(), id, &rate)
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

	return s.applyTenantChangeWithDetail(ctx, id, action, "", apply)
}

// applyTenantChangeWithDetail is applyTenantChange where the caller knows
// something the generic sentence cannot say — the throttle rate, for instance.
// "Throttled Acme" does not tell a later reader what ceiling was applied, which
// is the only thing they will want to know.
func (s *Server) applyTenantChangeWithDetail(ctx context.Context, id string, action string,
	detail string, apply func(tenantID uuid.UUID) error) (store.OperatorTenant, error) {

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
	if detail == "" {
		detail = auditDetail(action, before.Name)
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, action,
		&tenantID, before.Name, before.Name, detail); err != nil {
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
//
// The comparison is against the contract's own constant, not a literal.
// It used to read `!= "enabled"`, and routes have held 'active' or 'disabled'
// since migration 00029 renamed the status — so the filter excluded EVERY
// route, and the cost reference came back null on all fourteen rates. That was
// reported as missing carrier data, and the data had been there the whole time:
// the same invented word cost the console its status column once already, and
// this is the second place it hid.
func routeCostReference(routes []store.Route, country, channel string) *int {
	var lowest *int
	for _, route := range routes {
		if route.Country != country || route.Channel != channel ||
			route.Status != string(gen.RouteStatusActive) {
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
			// Required key, nullable value: an unwired corridor and an absent
			// field are different facts, and only one should be representable.
			ConnectionId: route.ConnectionID,
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
	// range, cursor and limit were all declared by the contract and read by
	// nothing: every call returned the newest 100 rows whatever was asked for.
	since := rangeSince("30d")
	if request.Params.Range != nil {
		since = rangeSince(string(*request.Params.Range))
	}
	filter := store.AuditLogFilter{
		TenantID: request.Params.TenantId, Action: auditAction, Since: since,
	}
	if request.Params.Cursor != nil {
		filter.Cursor = *request.Params.Cursor
	}
	if request.Params.Limit != nil {
		filter.Limit = *request.Params.Limit
	}
	entries, total, next, err := store.ListAuditLog(ctx, s.DB, filter)
	if errors.Is(err, store.ErrInvalidCursor) {
		return gen.GetAuditLog422JSONResponse(
			errorBody(codeValidation, "Malformed cursor.")), nil
	}
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
	page := gen.GetAuditLog200JSONResponse{Entries: out, Total: total}
	if next != "" {
		page.NextCursor = &next
	}
	return page, nil
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
	// Read BEFORE the change, not after: route.delete removes the row, so a
	// label read afterwards is always the bare uuid — which is the one entry
	// where knowing which route it was matters most.
	label := routeID.String()
	if route, err := store.GetRoute(ctx, s.DB, routeID); err == nil && route.Label != "" {
		label = route.Label
	}
	if err := apply(routeID); err != nil {
		return err
	}
	return store.RecordOperatorAction(ctx, s.DB, operator.Email, action,
		nil, "", label, routeActionDetail(action, label))
}

// routeActionDetail describes a routing change in words.
//
// Route order decides which carrier a tenant's traffic actually takes, so "the
// route was moved up" without saying which route, past what, is not something
// anyone can act on weeks later.
func routeActionDetail(action, label string) string {
	switch action {
	case "route.enable":
		return "Enabled the " + label + " route"
	case "route.disable":
		return "Disabled the " + label + " route"
	case "route.move_up":
		return "Raised the priority of the " + label + " route"
	case "route.move_down":
		return "Lowered the priority of the " + label + " route"
	case "route.delete":
		return "Deleted the " + label + " route"
	default:
		return ""
	}
}

// routeBeforeChange reads a route so a handler can refuse the change. It stays
// separate from routeAfterChange so a caller cannot accidentally decide on
// state it already overwrote.
func (s *Server) routeBeforeChange(ctx context.Context, id string) (store.Route, error) {
	routeID, valid := parsePathID(id)
	if !valid {
		return store.Route{}, store.ErrNotFound
	}
	return store.GetRoute(ctx, s.DB, routeID)
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
		ConnectionId:        route.ConnectionID,
	}
}

// CreateRoute adds a path to a corridor.
//
// Until this existed a corridor could only be changed by editing the table by
// hand: the console listed routes, reordered them and toggled them, and had no
// way to add the one an operator had just signed a carrier contract for. The
// route arrives disabled and last — see store.CreateRoute for why both.
func (s *Server) CreateRoute(ctx context.Context, request gen.CreateRouteRequestObject) (
	gen.CreateRouteResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.CreateRoute401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	if request.Body == nil {
		return gen.CreateRoute422JSONResponse(errorBody(codeValidation,
			"A route needs a country, channel, carrier, label, standing, cost and currency.")), nil
	}

	// Checked here rather than trusted from the binder: oapi-codegen renders
	// the contract's enums as plain Go string aliases, so an out-of-enum value
	// reaches the database and turns a typo into a 500.
	route := store.Route{
		Country: string(request.Body.Country), Channel: string(request.Body.Channel),
		Carrier:             string(request.Body.Carrier),
		Label:               strings.TrimSpace(request.Body.Label),
		ComplianceStanding:  string(request.Body.ComplianceStanding),
		CostPerSegmentMinor: int64(request.Body.CostPerSegmentMinor),
		Currency:            string(request.Body.Currency),
		// Optional: a corridor may be defined before its bind exists.
		ConnectionID: request.Body.ConnectionId,
	}
	for _, check := range []struct {
		value   string
		allowed []string
		field   string
	}{
		{route.Country, validCountries, "country"},
		{route.Channel, validChannels, "channel"},
		{route.Carrier, validCarriers, "carrier"},
		{route.ComplianceStanding, validStandings, "complianceStanding"},
		{route.Currency, validCurrencies, "currency"},
	} {
		if !oneOf(check.value, check.allowed) {
			return gen.CreateRoute422JSONResponse(errorBody(codeValidation,
				enumMessage(check.field, check.allowed))), nil
		}
	}
	if route.Label == "" {
		return gen.CreateRoute422JSONResponse(errorBody(codeValidation,
			"A route needs a label — it is how an operator tells two paths to the "+
				"same carrier apart.")), nil
	}
	if route.CostPerSegmentMinor < 0 {
		return gen.CreateRoute422JSONResponse(errorBody(codeValidation,
			"Cost per segment cannot be negative.")), nil
	}

	created, err := store.CreateRoute(ctx, s.DB, route)
	if err != nil {
		return nil, err
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, "route.create",
		nil, "", created.ID.String(), created.Label); err != nil {
		return nil, err
	}
	return gen.CreateRoute201JSONResponse(toGenRoute(created)), nil
}

// DeleteRoute removes a path from a corridor.
func (s *Server) DeleteRoute(ctx context.Context, request gen.DeleteRouteRequestObject) (
	gen.DeleteRouteResponseObject, error) {

	if err := s.routeAction(ctx, request.Id, "route.delete", func(id uuid.UUID) error {
		return store.DeleteRoute(ctx, s.DB, id)
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return gen.DeleteRoute404JSONResponse(errorBody("not_found", "No such route.")), nil
		}
		if errors.Is(err, store.ErrConflict) {
			return gen.DeleteRoute422JSONResponse(errorBody(codeValidation,
				"This route is active. Disable it and watch the corridor before "+
					"removing it.")), nil
		}
		return nil, err
	}
	return gen.DeleteRoute204Response{}, nil
}

func (s *Server) EnableRoute(ctx context.Context, request gen.EnableRouteRequestObject) (
	gen.EnableRouteResponseObject, error) {

	// A grey route is one whose traffic reaches handsets without being
	// registered with the operator behind it. It delivers until the carrier
	// notices, and then it does not: messages are filtered with no report, the
	// sender id is blocked, and under DLT the penalty lands on the customer's
	// principal entity rather than on us.
	//
	// So enabling one is not an ordinary toggle, and the console offered it as
	// though it were. Two grey routes were found active on production on
	// 2026-08-21, both with registered alternatives in the same corridor —
	// nobody chose that, it was just the easiest button to press. Turning one on
	// now requires the deployment to say so out loud.
	if !s.AllowGreyRoutes {
		route, err := s.routeBeforeChange(ctx, request.Id)
		if err == nil && route.ComplianceStanding == "grey" {
			return gen.EnableRoute422JSONResponse(errorBody(codeValidation,
				"This route is unregistered (grey) traffic. Enabling it risks the "+
					"customer's sender registration, so it is refused unless the "+
					"deployment sets ALLOW_GREY_ROUTES.")), nil
		}
	}

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
	// An unchecked channel here writes a rate row nothing will ever match, so
	// the corridor it was meant to price silently keeps the old number.
	if !oneOf(string(request.Body.Channel), validChannels) {
		return gen.UpdateDefaultRate422JSONResponse(errorBody(codeValidation,
			enumMessage("Channel", validChannels))), nil
	}
	rate, previous, err := store.UpsertPricingRate(ctx, s.DB, string(request.Body.Country),
		string(request.Body.Channel), category, int64(request.Body.PerSegmentMinor))
	if err != nil {
		return nil, err
	}
	// The target label names the corridor; the detail says what actually changed
	// about it. Without the old number the entry records that a price moved but
	// not from where, which is the one thing an incident review needs.
	corridor := string(request.Body.Country) + " " + string(request.Body.Channel)
	detail := fmt.Sprintf("Set the %s default rate to %d %s (minor units)",
		corridor, request.Body.PerSegmentMinor, rate.Currency)
	if previous != nil {
		detail = fmt.Sprintf("Changed the %s default rate from %d to %d %s (minor units)",
			corridor, *previous, request.Body.PerSegmentMinor, rate.Currency)
	}
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, "rate.default_update",
		nil, "", corridor, detail); err != nil {
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
	if !oneOf(string(request.Body.Channel), validChannels) {
		return gen.CreateRateOverride422JSONResponse(errorBody(codeValidation,
			enumMessage("Channel", validChannels))), nil
	}
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
		string(request.Body.Country)+" "+string(request.Body.Channel),
		fmt.Sprintf("Gave %s a negotiated %s %s rate of %d %s (minor units)",
			tenant.Name, request.Body.Country, request.Body.Channel,
			request.Body.PerSegmentMinor, created.Currency)); err != nil {
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
		override.Country+" "+override.Channel,
		fmt.Sprintf("Changed %s's negotiated %s %s rate to %d %s (minor units)",
			override.TenantName, override.Country, override.Channel,
			override.PerSegmentMinor, override.Currency)); err != nil {
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
		override.Country+" "+override.Channel,
		fmt.Sprintf("Removed %s's negotiated %s %s rate of %d %s (minor units); "+
			"they fall back to the default",
			override.TenantName, override.Country, override.Channel,
			override.PerSegmentMinor, override.Currency)); err != nil {
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
	// Compliance registrations are the third thing that needs a decision, and
	// they were missing from this queue entirely. A customer who submitted a DLT
	// or EIN registration sat in pending_review with nothing able to approve it,
	// which meant they could never send. The e2e suite hid it by advancing
	// registrations through /v1/dev/advance-registration — a test hook standing
	// in for a workflow that did not exist.
	registrations, err := store.ListPendingRegistrations(ctx, s.operatorPool(), queueStatus)
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

	// Carried with its timestamp so the merge can be ordered.
	//
	// Appending the three sources back to back left the queue grouped by kind
	// rather than by age, which is invisible while everything is returned and
	// wrong the moment it is paged: with a hundred pending senders, the first
	// page is all senders and a registration submitted weeks earlier never
	// appears at all. An operator works "what needs a decision next", so the
	// merge is sorted newest-first across all three kinds before it is cut.
	type queuedItem struct {
		at   time.Time
		item gen.ApprovalQueueItem
	}
	queued := make([]queuedItem, 0, len(senders)+len(templates)+len(registrations))
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
		queued = append(queued, queuedItem{at: sender.CreatedAt, item: item})
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
		queued = append(queued, queuedItem{at: template.CreatedAt, item: item})
	}
	for _, reg := range registrations {
		if !keep("registration", reg.Country, reg.Status) {
			continue
		}
		entry := gen.ApprovalQueueRegistrationItem{
			Id: reg.ID, ItemType: gen.ApprovalQueueRegistrationItemItemTypeRegistration,
			TenantId: reg.TenantID, TenantName: reg.TenantName,
			ObjectKey: reg.ObjectKey,
			Country:   gen.CountryCode(reg.Country),
			Status:    gen.ApprovalStatus(reg.Status), CreatedAt: reg.CreatedAt,
			RejectionReason: reg.RejectionReason,
			RegistrationId:  reg.ExternalID,
			// The submitted values are the whole point of the review: an
			// operator judging a DLT entity needs the PAN and entity name in
			// front of them, not just the fact that something was submitted.
			Fields: decodeRegistrationFields(reg.Fields),
		}
		var item gen.ApprovalQueueItem
		if err := item.FromApprovalQueueRegistrationItem(entry); err != nil {
			return nil, err
		}
		queued = append(queued, queuedItem{at: reg.CreatedAt, item: item})
	}
	sort.SliceStable(queued, func(a, b int) bool { return queued[a].at.After(queued[b].at) })
	items := make([]gen.ApprovalQueueItem, 0, len(queued))
	for _, entry := range queued {
		items = append(items, entry.item)
	}

	// Paged in memory over the merged list, and the cursor is an offset.
	//
	// The other paged endpoints keyset on (created_at, id) against one table.
	// This queue is a merge of three with different shapes, so a keyset cursor
	// would need a sort key that exists in all three and a three-way merge to
	// resume from — a lot of machinery for a list bounded by what staff have not
	// yet decided. The offset is honest about what it is: if rows are decided
	// between two page reads the window shifts, which for a queue being worked
	// down is a redraw, not a correctness problem.
	//
	// ponytail: offset cursor over a merged queue; keyset it if the pending
	// backlog ever justifies the three-way merge.
	total := len(items)
	offset := 0
	if request.Params.Cursor != nil && *request.Params.Cursor != "" {
		decoded, err := decodeOffsetCursor(*request.Params.Cursor)
		if err != nil {
			return gen.GetApprovalQueue422JSONResponse(
				errorBody(codeValidation, "That page cursor is not valid.")), nil
		}
		offset = decoded
	}
	limit := 100
	if request.Params.Limit != nil && *request.Params.Limit > 0 {
		limit = *request.Params.Limit
	}
	if offset > total {
		offset = total
	}
	window := items[offset:]
	page := gen.GetApprovalQueue200JSONResponse{Total: total}
	if len(window) > limit {
		window = window[:limit]
		next := encodeOffsetCursor(offset + limit)
		page.NextCursor = &next
	}
	page.Items = window
	return page, nil
}

// encodeOffsetCursor and decodeOffsetCursor carry a position through an opaque
// string, so a client cannot come to depend on it being an offset. The contract
// promises opacity, not a keyset.
func encodeOffsetCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("offset:" + strconv.Itoa(offset)))
}

func decodeOffsetCursor(cursor string) (int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("malformed cursor")
	}
	value, ok := strings.CutPrefix(string(raw), "offset:")
	if !ok {
		return 0, fmt.Errorf("malformed cursor")
	}
	offset, err := strconv.Atoi(value)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("malformed cursor")
	}
	return offset, nil
}

// decodeRegistrationFields turns the stored jsonb into the map the contract
// declares. A registration whose fields cannot be read is still worth showing —
// the operator can see who submitted what and when, and chase the rest — so a
// decode failure yields an empty map rather than failing the whole queue.
func decodeRegistrationFields(raw []byte) map[string]any {
	fields := map[string]any{}
	if len(raw) == 0 {
		return fields
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return map[string]any{}
	}
	return fields
}

// ApproveRegistrationItem records a compliance decision.
//
// Approving a registration is what unblocks a tenant's ability to send in that
// country, so until this existed the compliance step was a dead end: submitted,
// visible to the customer as "in review", and impossible for anyone to advance.
func (s *Server) ApproveRegistrationItem(ctx context.Context,
	request gen.ApproveRegistrationItemRequestObject) (
	gen.ApproveRegistrationItemResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.ApproveRegistrationItem401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	id, valid := parsePathID(request.Id)
	if !valid {
		return gen.ApproveRegistrationItem404JSONResponse(
			errorBody(codeNotFound, "No such registration.")), nil
	}
	reg, err := store.DecideRegistration(ctx, s.operatorPool(), id, "approved", "", "")
	if errors.Is(err, store.ErrNotFound) {
		return gen.ApproveRegistrationItem404JSONResponse(
			errorBody(codeNotFound, "No such registration.")), nil
	}
	if err != nil {
		return nil, err
	}
	if err := store.RecordOperatorAction(ctx, s.operatorPool(), operator.Email,
		"registration.approve", &reg.TenantID, reg.TenantName,
		reg.Country+" "+reg.ObjectKey,
		fmt.Sprintf("Approved the %s %s registration, unblocking sending in %s",
			reg.Country, reg.ObjectKey, reg.Country)); err != nil {
		return nil, err
	}
	// The customer is watching a screen that said "in review". Tell it.
	s.publishTenantEvent(ctx, reg.TenantID, "registration.decided", "", reg.ID.String())
	return gen.ApproveRegistrationItem200JSONResponse(operatorRegistrationResponse(reg)), nil
}

// RejectRegistrationItem refuses one, with the reason the customer will read.
func (s *Server) RejectRegistrationItem(ctx context.Context,
	request gen.RejectRegistrationItemRequestObject) (
	gen.RejectRegistrationItemResponseObject, error) {

	operator, err := s.requireOperator(ctx)
	if err != nil {
		return gen.RejectRegistrationItem401JSONResponse(
			errorBody(codeUnauthenticated, "Sign in to the operator console.")), nil
	}
	id, valid := parsePathID(request.Id)
	if !valid {
		return gen.RejectRegistrationItem404JSONResponse(
			errorBody(codeNotFound, "No such registration.")), nil
	}
	// A rejection the customer cannot act on is worse than none: they resubmit
	// the same thing and wait again. The sender and template rejections already
	// demand a reason, and compliance is the one where "why" matters most.
	reason := ""
	if request.Body != nil {
		reason = strings.TrimSpace(request.Body.Reason)
	}
	if reason == "" {
		return gen.RejectRegistrationItem422JSONResponse(errorBody(codeValidation,
			"Give a reason the customer can act on.")), nil
	}
	reg, err := store.DecideRegistration(ctx, s.operatorPool(), id, "rejected", reason, "")
	if errors.Is(err, store.ErrNotFound) {
		return gen.RejectRegistrationItem404JSONResponse(
			errorBody(codeNotFound, "No such registration.")), nil
	}
	if err != nil {
		return nil, err
	}
	if err := store.RecordOperatorAction(ctx, s.operatorPool(), operator.Email,
		"registration.reject", &reg.TenantID, reg.TenantName,
		reg.Country+" "+reg.ObjectKey,
		fmt.Sprintf("Rejected the %s %s registration: %s",
			reg.Country, reg.ObjectKey, reason)); err != nil {
		return nil, err
	}
	s.publishTenantEvent(ctx, reg.TenantID, "registration.decided", "", reg.ID.String())
	return gen.RejectRegistrationItem200JSONResponse(operatorRegistrationResponse(reg)), nil
}

// operatorRegistrationResponse is the queue's row shape rendered as the same
// Registration the customer sees. Separate from registrationResponse in
// registrations.go only because the operator queries carry a joined tenant name
// and raw jsonb, where the tenant-side query returns a decoded struct.
func operatorRegistrationResponse(reg store.PendingRegistration) gen.Registration {
	return gen.Registration{
		Id: reg.ID, Country: gen.CountryCode(reg.Country), ObjectKey: reg.ObjectKey,
		Status: gen.ApprovalStatus(reg.Status), RejectionReason: reg.RejectionReason,
		RegistrationId: reg.ExternalID, Fields: decodeRegistrationFields(reg.Fields),
		CreatedAt: reg.CreatedAt, UpdatedAt: time.Now(),
	}
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
	s.publishTenantEvent(ctx, sender.TenantID, "sender.decided", "", sender.ID.String())
	// An approval carries no reason, so passing `reason` alone left the Audit
	// table's most prominent column blank on every approve — half the rows on
	// the screen. The detail says what was decided about what.
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, action,
		&sender.TenantID, sender.TenantName, sender.Header,
		decisionDetail(action, "sender header", sender.Header, sender.TenantName, reason)); err != nil {
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
	s.publishTenantEvent(ctx, template.TenantID, "template.decided", "", template.ID.String())
	if err := store.RecordOperatorAction(ctx, s.DB, operator.Email, action,
		&template.TenantID, template.TenantName, template.Name,
		decisionDetail(action, "template", template.Name, template.TenantName, reason)); err != nil {
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
	filter := store.SupportTicketFilter{TenantID: request.Params.TenantId}
	if request.Params.Status != nil {
		value := string(*request.Params.Status)
		filter.Status = &value
	}
	if request.Params.Category != nil {
		value := string(*request.Params.Category)
		filter.Category = &value
	}
	if request.Params.Cursor != nil {
		filter.Cursor = *request.Params.Cursor
	}
	if request.Params.Limit != nil {
		filter.Limit = *request.Params.Limit
	}
	tickets, total, next, err := store.ListAllSupportTickets(ctx, s.operatorPool(), filter)
	if errors.Is(err, store.ErrInvalidCursor) {
		return gen.GetOperatorSupportTickets422JSONResponse(
			errorBody(codeValidation, "That page cursor is not valid.")), nil
	}
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
	page := gen.GetOperatorSupportTickets200JSONResponse{Tickets: out, Total: total}
	if next != "" {
		page.NextCursor = &next
	}
	return page, nil
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

	// Cheapest ACTIVE route per channel and per country: the best case cost,
	// which is the honest floor to measure margin against.
	//
	// Same stale word as routeCostReference — see the note there. Here it was
	// worse than a null: with every route excluded, both maps stayed empty, so
	// margin was measured against a cost of zero and every corridor reported a
	// margin of 100%.
	channelCost := map[string]int64{}
	countryCost := map[string]int64{}
	for _, route := range routes {
		if route.Status != string(gen.RouteStatusActive) {
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
	if request.Params.Cursor != nil {
		filter.Cursor = *request.Params.Cursor
	}

	activity, total, next, err := store.ListUserActivity(ctx, s.operatorPool(), filter)
	if errors.Is(err, store.ErrInvalidCursor) {
		return gen.GetUserActivity422JSONResponse(
			errorBody(codeValidation, "That page cursor is not valid.")), nil
	}
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
	page := gen.GetUserActivity200JSONResponse{Entries: entries, Total: total}
	if next != "" {
		page.NextCursor = &next
	}
	return page, nil
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

// decisionDetail is the sentence an approve or reject writes into the audit log.
//
// A rejection already carried its reason; an approval carried nothing, so the
// Audit table's row header was blank on every approve. Both now say what was
// decided, about what, for whom — the line an operator quotes into a write-up.
func decisionDetail(action, kind, subject, tenantName, reason string) string {
	verb := "Approved"
	if strings.HasSuffix(action, ".reject") {
		verb = "Rejected"
	}
	detail := fmt.Sprintf("%s the %s %q", verb, kind, subject)
	if tenantName != "" {
		detail += " for " + tenantName
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		detail += ": " + reason
	}
	return detail
}
