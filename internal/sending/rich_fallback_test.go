package sending_test

import (
	"context"
	"testing"

	"github.com/saeedafri/sms-be/internal/connector"
	"github.com/saeedafri/sms-be/internal/store"
)

// featureCarrier is an RCS gateway whose handsets each list their features.
type featureCarrier struct {
	recordingCarrier
	features map[string][]string
}

func (*featureCarrier) Vendor() string { return "airtel" }

func (c *featureCarrier) Capability(_ context.Context, _ string, msisdn string) (connector.RCSCapability, error) {
	return connector.RCSCapability{Msisdn: msisdn, Reachable: true, Features: c.features[msisdn]}, nil
}

func (c *featureCarrier) Reachable(_ context.Context, _ string, msisdns []string) ([]string, error) {
	return msisdns, nil
}

const carouselContent = `{"kind":"carousel","cardWidth":"MEDIUM","cards":[` +
	`{"mediaUrl":"https://cdn.example/a.jpg","title":"Hi {{first_name}}","description":"One"},` +
	`{"mediaUrl":"https://cdn.example/b.jpg","title":"Two","description":"Two"}]}`

// Batch 4 row R11: a handset that cannot render a carousel is carried by the
// SMS fallback, and the row says why.
func TestAHandsetThatCannotRenderACarouselGoesBySMS(t *testing.T) {
	gateway := &featureCarrier{features: map[string][]string{
		"+919820000211": {"RICHCARD_CAROUSEL"}, "+919820000212": {"RICHCARD_STANDALONE"}}}
	leg := newTwoLeg(t, gateway)
	leg.rcsCard = leg.f.seedRCSCard(leg.rcsSender, []string{"first_name"}, carouselContent)
	campaignID, sent, failed := leg.launch(t, map[string]contactSeed{
		"919820000211": {fields: map[string]string{"first_name": "A"}, consent: both},
		"919820000212": {fields: map[string]string{"first_name": "B"}, consent: both},
	})
	if sent != 2 || failed != 0 {
		t.Fatalf("sent=%d failed=%d, want both carried", sent, failed)
	}
	if len(gateway.submissions) != 1 || gateway.submissions[0].Msisdn != "+919820000211" {
		t.Errorf("RCS carried %d, want only the carousel-capable handset", len(gateway.submissions))
	}
	row := leg.rows(t, campaignID)["+919820000212"]
	if row.DeliveredChannel == nil || *row.DeliveredChannel != "SMS" ||
		row.FallbackReason != "rich_content_unsupported" || row.ReadAt != nil {
		t.Errorf("fallback row = %+v, want SMS, rich_content_unsupported, never read", row)
	}
}

// Batch 4 row R12: with no fallback the message fails rich_content_unsupported
// at no cost, and nothing reaches the carrier.
func TestWithNoFallbackAnUnrenderableCarouselFailsUncharged(t *testing.T) {
	gateway := &featureCarrier{features: map[string][]string{}}
	f := newFixture(t)
	f.service.Carriers = connector.Registry{Default: &recordingCarrier{},
		ByChannel: map[string]connector.Connector{"RCS": gateway}}
	sender := f.seedApprovedSender("RCSN"+t.Name()[:4], "RCS")
	template := f.seedRCSCard(sender, []string{"first_name"}, carouselContent)
	list := f.seedListWithConsent(map[string]contactSeed{
		"919820000221": {fields: map[string]string{"first_name": "A"},
			consent: map[string]string{"RCS": "opted_in"}}})
	campaignID := f.seedRCSCampaign(sender, template, list)
	campaign, err := store.GetCampaign(context.Background(), f.service.DB, f.identity, campaignID)
	if err != nil {
		t.Fatal(err)
	}
	if _, failed, err := f.service.LaunchCampaign(context.Background(), f.identity, campaign); err != nil || failed != 1 {
		t.Fatalf("launch failed=%d err=%v, want one failure", failed, err)
	}
	records, _, err := store.QueryMessages(context.Background(), f.service.ClickHouse,
		f.identity.TenantID, store.MessageFilter{CampaignID: &campaignID, Limit: 10})
	if err != nil || len(records) != 1 {
		t.Fatalf("rows = %d (%v), want 1", len(records), err)
	}
	row := records[0]
	if row.Status != "undelivered" || row.ErrorCode == nil || *row.ErrorCode != "rich_content_unsupported" ||
		row.CostMinor != 0 || len(gateway.submissions) != 0 {
		t.Errorf("row = %s %v cost %d, carrier saw %d", row.Status, row.ErrorCode, row.CostMinor,
			len(gateway.submissions))
	}
}
