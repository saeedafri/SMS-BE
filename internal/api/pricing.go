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

	// The same bounds the campaign wizard quotes, from the same function.
	// These two estimates used to compute the upper bound differently — this
	// one had no spread at all unless a fallback channel was set, while the
	// wizard added 1 for any body containing "{{" — so the identical template
	// priced through /billing/estimate and then through the wizard returned
	// different numbers, and the estimator was the optimistic one. A customer
	// saw the price go up for no reason they could see.
	minSegments, maxSegments := billing.SegmentBounds(request.Body.PrimaryBody)

	// A range has TWO independent causes and both have to be spanned: how long
	// a recipient's substituted body is, and which channel that recipient lands
	// on. Quoting a segment range beside a single exact price would have the
	// screen contradict itself.
	minCost := int64(recipients) * int64(minSegments) * rate.PerSegmentMinor
	maxCost := int64(recipients) * int64(maxSegments) * rate.PerSegmentMinor
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

		fallbackMin, fallbackMax := billing.SegmentBounds(request.Body.Fallback.Body)
		fallbackCostMin := int64(recipients) * int64(fallbackMin) * fallbackRate.PerSegmentMinor
		fallbackCostMax := int64(recipients) * int64(fallbackMax) * fallbackRate.PerSegmentMinor
		fallbackEligible = recipients

		// Both causes at once: the cheapest a recipient can be is the shorter
		// body on the cheaper channel, and the dearest is the longer body on
		// the dearer one. The truth lands between and depends on per-handset
		// capability.
		minCost, maxCost = min(minCost, fallbackCostMin), max(maxCost, fallbackCostMax)
		if minSegments > fallbackMin {
			minSegments = fallbackMin
		}
		if maxSegments < fallbackMax {
			maxSegments = fallbackMax
		}
	}

	return gen.EstimateCost200JSONResponse(gen.CampaignEstimate{
		Recipients:            recipients,
		FallbackEligible:      fallbackEligible,
		SegmentsPerMessageMin: minSegments,
		SegmentsPerMessageMax: maxSegments,
		CostMinorMin:          int(minCost),
		CostMinorMax:          int(maxCost),
		Currency:              gen.CurrencyCode(rate.Currency),
		// Suppression lists arrive in Stage 4; until then nothing is excluded,
		// and reporting zero is accurate rather than a placeholder.
		SuppressedExcluded: 0,
	}), nil
}
