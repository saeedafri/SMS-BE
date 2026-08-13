package api_test

import (
	"net/http"
	"strings"
	"testing"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

func TestListPricingReturnsTheRateCard(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodGet, "/v1/pricing", acct.Token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}
	var rates []gen.PricingRate
	res.decode(t, &rates)
	if len(rates) == 0 {
		t.Fatal("the rate card is empty; a fresh install cannot estimate a cost")
	}
	for _, rate := range rates {
		if rate.PerSegmentMinor < 0 {
			t.Errorf("negative rate: %+v", rate)
		}
		if rate.Currency == "" || rate.Country == "" || rate.Channel == "" {
			t.Errorf("incomplete rate: %+v", rate)
		}
	}
}

func TestEstimateMultipliesRecipientsBySegmentsByRate(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	// India SMS is seeded at 12 paise per segment.
	res := h.do(http.MethodPost, "/v1/billing/estimate", acct.Token, map[string]any{
		"country": "IN", "channel": "SMS", "recipientCount": 1000,
		"primaryBody": "Your order has shipped.",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}
	var estimate gen.CampaignEstimate
	res.decode(t, &estimate)

	if estimate.SegmentsPerMessage != 1 {
		t.Fatalf("segmentsPerMessage = %d, want 1", estimate.SegmentsPerMessage)
	}
	if estimate.CostMinorMin != 12_000 || estimate.CostMinorMax != 12_000 {
		t.Fatalf("cost = %d..%d, want 12000..12000 (1000 × 1 × 12)",
			estimate.CostMinorMin, estimate.CostMinorMax)
	}
	if estimate.Currency != gen.CurrencyCode("INR") {
		t.Errorf("currency = %q, want INR", estimate.Currency)
	}
	// With no fallback, the range collapses to a single number.
	if estimate.FallbackEligible != 0 {
		t.Errorf("fallbackEligible = %d with no fallback, want 0", estimate.FallbackEligible)
	}
}

// A longer body costs proportionally more, and the estimate must use the same
// segment arithmetic the eventual charge will — an estimate that disagrees
// with the invoice is exactly the opaque billing this product exists to fix.
func TestEstimateScalesWithSegments(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	body := strings.Repeat("a", 161) // tips into two segments
	res := h.do(http.MethodPost, "/v1/billing/estimate", acct.Token, map[string]any{
		"country": "IN", "channel": "SMS", "recipientCount": 100, "primaryBody": body,
	})
	var estimate gen.CampaignEstimate
	res.decode(t, &estimate)

	if estimate.SegmentsPerMessage != 2 {
		t.Fatalf("segmentsPerMessage = %d, want 2", estimate.SegmentsPerMessage)
	}
	if estimate.CostMinorMin != 2_400 {
		t.Fatalf("cost = %d, want 2400 (100 × 2 × 12)", estimate.CostMinorMin)
	}
}

// One non-GSM-7 character re-encodes the message to UCS-2 and more than
// doubles the bill. The estimate has to show that, or the user is surprised by
// the invoice.
func TestEstimateReflectsUCS2ReEncoding(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	plain := h.do(http.MethodPost, "/v1/billing/estimate", acct.Token, map[string]any{
		"country": "IN", "channel": "SMS", "recipientCount": 1,
		"primaryBody": strings.Repeat("a", 160),
	})
	var plainEstimate gen.CampaignEstimate
	plain.decode(t, &plainEstimate)

	unicode := h.do(http.MethodPost, "/v1/billing/estimate", acct.Token, map[string]any{
		"country": "IN", "channel": "SMS", "recipientCount": 1,
		"primaryBody": strings.Repeat("a", 159) + "’",
	})
	var unicodeEstimate gen.CampaignEstimate
	unicode.decode(t, &unicodeEstimate)

	if plainEstimate.SegmentsPerMessage != 1 {
		t.Fatalf("plain 160 chars = %d segments, want 1", plainEstimate.SegmentsPerMessage)
	}
	if unicodeEstimate.SegmentsPerMessage != 3 {
		t.Fatalf("160 chars with a smart quote = %d segments, want 3",
			unicodeEstimate.SegmentsPerMessage)
	}
	if unicodeEstimate.CostMinorMin <= plainEstimate.CostMinorMin {
		t.Fatal("the UCS-2 estimate is not more expensive than the GSM-7 one")
	}
}

// With an RCS→SMS fallback the contract models a range, because which
// recipients get which channel depends on per-handset capability.
func TestEstimateReturnsARangeWhenAFallbackIsPossible(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPost, "/v1/billing/estimate", acct.Token, map[string]any{
		"country": "IN", "channel": "RCS", "recipientCount": 500,
		"primaryBody": "Rich message",
		"fallback":    map[string]any{"channel": "SMS", "body": "Plain message"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}
	var estimate gen.CampaignEstimate
	res.decode(t, &estimate)

	if estimate.FallbackEligible != 500 {
		t.Errorf("fallbackEligible = %d, want 500", estimate.FallbackEligible)
	}
	// RCS is 35 paise and SMS 12, so the range spans 6000..17500.
	if estimate.CostMinorMin != 6_000 || estimate.CostMinorMax != 17_500 {
		t.Fatalf("cost = %d..%d, want 6000..17500",
			estimate.CostMinorMin, estimate.CostMinorMax)
	}
	if estimate.CostMinorMin > estimate.CostMinorMax {
		t.Fatal("min exceeds max")
	}
}

func TestEstimateRejectsAnUnpricedRoute(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPost, "/v1/billing/estimate", acct.Token, map[string]any{
		"country": "GB", "channel": "WHATSAPP", "recipientCount": 10,
		"primaryBody": "Hello",
	})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", res.Code, res.Body)
	}
}

func TestEstimateHandlesZeroRecipients(t *testing.T) {
	h := newHarness(t)
	acct := h.newAccount("owner")

	res := h.do(http.MethodPost, "/v1/billing/estimate", acct.Token, map[string]any{
		"country": "IN", "channel": "SMS", "recipientCount": 0, "primaryBody": "Hi",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}
	var estimate gen.CampaignEstimate
	res.decode(t, &estimate)
	if estimate.CostMinorMin != 0 || estimate.CostMinorMax != 0 {
		t.Fatalf("cost = %d..%d for zero recipients, want 0..0",
			estimate.CostMinorMin, estimate.CostMinorMax)
	}
}
