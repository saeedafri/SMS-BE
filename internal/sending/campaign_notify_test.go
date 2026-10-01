package sending_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/connector"
)

// heard records what the send path told a live screen, per campaign.
type heard struct {
	mu       sync.Mutex
	status   map[uuid.UUID]int
	progress map[uuid.UUID]int
}

func (h *heard) CampaignStatusChanged(_ context.Context, _, campaignID uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status[campaignID]++
}

func (h *heard) CampaignProgressed(_ context.Context, _, campaignID uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.progress[campaignID]++
}

func (h *heard) counts(campaignID uuid.UUID) (status, progress int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status[campaignID], h.progress[campaignID]
}

func listening(f *fixture) *heard {
	h := &heard{status: map[uuid.UUID]int{}, progress: map[uuid.UUID]int{}}
	f.service.Notifier = h
	return h
}

// H4 E1 and E2 on the real send path: a launch announces the campaign starting
// and landing, and every later change to its messages — a delivery report, a
// late submit answer — announces progress. A screen that misses any of these
// settles on a stale number, which is what the stream exists to prevent.
func TestACampaignTellsTheScreenAtEveryStep(t *testing.T) {
	f := newFixture(t)
	h := listening(f)

	listID, _ := f.seedList("Live screen", 2)
	campaign := f.seedCampaign(f.templateID, listID, "sending", 2)
	if _, _, err := f.service.LaunchCampaign(context.Background(), f.identity, campaign); err != nil {
		t.Fatalf("launch: %v", err)
	}
	status, progress := h.counts(campaign.ID)
	if status < 2 {
		t.Errorf("launch announced %d status changes, want sending and its landing (2)", status)
	}
	if progress < 1 {
		t.Errorf("launch announced no progress, want one per page")
	}

	for _, report := range f.sandbox.DrainReports() {
		if err := f.service.ApplyDeliveryReport(context.Background(), f.identity, report); err != nil {
			t.Fatalf("apply report: %v", err)
		}
	}
	if _, after := h.counts(campaign.ID); after < progress+2 {
		t.Errorf("two delivery reports announced %d progress, want 2", after-progress)
	}
}

func TestALateSubmitOnACampaignMessageIsAnnounced(t *testing.T) {
	f := newFixture(t)
	ids := f.sendOneEachWay(scriptedCarrier{answer: answering("SUBMIT_TIMEOUT", false)})
	h := listening(f)

	campaignID := f.message(ids["campaign"]).CampaignID
	if campaignID == nil {
		t.Fatal("the campaign message carries no campaign id")
	}
	if err := f.service.ApplyLateSubmit(context.Background(), connector.LateSubmit{
		MessageID: ids["campaign"].String(), Accepted: true, CarrierRef: "late-campaign",
	}); err != nil {
		t.Fatalf("apply late submit: %v", err)
	}
	if _, progress := h.counts(*campaignID); progress != 1 {
		t.Errorf("a late submit announced %d progress events, want 1", progress)
	}
}
