package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/mailer"
)

// Ask 68. Alerts and scheduled reports reach their recipients through the
// transactional mailer. Resend is replaced by a recorder here; an address
// containing "bounce" is refused the way Resend refuses a bad one.

type sentMail struct{ To, Subject string }

type mailRecorder struct {
	mu   sync.Mutex
	sent []sentMail
}

func (r *mailRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var body struct {
		To      []string `json:"to"`
		Subject string   `json:"subject"`
	}
	_ = json.NewDecoder(req.Body).Decode(&body)
	status := http.StatusOK
	if strings.Contains(body.To[0], "bounce") {
		status = http.StatusUnprocessableEntity
	} else {
		r.mu.Lock()
		r.sent = append(r.sent, sentMail{To: body.To[0], Subject: body.Subject})
		r.mu.Unlock()
	}
	return &http.Response{StatusCode: status, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(`{"id":"x"}`))}, nil
}

// to counts mail delivered to one address. Sweeps run across every tenant in
// the shared test database, so tests count by their own unique addresses.
func (r *mailRecorder) to(address string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, mail := range r.sent {
		if mail.To == address {
			n++
		}
	}
	return n
}

func (h *harness) recordMail() *mailRecorder {
	recorder := &mailRecorder{}
	h.server.Mail = &mailer.Mailer{APIKey: "re_test", From: "Relay <r@x.test>",
		Logger: h.server.Logger, Client: &http.Client{Transport: recorder}}
	return recorder
}

func uniqueAddress(name string) string {
	return name + "-" + uuid.NewString()[:8] + "@example.test"
}

func (h *harness) setLowBalanceAlert(acct account, enabled bool, threshold int, recipients ...string) {
	h.t.Helper()
	res := h.do(http.MethodPatch, "/v1/alerts", acct.Token, map[string]any{
		"lowBalance": []any{map[string]any{"currency": "INR", "enabled": enabled,
			"thresholdMinor": threshold, "recipients": recipients}},
	})
	if res.Code != http.StatusOK {
		h.t.Fatalf("update alerts = %d\n%s", res.Code, res.Body)
	}
}

func (h *harness) evaluateAlerts() {
	h.t.Helper()
	if err := h.server.EvaluateAlerts(context.Background()); err != nil {
		h.t.Logf("evaluate alerts: %v", err)
	}
}

func TestALowBalanceAlertEmailsOncePerBreach(t *testing.T) {
	h := newHarness(t)
	mail := h.recordMail()
	tenant := h.newAccount("owner")
	h.fundWallet(tenant) // INR 10,000.00
	first, second := uniqueAddress("finance"), uniqueAddress("ops")

	h.setLowBalanceAlert(tenant, true, 2_000_000, first, second)
	h.evaluateAlerts()
	if mail.to(first) != 1 || mail.to(second) != 1 {
		t.Fatalf("on breach: %d and %d emails, want one each", mail.to(first), mail.to(second))
	}

	h.evaluateAlerts()
	if mail.to(first) != 1 {
		t.Fatalf("still in breach: %d emails, want still 1", mail.to(first))
	}

	h.setLowBalanceAlert(tenant, true, 500_000, first, second) // recovered
	h.evaluateAlerts()
	h.setLowBalanceAlert(tenant, true, 2_000_000, first, second) // breached again
	h.evaluateAlerts()
	if mail.to(first) != 2 {
		t.Errorf("after recovering and breaching again: %d emails, want 2", mail.to(first))
	}
}

func TestADisabledAlertNeverEmails(t *testing.T) {
	h := newHarness(t)
	mail := h.recordMail()
	tenant := h.newAccount("owner")
	h.fundWallet(tenant)
	address := uniqueAddress("finance")

	h.setLowBalanceAlert(tenant, false, 2_000_000, address)
	h.evaluateAlerts()
	if got := mail.to(address); got != 0 {
		t.Errorf("disabled rule in breach sent %d emails, want 0", got)
	}
}

type reportView struct {
	ID          string      `json:"id"`
	Paused      bool        `json:"paused"`
	RecentSends []time.Time `json:"recentSends"`
	NextSendAt  *time.Time  `json:"nextSendAt"`
}

func (h *harness) report(acct account, id string) reportView {
	h.t.Helper()
	var list []reportView
	h.do(http.MethodGet, "/v1/analytics/reports", acct.Token, nil).decode(h.t, &list)
	for _, report := range list {
		if report.ID == id {
			return report
		}
	}
	h.t.Fatalf("report %s not listed", id)
	return reportView{}
}

func TestAScheduledReportIsEmailedWhenDueAndItsHistoryIsReal(t *testing.T) {
	h := newSendHarness(t)
	mail := h.recordMail()
	tenant := h.newAccount("owner")
	good, bounce := uniqueAddress("ceo"), uniqueAddress("bounce")

	res := h.do(http.MethodPost, "/v1/analytics/reports", tenant.Token, map[string]any{
		"frequency": "daily", "range": "7d", "recipients": []string{bounce, good},
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("create report = %d\n%s", res.Code, res.Body)
	}
	var created reportView
	res.decode(t, &created)
	if len(created.RecentSends) != 0 {
		t.Fatalf("a new report claims %d sends", len(created.RecentSends))
	}
	if created.NextSendAt == nil || time.Until(*created.NextSendAt) < 23*time.Hour {
		t.Fatalf("nextSendAt = %v, want about a day away", created.NextSendAt)
	}

	sweep := func() {
		if err := h.server.SendDueReports(context.Background()); err != nil {
			t.Logf("send reports: %v", err)
		}
	}
	sweep()
	if mail.to(good) != 0 {
		t.Fatalf("sent before it was due")
	}

	if _, err := h.admin.Exec(context.Background(),
		`UPDATE scheduled_reports SET next_send_at = now() - interval '1 minute' WHERE id = $1`,
		created.ID); err != nil {
		t.Fatal(err)
	}
	sweep()
	sweep()
	if got := mail.to(good); got != 1 {
		t.Fatalf("due report reached the good address %d times, want 1 — a bad address must not stop it", got)
	}
	after := h.report(tenant, created.ID)
	if len(after.RecentSends) != 1 {
		t.Errorf("recentSends = %v, want the one real send", after.RecentSends)
	}
	if after.NextSendAt == nil || !after.NextSendAt.After(time.Now()) {
		t.Errorf("nextSendAt = %v, want in the future", after.NextSendAt)
	}

	if res := h.do(http.MethodPatch, "/v1/analytics/reports/"+created.ID, tenant.Token,
		map[string]any{"paused": true}); res.Code != http.StatusOK {
		t.Fatalf("pause = %d\n%s", res.Code, res.Body)
	}
	paused := h.report(tenant, created.ID)
	if paused.NextSendAt != nil {
		t.Errorf("paused nextSendAt = %v, want null", paused.NextSendAt)
	}
	if _, err := h.admin.Exec(context.Background(),
		`UPDATE scheduled_reports SET next_send_at = now() - interval '1 minute' WHERE id = $1`,
		created.ID); err != nil {
		t.Fatal(err)
	}
	sweep()
	if got := mail.to(good); got != 1 {
		t.Errorf("a paused report sent: %d emails, want still 1", got)
	}
}
