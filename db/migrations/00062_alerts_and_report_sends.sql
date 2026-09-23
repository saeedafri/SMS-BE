-- +goose Up

-- Alerts and scheduled reports stop being settings nobody acts on.

-- Whether each alert rule is currently in breach, so a breach emails once and
-- then stays quiet until the metric recovers and breaches again. rule is
-- 'low_balance:<CURRENCY>', 'delivery_floor', 'spend_ceiling' or
-- 'volume_ceiling'. last_fired_at is kept for the day someone asks "when did
-- that alert last go off"; nothing on the wire reads it yet.
CREATE TABLE alert_state (
    tenant_id     uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    rule          text        NOT NULL,
    breached      boolean     NOT NULL DEFAULT false,
    last_fired_at timestamptz,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, rule)
);

ALTER TABLE alert_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_state FORCE  ROW LEVEL SECURITY;
CREATE POLICY alert_state_isolation ON alert_state
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON alert_state TO sms_app;

-- When each report is next owed. Stored, not derived: the derived schedule
-- claimed sends that never happened. Existing reports start one period from
-- now rather than from their creation, so the deploy does not email every
-- recipient a backlog at once.
ALTER TABLE scheduled_reports ADD COLUMN next_send_at timestamptz;
UPDATE scheduled_reports SET next_send_at = now() + CASE frequency
    WHEN 'daily'  THEN interval '1 day'
    WHEN 'weekly' THEN interval '7 days'
    ELSE interval '1 month' END;
ALTER TABLE scheduled_reports ALTER COLUMN next_send_at SET NOT NULL;
CREATE INDEX scheduled_reports_due ON scheduled_reports (next_send_at) WHERE NOT paused;

-- Every send that actually went out: what recentSends reads.
CREATE TABLE report_sends (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    report_id  uuid        NOT NULL REFERENCES scheduled_reports(id) ON DELETE CASCADE,
    sent_at    timestamptz NOT NULL DEFAULT now(),
    recipients integer     NOT NULL
);
CREATE INDEX report_sends_report ON report_sends (report_id, sent_at DESC);

ALTER TABLE report_sends ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_sends FORCE  ROW LEVEL SECURITY;
CREATE POLICY report_sends_isolation ON report_sends
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT ON report_sends TO sms_app;

-- The two sweeps find work across tenants through the operator pool, as the
-- campaign scheduler does. Additive, as 00019 established.
CREATE POLICY alert_rules_operator ON alert_rules
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());
CREATE POLICY scheduled_reports_operator ON scheduled_reports
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

-- +goose Down
DROP POLICY scheduled_reports_operator ON scheduled_reports;
DROP POLICY alert_rules_operator ON alert_rules;
DROP TABLE report_sends;
ALTER TABLE scheduled_reports DROP COLUMN next_send_at;
DROP TABLE alert_state;
