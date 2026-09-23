package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Ask 67. A pending retry is a row, not a sleeping goroutine, so a process
// that restarts inside the retry window still makes it — at its time, once.

func (h *harness) retryRow(endpointID uuid.UUID) (state string, attempt int) {
	h.t.Helper()
	if err := h.admin.QueryRow(context.Background(), `
		SELECT state, attempt FROM webhook_retries WHERE endpoint_id = $1`,
		endpointID).Scan(&state, &attempt); err != nil {
		h.t.Fatalf("read retry row: %v", err)
	}
	return state, attempt
}

func (h *harness) sweepRetries() {
	h.t.Helper()
	if err := h.server.RetryDueWebhooks(context.Background()); err != nil {
		h.t.Logf("retry sweep: %v", err)
	}
}

func TestAPendingWebhookRetryIsMadeOnTimeAfterARestartAndOnlyOnce(t *testing.T) {
	h := newSendHarness(t)
	h.server.WebhookRetryDelays = []time.Duration{5 * time.Minute}
	endpoint := startCustomerEndpoint(t, http.StatusInternalServerError, http.StatusOK)
	tenant := h.newAccount("owner")
	created := h.subscribeEndpoint(tenant, endpoint, "message.delivered")

	h.deliverOneSMS(tenant, "9876543210")
	waitFor(t, "the failed first attempt", func() bool {
		return len(endpoint.received("message.delivered")) == 1
	})
	waitFor(t, "the retry to be recorded", func() bool {
		var n int
		_ = h.admin.QueryRow(context.Background(),
			`SELECT count(*) FROM webhook_retries WHERE endpoint_id = $1`, created.Id).Scan(&n)
		return n == 1
	})
	if state, attempt := h.retryRow(created.Id); state != "pending" || attempt != 2 {
		t.Fatalf("retry = %s attempt %d, want pending attempt 2", state, attempt)
	}

	// Nothing in memory survives a restart; the sweep reads only the table.
	// Before the retry is due, a sweep makes nothing.
	h.sweepRetries()
	if got := len(endpoint.received("message.delivered")); got != 1 {
		t.Fatalf("deliveries before the retry was due = %d, want 1", got)
	}

	later := time.Now().Add(6 * time.Minute)
	h.server.Now = func() time.Time { return later }
	h.sweepRetries()
	if got := len(endpoint.received("message.delivered")); got != 2 {
		t.Fatalf("deliveries once due = %d, want 2", got)
	}
	if state, _ := h.retryRow(created.Id); state != "succeeded" {
		t.Errorf("retry state = %s, want succeeded", state)
	}

	h.server.Now = func() time.Time { return later.Add(time.Hour) }
	h.sweepRetries()
	if got := len(endpoint.received("message.delivered")); got != 2 {
		t.Errorf("deliveries after success = %d, want 2 — a delivered retry was made again", got)
	}
}

func TestAWebhookRetryThatExhaustsItsScheduleIsMarkedAbandoned(t *testing.T) {
	h := newSendHarness(t)
	h.server.WebhookRetryDelays = []time.Duration{time.Millisecond}
	endpoint := startCustomerEndpoint(t, http.StatusInternalServerError)
	tenant := h.newAccount("owner")
	created := h.subscribeEndpoint(tenant, endpoint, "message.delivered")

	h.deliverOneSMS(tenant, "9876543210")
	waitFor(t, "the retry to give up", func() bool {
		h.sweepRetries()
		var state string
		_ = h.admin.QueryRow(context.Background(),
			`SELECT state FROM webhook_retries WHERE endpoint_id = $1`, created.Id).Scan(&state)
		return state == "abandoned"
	})
	if got := len(endpoint.received("message.delivered")); got != 2 {
		t.Errorf("attempts = %d, want 2 (the first and its one retry)", got)
	}
}
