-- +goose Up

-- Per-tenant settings that are single-row by nature. One table with a
-- tenant_id primary key rather than a table per setting: they are read
-- together on the settings screens, and a join per toggle buys nothing.
CREATE TABLE tenant_settings (
    tenant_id                  uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,

    -- SSO. Nullable columns rather than a separate table because a tenant has
    -- at most one identity provider.
    sso_enabled                boolean NOT NULL DEFAULT false,
    sso_provider               text CHECK (sso_provider IN ('saml','oidc')),
    sso_metadata_url           text,
    sso_entity_id              text,

    -- Retention is stored here but enforced by the ClickHouse TTL, which is
    -- what actually deletes rows. Storing a number the storage layer ignores
    -- would be a lie told in a settings screen, so the reconciliation between
    -- the two is deliberate: see docs/ARCHITECTURE.md.
    message_log_retention_days integer NOT NULL DEFAULT 90
                               CHECK (message_log_retention_days IN (30,90,180,365)),

    updated_at                 timestamptz NOT NULL DEFAULT now()
);

-- Scheduled analytics reports emailed to a recipient.
CREATE TABLE scheduled_reports (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    frequency   text NOT NULL CHECK (frequency IN ('daily','weekly','monthly')),
    range_key   text NOT NULL DEFAULT '30d' CHECK (range_key IN ('7d','30d','90d')),
    recipients  text[] NOT NULL DEFAULT '{}',
    paused      boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX scheduled_reports_tenant ON scheduled_reports (tenant_id, created_at DESC);

ALTER TABLE tenant_settings   ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_settings   FORCE  ROW LEVEL SECURITY;
ALTER TABLE scheduled_reports ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduled_reports FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_settings_isolation ON tenant_settings
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY scheduled_reports_isolation ON scheduled_reports
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_settings TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON scheduled_reports TO sms_app;

-- +goose Down
DROP TABLE scheduled_reports;
DROP TABLE tenant_settings;
