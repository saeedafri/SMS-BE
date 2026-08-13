-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE tenants (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text        NOT NULL,
    status      text        NOT NULL DEFAULT 'active'
                            CHECK (status IN ('active','suspended','throttled')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Every tenant-owned table follows this exact shape: RLS on, FORCE so even the
-- table owner is subject to it, and a policy keyed to the per-transaction
-- setting the API writes from the verified token. See docs/ARCHITECTURE.md §7.
--
-- current_setting(..., true) returns NULL rather than erroring when the setting
-- is absent, so a connection that never called set_config sees no rows at all —
-- failing closed, which is the behaviour we want.
ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON tenants
    USING (id = current_setting('app.tenant_id', true)::uuid);

GRANT SELECT, INSERT, UPDATE ON tenants TO sms_app;

-- +goose Down
DROP TABLE tenants;
