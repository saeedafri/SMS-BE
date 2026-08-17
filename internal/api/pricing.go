package api

import (
	"context"
	"errors"

	"github.com/saeedafri/sms-be/internal/domain/billing"
	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func (s *Server) ListPricing(ctx context.Context, _ gen.ListPricingRequestObject) (gen.ListPricingResponseObject, error) {
	if _, ok := identityFrom(ctx); !ok {
		return gen.ListPricing401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}
	rates, err := store.ListPricingRates(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	out := make([]gen.PricingRate, 0, len(rates))
	for _, rate := range rates {
		entry := gen.PricingRate{
			Country:         gen.CountryCode(rate.Country),
			Channel:         gen.ChannelId(rate.Channel),
			PerSegmentMinor: int(rate.PerSegmentMinor),
			Currency:        gen.CurrencyCode(rate.Currency),
		}
		if rate.Category != "" {
			var category gen.PricingRate_Category
			_ = category.FromTemplateCategory(gen.TemplateCategory(rate.Category))
			entry.Category = &category
		}
		out = append(out, entry)
	}
	return gen.ListPricing200JSONResponse(out), nil
}

// EstimateCost prices a send before it happens. The campaign builder shows this
// to the user, so it must use exactly the same segment arithmetic the eventual
// charge will — an estimate that disagrees with the invoice is the "opaque
// billing" complaint the product exists to fix.
func (s *Server) EstimateCost(ctx context.Context, request gen.EstimateCostRequestObject) (gen.EstimateCostResponseObject, error) {
	identity, ok := identityFrom(ctx)
	if !ok {
		return gen.EstimateCost401JSONResponse(
			errorBody(codeUnauthenticated, "Missing or invalid bearer token")), nil
	}

	recipients := request.Body.RecipientCount
	if recipients < 0 {
		return gen.EstimateCost422JSONResponse(
			errorBody(codeValidation, "Recipient count cannot be negative.")), nil
	}

	category := ""
	if request.Body.Category != nil {
		if decoded, err := request.Body.Category.AsTemplateCategory(); err == nil {
			category = string(decoded)
		}
	}

	rate, err := store.FindPricingRate(ctx, s.DB, identity.TenantID,
		string(request.Body.Country), string(request.Body.Channel), category)
	if errors.Is(err, store.ErrNotFound) {
		return gen.EstimateCost422JSONResponse(errorBody(codeValidation,
			"We do not have a rate for that country and channel yet.")), nil
	}
	if err != nil {
		return nil, err
	}

	segments := billing.SegmentCount(request.Body.PrimaryBody)
	primaryCost := int64(recipients) * int64(segments) * rate.PerSegmentMinor

	// With no fallback channel, min and max are the same number. The contract
	// models a range because RCS→SMS fallback means some recipients may be
	// billed at the fallback channel's rate instead.
	minCost, maxCost := primaryCost, primaryCost
	fallbackEligible := 0

	if request.Body.Fallback != nil {
		fallbackCategory := ""
		if request.Body.Fallback.Category != nil {
			if decoded, err := request.Body.Fallback.Category.AsTemplateCategory(); err == nil {
				fallbackCategory = string(decoded)
			}
		}
		fallbackRate, err := store.FindPricingRate(ctx, s.DB, identity.TenantID,
			string(request.Body.Country), string(request.Body.Fallback.Channel), fallbackCategory)
		if errors.Is(err, store.ErrNotFound) {
			return gen.EstimateCost422JSONResponse(errorBody(codeValidation,
				"We do not have a rate for the fallback channel yet.")), nil
		}
		if err != nil {
			return nil, err
		}

		fallbackSegments := billing.SegmentCount(request.Body.Fallback.Body)
		fallbackCost := int64(recipients) * int64(fallbackSegments) * fallbackRate.PerSegmentMinor
		fallbackEligible = recipients

		// The bounds are "everyone on the primary channel" versus "everyone on
		// the fallback", whichever way round is cheaper — the truth lands
		// between them and depends on per-handset capability.
		minCost, maxCost = min(primaryCost, fallbackCost), max(primaryCost, fallbackCost)
	}

	return gen.EstimateCost200JSONResponse(gen.CampaignEstimate{
		Recipients:         recipients,
		FallbackEligible:   fallbackEligible,
		SegmentsPerMessage: segments,
		CostMinorMin:       int(minCost),
		CostMinorMax:       int(maxCost),
		Currency:           gen.CurrencyCode(rate.Currency),
		// Suppression lists arrive in Stage 4; until then nothing is excluded,
		// and reporting zero is accurate rather than a placeholder.
		SuppressedExcluded: 0,
	}), nil
}
