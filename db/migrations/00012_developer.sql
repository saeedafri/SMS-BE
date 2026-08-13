-- +goose Up

-- API keys authenticate the public send API, so this table is a credential
-- store and is treated like one.
--
-- The secret is never stored. Only a SHA-256 hash is kept, plus the short
-- display prefix the dashboard shows so a user can tell two keys apart. A
-- leaked database must not yield working credentials — and unlike a password,
-- an API key is high-entropy and machine-generated, so a fast hash is correct
-- here where argon2 would be correct for a password.
CREATE TABLE api_keys (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name         text NOT NULL,
    environment  text NOT NULL CHECK (environment IN ('live','test')),
    scopes       text[] NOT NULL DEFAULT '{}',
    key_prefix   text NOT NULL,
    key_hash     bytea NOT NULL,
    status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked')),
    last_used_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Unique across all tenants: the lookup at authentication time has no tenant
-- to scope by — the key IS the tenant claim — so a collision would be a
-- cross-tenant authentication bug.
CREATE UNIQUE INDEX api_keys_hash ON api_keys (key_hash);
CREATE INDEX api_keys_page ON api_keys (tenant_id, created_at DESC);

CREATE TABLE webhook_endpoints (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    environment           text NOT NULL CHECK (environment IN ('live','test')),
    url                   text NOT NULL,
    subscribed_events     text[] NOT NULL DEFAULT '{}',
    -- Same rule as API keys: the signing secret is what proves a payload came
    -- from us, so only its hash and display prefix live here.
    signing_secret_prefix text NOT NULL,
    signing_secret_hash   bytea NOT NULL,
    status                text NOT NULL DEFAULT 'enabled'
                          CHECK (status IN ('enabled','disabled')),
    created_at            timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX webhook_endpoints_tenant ON webhook_endpoints (tenant_id, created_at DESC);

-- Delivery attempts. Append-only by convention rather than by trigger: unlike
-- the wallet ledger this is diagnostic rather than financial, and a retry
-- legitimately writes a new row per attempt.
CREATE TABLE webhook_events (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    endpoint_id      uuid NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
    event_type       text NOT NULL,
    payload          jsonb NOT NULL DEFAULT '{}'::jsonb,
    attempt          integer NOT NULL DEFAULT 1,
    outcome          text NOT NULL CHECK (outcome IN ('succeeded','failed')),
    http_status      integer,
    response_snippet text,
    occurred_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX webhook_events_page
    ON webhook_events (endpoint_id, occurred_at DESC, id DESC);

CREATE TABLE ip_allowlist (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    environment text NOT NULL CHECK (environment IN ('live','test')),
    cidr        text NOT NULL,
    label       text,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX ip_allowlist_tenant ON ip_allowlist (tenant_id, created_at DESC);

ALTER TABLE api_keys           ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_keys           FORCE  ROW LEVEL SECURITY;
ALTER TABLE webhook_endpoints  ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_endpoints  FORCE  ROW LEVEL SECURITY;
ALTER TABLE webhook_events     ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_events     FORCE  ROW LEVEL SECURITY;
ALTER TABLE ip_allowlist       ENABLE ROW LEVEL SECURITY;
ALTER TABLE ip_allowlist       FORCE  ROW LEVEL SECURITY;

CREATE POLICY api_keys_tenant_isolation ON api_keys
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY webhook_endpoints_tenant_isolation ON webhook_endpoints
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY webhook_events_tenant_isolation ON webhook_events
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY ip_allowlist_tenant_isolation ON ip_allowlist
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());

-- +goose StatementBegin
-- Authenticating an API key is the same bootstrap problem as resolving a
-- session: RLS on api_keys cannot be satisfied before the tenant is known, and
-- the key is what establishes it. SECURITY DEFINER is the deliberate, minimal
-- escape hatch — it returns exactly the tenant and scopes, never the hash.
CREATE FUNCTION resolve_api_key(hash bytea)
RETURNS TABLE (key_id uuid, tenant_id uuid, scopes text[], environment text)
LANGUAGE sql SECURITY DEFINER SET search_path = public AS $$
    SELECT k.id, k.tenant_id, k.scopes, k.environment
    FROM api_keys k
    WHERE k.key_hash = hash AND k.status = 'active';
$$;
-- +goose StatementEnd

GRANT SELECT, INSERT, UPDATE, DELETE ON api_keys TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON webhook_endpoints TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON webhook_events TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ip_allowlist TO sms_app;
GRANT EXECUTE ON FUNCTION resolve_api_key(bytea) TO sms_app;

-- +goose Down
DROP FUNCTION resolve_api_key(bytea);
DROP TABLE ip_allowlist;
DROP TABLE webhook_events;
DROP TABLE webhook_endpoints;
DROP TABLE api_keys;
