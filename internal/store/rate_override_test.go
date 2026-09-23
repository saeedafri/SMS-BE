package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/store"
)

// An override is a price somebody agreed to, and it applies only to what it was
// agreed for. Returning a WhatsApp UTILITY rate for WhatsApp MARKETING bills the
// customer at a number nobody signed off — and the sparser the override table,
// the likelier it was, since a tenant normally has one or two rows.
func TestARateOverrideAppliesOnlyToWhatItWasAgreedFor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	negotiated, control := uuid.New(), uuid.New()
	admin := seedTenants(t, ctx, negotiated, control)
	pool := appPool(t, ctx)

	const (
		utilityOnly  = 4001
		channelWide  = 4002
		utilityAgain = 4003
	)
	override := func(tenant uuid.UUID, category any, minor int64) {
		t.Helper()
		if _, err := admin.Exec(ctx, `
			INSERT INTO rate_overrides (tenant_id, country, channel, category,
			                            per_segment_minor, currency)
			VALUES ($1, 'IN', 'WHATSAPP', $2, $3, 'INR')`,
			tenant, category, minor); err != nil {
			t.Fatalf("seed override: %v", err)
		}
	}
	rateFor := func(tenant uuid.UUID, category string) int64 {
		t.Helper()
		rate, err := store.FindPricingRate(ctx, pool, tenant, "IN", "WHATSAPP", category)
		if err != nil {
			t.Fatalf("find rate: %v", err)
		}
		return rate.PerSegmentMinor
	}

	// What this corridor costs a tenant that negotiated nothing. Read rather
	// than hardcoded: the assertions below are about the override being absent,
	// not about what the published card happens to say today.
	defaultMarketing := rateFor(control, "MARKETING")
	defaultAnyCategory := rateFor(control, "")

	override(negotiated, "UTILITY", utilityOnly)

	if got := rateFor(negotiated, "MARKETING"); got != defaultMarketing {
		t.Errorf("MARKETING with only a UTILITY override = %d, want the default %d — "+
			"an override is never a fallback for a sibling category", got, defaultMarketing)
	}
	if got := rateFor(negotiated, "UTILITY"); got != utilityOnly {
		t.Errorf("UTILITY = %d, want its own override %d", got, utilityOnly)
	}
	// Not enumerated in the decision, and it follows from it: an ask with no
	// category is not an ask for UTILITY, so the agreement does not cover it.
	if got := rateFor(negotiated, ""); got != defaultAnyCategory {
		t.Errorf("a category-less ask = %d, want the default %d — the override was "+
			"agreed for one category, not for the channel", got, defaultAnyCategory)
	}

	// Set without a category, which is what "every category on this channel"
	// means.
	channelLevel := uuid.New()
	seedTenants(t, ctx, channelLevel)
	override(channelLevel, nil, channelWide)
	for _, category := range []string{"MARKETING", "UTILITY", "AUTHENTICATION", ""} {
		if got := rateFor(channelLevel, category); got != channelWide {
			t.Errorf("category %q under a channel-level override = %d, want %d",
				category, got, channelWide)
		}
	}

	// Both: the exact category beats the channel-level row.
	override(channelLevel, "UTILITY", utilityAgain)
	if got := rateFor(channelLevel, "UTILITY"); got != utilityAgain {
		t.Errorf("UTILITY with both overrides = %d, want the exact one %d", got, utilityAgain)
	}
	if got := rateFor(channelLevel, "MARKETING"); got != channelWide {
		t.Errorf("MARKETING with both overrides = %d, want the channel-level one %d",
			got, channelWide)
	}
}
