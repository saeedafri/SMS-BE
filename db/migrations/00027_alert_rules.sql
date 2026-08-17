-- +goose Up
-- Alert rules had no storage at all. GetAlerts returned four hardcoded, always-
-- disabled rule groups and UpdateAlerts answered 501, so the alerts screen could
-- be filled in, saved, and would silently forget everything.
--
-- Stored as one jsonb document per tenant rather than a table per rule type.
-- This is settings data: it is read whole when the screen opens, written whole
-- when it is saved, and never queried by field — nothing asks "which tenants
-- have a spend ceiling above X". Four normalised tables would buy query shapes
-- nobody needs and cost four joins on every page load.
--
-- The shape inside is the contract's own AlertRules, so the API reads and writes
-- it without a translation layer that could drift from the schema.
CREATE TABLE alert_rules (
    tenant_id  uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    rules      jsonb       NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE alert_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_rules FORCE  ROW LEVEL SECURITY;

-- Both clauses, deliberately. USING governs SELECT, UPDATE and DELETE but NOT
-- INSERT: a policy with only USING silently rejects every insert, with no error
-- message anyone can search for. Every policy in this schema carries both.
CREATE POLICY alert_rules_isolation ON alert_rules
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON alert_rules TO sms_app;

-- +goose Down
DROP TABLE alert_rules;
