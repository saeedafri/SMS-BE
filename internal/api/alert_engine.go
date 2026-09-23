package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/saeedafri/sms-be/internal/store"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// Alerts and scheduled reports are operational email to a tenant's own team:
// not metered, not priced, not held against a wallet. That is the
// transactional traffic internal/mailer exists for, so both go through s.Mail.
//
// With no RESEND_API_KEY, Mail only logs. Both engines then do nothing at all
// rather than mark an alert fired or a report sent that nobody received.

// alertCheck is one rule's verdict for this cycle.
type alertCheck struct {
	key        string
	enabled    bool
	recipients []string
	breached   bool
	subject    string
	detail     string
}

// EvaluateAlerts checks every tenant's enabled alert rules and emails a rule's
// recipients once when it goes into breach. It stays quiet while the breach
// lasts and re-arms when the metric recovers, so a second breach emails again.
func (s *Server) EvaluateAlerts(ctx context.Context) error {
	if s.OperatorDB == nil || !s.Mail.Enabled() {
		return nil
	}
	tenants, err := store.TenantsWithAlertRules(ctx, s.OperatorDB)
	if err != nil {
		return err
	}
	var failures []error
	for _, tenantID := range tenants {
		identity := store.Identity{TenantID: tenantID}
		if err := s.evaluateTenantAlerts(ctx, identity); err != nil {
			failures = append(failures, fmt.Errorf("tenant %s: %w", tenantID, err))
		}
	}
	return errors.Join(failures...)
}

func (s *Server) evaluateTenantAlerts(ctx context.Context, identity store.Identity) error {
	rules, err := s.loadAlertRules(ctx, identity)
	if err != nil {
		return err
	}
	checks, err := s.checkAlerts(ctx, identity, rules)
	if err != nil {
		return err
	}
	breached, err := store.BreachedAlerts(ctx, s.DB, identity)
	if err != nil {
		return err
	}

	var failures []error
	for _, check := range checks {
		active := check.enabled && len(check.recipients) > 0
		switch {
		case active && check.breached && !breached[check.key]:
			if s.emailAll(ctx, check.recipients, check.subject,
				layout(check.subject, check.detail, "Open alert settings",
					s.appBaseURL()+"/settings/alerts",
					"You get this because your address is on this alert in Relay. "+
						"It will not repeat until the figure recovers and crosses the threshold again.")) == 0 {
				// Nobody got it: leave the rule armed so the next cycle tries again.
				continue
			}
			failures = append(failures,
				store.SetAlertBreached(ctx, s.DB, identity, check.key, true, true))
		case breached[check.key] && (!active || !check.breached):
			// Recovered, or switched off: re-arm, so the next breach is news.
			failures = append(failures,
				store.SetAlertBreached(ctx, s.DB, identity, check.key, false, false))
		}
	}
	return errors.Join(failures...)
}

// checkAlerts measures each rule against live data. Only enabled rules cost a
// query; a disabled one is reported unbreached.
func (s *Server) checkAlerts(ctx context.Context, identity store.Identity,
	rules gen.AlertRules) ([]alertCheck, error) {

	var checks []alertCheck

	balances, err := store.ListWalletBalances(ctx, s.DB, identity)
	if err != nil {
		return nil, err
	}
	for _, rule := range rules.LowBalance {
		check := alertCheck{key: "low_balance:" + string(rule.Currency),
			enabled: rule.Enabled, recipients: rule.Recipients}
		for _, balance := range balances {
			if balance.Currency == string(rule.Currency) && rule.Enabled {
				check.breached = balance.BalanceMinor < int64(rule.ThresholdMinor)
				check.subject = fmt.Sprintf("Your %s balance is low", rule.Currency)
				check.detail = fmt.Sprintf("Your %s wallet balance is %s, below your alert threshold of %s. "+
					"Top up to keep sending.", rule.Currency,
					money(balance.BalanceMinor, balance.Currency),
					money(int64(rule.ThresholdMinor), balance.Currency))
			}
		}
		checks = append(checks, check)
	}

	floor, spend, volume := rules.DeliveryFloor, rules.SpendCeiling, rules.VolumeCeiling
	checks = append(checks,
		alertCheck{key: "delivery_floor", enabled: floor.Enabled, recipients: floor.Recipients},
		alertCheck{key: "spend_ceiling", enabled: spend.Enabled, recipients: spend.Recipients},
		alertCheck{key: "volume_ceiling", enabled: volume.Enabled, recipients: volume.Recipients})
	if !floor.Enabled && !spend.Enabled && !volume.Enabled {
		return checks, nil
	}
	conn, err := s.clickhouse(ctx)
	if err != nil {
		return nil, err
	}
	last := len(checks) - 3

	if floor.Enabled {
		summary, _, _, err := store.QueryAnalytics(ctx, conn, identity.TenantID,
			store.AnalyticsFilter{Since: rangeSince(string(floor.Range))})
		if err != nil {
			return nil, err
		}
		// No traffic is not a delivery problem, so it never breaches.
		if summary.Sent > 0 {
			rate := 100 * float64(summary.Delivered) / float64(summary.Sent)
			checks[last].breached = rate < float64(floor.ThresholdPercent)
			checks[last].subject = "Your delivery rate has dropped"
			checks[last].detail = fmt.Sprintf("%.1f%% of your messages were delivered over the last %s "+
				"(%d of %d), below your alert floor of %.1f%%.", rate, floor.Range,
				summary.Delivered, summary.Sent, floor.ThresholdPercent)
		}
	}
	if spend.Enabled {
		usage, err := store.UsageByChannel(ctx, conn, identity.TenantID, rangeSince("30d"), string(spend.Currency))
		if err != nil {
			return nil, err
		}
		var total int64
		for _, row := range usage {
			total += row.AmountMinor
		}
		checks[last+1].breached = total > int64(spend.ThresholdMinor)
		checks[last+1].subject = "Your spend has passed its ceiling"
		checks[last+1].detail = fmt.Sprintf("You have spent %s over the last 30 days, above your alert ceiling of %s.",
			money(total, string(spend.Currency)), money(int64(spend.ThresholdMinor), string(spend.Currency)))
	}
	if volume.Enabled {
		usage, err := store.UsageByChannel(ctx, conn, identity.TenantID, rangeSince("30d"), "")
		if err != nil {
			return nil, err
		}
		total := 0
		for _, row := range usage {
			total += row.MessageCount
		}
		checks[last+2].breached = total > volume.ThresholdCount
		checks[last+2].subject = "Your message volume has passed its ceiling"
		checks[last+2].detail = fmt.Sprintf("%d messages were delivered over the last 30 days, "+
			"above your alert ceiling of %d.", total, volume.ThresholdCount)
	}
	return checks, nil
}

// SendDueReports emails every unpaused scheduled report whose time has come and
// records each send, which is what the report's recentSends then shows.
func (s *Server) SendDueReports(ctx context.Context) error {
	if s.OperatorDB == nil || !s.Mail.Enabled() {
		return nil
	}
	due, err := store.DueScheduledReports(ctx, s.OperatorDB, s.now(), 50)
	if err != nil {
		return err
	}
	var failures []error
	for _, report := range due {
		if err := s.sendReport(ctx, report); err != nil {
			failures = append(failures, fmt.Errorf("report %s: %w", report.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (s *Server) sendReport(ctx context.Context, report store.ScheduledReport) error {
	identity := store.Identity{TenantID: report.TenantID}
	claimed, err := store.ClaimScheduledReport(ctx, s.DB, identity, report)
	if err != nil || !claimed {
		return err
	}
	conn, err := s.clickhouse(ctx)
	if err != nil {
		return err
	}
	summary, _, _, err := store.QueryAnalytics(ctx, conn, report.TenantID,
		store.AnalyticsFilter{Since: rangeSince(report.Range)})
	if err != nil {
		return err
	}
	rate := 0.0
	if summary.Sent > 0 {
		rate = 100 * float64(summary.Delivered) / float64(summary.Sent)
	}
	subject := fmt.Sprintf("Your Relay %s report", report.Frequency)
	cost := "none"
	if summary.Currency != "" {
		cost = money(summary.CostMinor, summary.Currency)
	}
	detail := fmt.Sprintf("Over the last %s: %d messages sent, %d delivered, %d failed — "+
		"a %.1f%% delivery rate. Delivered cost: %s.", report.Range,
		summary.Sent, summary.Delivered, summary.Failed, rate, cost)
	if summary.CurrencyMixed {
		detail += " Cost spans more than one currency; the analytics page splits it."
	}
	body := layout(subject, detail, "Open analytics", s.appBaseURL()+"/analytics",
		"You get this because your address is on a scheduled report in Relay. "+
			"Pause or delete it on the analytics page.")

	sent := s.emailAll(ctx, report.Recipients, subject, body)
	if sent == 0 {
		return fmt.Errorf("no recipient could be emailed")
	}
	return store.RecordReportSend(ctx, s.DB, identity, report.ID, sent)
}

// emailAll sends to each address in turn, so one bad address does not stop
// the rest, and reports how many went.
func (s *Server) emailAll(ctx context.Context, recipients []string, subject, body string) int {
	sent := 0
	for _, to := range recipients {
		to = strings.TrimSpace(to)
		if to == "" {
			continue
		}
		sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := s.Mail.Send(sendCtx, to, subject, body)
		cancel()
		if err != nil {
			s.Logger.Warn("notification email failed", "to", to, "subject", subject, "error", err)
			continue
		}
		sent++
	}
	return sent
}
